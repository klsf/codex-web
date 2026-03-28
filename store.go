package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *sessionStore) ensureSession(sessionID string) *Session {
	return s.ensureSessionWithWorkdir(sessionID, "")
}

func (s *sessionStore) ensureSessionWithWorkdir(sessionID, workdir string) *Session {
	return s.ensureRuntime(sessionID, workdir).session
}

func (s *sessionStore) ensureRuntime(sessionID, workdir string) *sessionRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sessionID != "" {
		if rt, ok := s.sessions[sessionID]; ok {
			if rt.session.Workdir == "" {
				rt.session.Workdir = normalizeWorkdir(workdir)
			}
			return rt
		}
	}

	now := time.Now()
	session := &Session{
		ID:        uuid.NewString(),
		Workdir:   normalizeWorkdir(workdir),
		Messages:  make([]Message, 0, 16),
		Events:    make([]EventLog, 0, 32),
		UpdatedAt: now,
	}
	rt := &sessionRuntime{
		session: session,
		clients: make(map[*clientConn]struct{}),
	}
	s.sessions[session.ID] = rt
	if err := s.saveLocked(session); err != nil {
		log.Printf("save new session: %v", err)
	}
	return rt
}

func (s *sessionStore) currentModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.meta.Model)
}

func (s *sessionStore) currentApprovalPolicy() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.meta.ApprovalPolicy)
}

func (s *sessionStore) currentServiceTier() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.meta.ServiceTier)
}

func (s *sessionStore) currentFastMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.meta.FastMode
}

func (s *sessionStore) setModel(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return s.currentModel(), nil
	}
	s.mu.Lock()
	s.meta.Model = value
	s.mu.Unlock()
	if s.app != nil {
		s.app.InvalidateLoadedThreads()
	}
	s.broadcastMeta()
	return value, nil
}

func (s *sessionStore) setApprovalPolicy(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return s.currentApprovalPolicy(), nil
	}
	if value != "never" {
		return "", errors.New("web mode currently only supports approvals=never")
	}
	s.mu.Lock()
	s.meta.ApprovalPolicy = value
	s.mu.Unlock()
	s.broadcastMeta()
	return value, nil
}

func (s *sessionStore) setFastMode(value string) (bool, string, error) {
	mode := strings.TrimSpace(strings.ToLower(value))
	if mode == "" {
		if s.currentFastMode() {
			mode = "off"
		} else {
			mode = "on"
		}
	}
	switch mode {
	case "status":
		return s.currentFastMode(), s.currentServiceTier(), nil
	case "on":
		if err := s.writeServiceTier("fast"); err != nil {
			return false, "", err
		}
		s.mu.Lock()
		s.meta.ServiceTier = "fast"
		s.meta.FastMode = true
		s.mu.Unlock()
		s.resetSessionThreads()
	case "off":
		if err := s.clearServiceTier(); err != nil {
			return false, "", err
		}
		s.mu.Lock()
		s.meta.ServiceTier = ""
		s.meta.FastMode = false
		s.mu.Unlock()
		s.resetSessionThreads()
	default:
		return false, "", errors.New("usage: /fast [on|off|status]")
	}
	if s.app != nil {
		s.app.InvalidateLoadedThreads()
	}
	s.broadcastMeta()
	return s.currentFastMode(), s.currentServiceTier(), nil
}

func (s *sessionStore) writeServiceTier(value string) error {
	if s.app == nil {
		return errors.New("codex app-server is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return s.app.WriteConfigValue(ctx, "service_tier", value)
}

func (s *sessionStore) clearServiceTier() error {
	if s.app == nil {
		return errors.New("codex app-server is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return s.app.ClearConfigValue(ctx, "service_tier")
}

func (s *sessionStore) resetSessionThreads() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, rt := range s.sessions {
		if rt.session.CodexThreadID == "" && rt.session.ActiveTurnID == "" {
			continue
		}
		rt.session.CodexThreadID = ""
		rt.session.ActiveTurnID = ""
		rt.session.UpdatedAt = time.Now()
		if err := s.saveLocked(rt.session); err != nil {
			log.Printf("save reset session thread: %v", err)
		}
	}
}

func (s *sessionStore) stopActiveTask(sessionID string) (bool, error) {
	if s.app == nil {
		return false, errors.New("codex app-server is not available")
	}
	if s.activeTaskID(sessionID) == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.app.InterruptTurn(ctx, sessionID); err != nil {
		return false, err
	}
	s.appendEvent(sessionID, "status", "turn interrupted", "")
	return true, nil
}

func (s *sessionStore) compactSession(sessionID string) (bool, error) {
	if s.app == nil {
		return false, errors.New("codex app-server is not available")
	}
	if s.activeTaskID(sessionID) != "" {
		return false, errors.New("task is running, stop it before compacting")
	}
	session := s.cloneSession(sessionID)
	if session == nil {
		return false, errors.New("session not found")
	}
	if strings.TrimSpace(session.CodexThreadID) == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.app.CompactThread(ctx, session.CodexThreadID); err != nil {
		return false, err
	}
	s.appendEvent(sessionID, "status", "thread compact started", "")
	return true, nil
}

func (s *sessionStore) sessionWorkdir(sessionID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rt, ok := s.sessions[sessionID]
	if !ok {
		return defaultWorkdir
	}
	return normalizeWorkdir(rt.session.Workdir)
}

func (s *sessionStore) broadcastMeta() {
	s.mu.RLock()
	clients := make(map[*clientConn]struct{})
	for _, rt := range s.sessions {
		for client := range rt.clients {
			clients[client] = struct{}{}
		}
	}
	meta := s.meta
	s.mu.RUnlock()
	broadcastJSON(clients, serverEvent{Type: "meta_update", Meta: &meta})
}

func (s *sessionStore) enqueuePrompt(sessionID, content string, imageIDs []string) error {
	if content == "" && len(imageIDs) == 0 {
		return errors.New("message is empty")
	}
	if s.app == nil {
		return errors.New("codex app-server is not available")
	}

	imageURLs, imagePaths, err := resolveImageFiles(imageIDs)
	if err != nil {
		return err
	}

	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return errors.New("session not found")
	}
	if rt.session.ActiveTaskID != "" {
		s.mu.Unlock()
		return errors.New("a task is already running in this session")
	}

	userMsg := Message{
		ID:        uuid.NewString(),
		Role:      "user",
		Content:   content,
		ImageURLs: imageURLs,
		CreatedAt: time.Now(),
	}
	rt.session.Messages = append(rt.session.Messages, userMsg)
	rt.session.UpdatedAt = time.Now()
	taskID := uuid.NewString()
	rt.session.ActiveTaskID = taskID
	if err := s.saveLocked(rt.session); err != nil {
		log.Printf("save session before task: %v", err)
	}
	clients := cloneClients(rt.clients)
	s.mu.Unlock()

	broadcastJSON(clients, serverEvent{Type: "message", Message: &userMsg})
	broadcastJSON(clients, serverEvent{Type: "task_status", TaskID: taskID, Running: true})

	go s.runAppServerTask(sessionID, taskID, content, imagePaths)
	return nil
}

func (s *sessionStore) enqueueReview(sessionID, args string) error {
	var taskID string
	var clients map[*clientConn]struct{}
	commandText := "/review"
	if args != "" {
		commandText += " " + args
	}

	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return errors.New("session not found")
	}
	if rt.session.ActiveTaskID != "" {
		s.mu.Unlock()
		return errors.New("a task is already running in this session")
	}

	userMsg := Message{
		ID:        uuid.NewString(),
		Role:      "user",
		Content:   commandText,
		CreatedAt: time.Now(),
	}
	rt.session.Messages = append(rt.session.Messages, userMsg)
	rt.session.UpdatedAt = time.Now()
	taskID = uuid.NewString()
	rt.session.ActiveTaskID = taskID
	if err := s.saveLocked(rt.session); err != nil {
		log.Printf("save session before review: %v", err)
	}
	clients = cloneClients(rt.clients)
	s.mu.Unlock()

	broadcastJSON(clients, serverEvent{Type: "message", Message: &userMsg})
	broadcastJSON(clients, serverEvent{Type: "task_status", TaskID: taskID, Running: true})
	go s.runReviewTask(sessionID, taskID, args)
	return nil
}

func (s *sessionStore) runAppServerTask(sessionID, taskID, prompt string, imagePaths []string) {
	waited := s.acquireTaskSlot(sessionID)
	defer s.releaseTaskSlot()

	if waited {
		s.appendEvent(sessionID, "status", "task dequeued", "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), appServerRPCTimeout)
	defer cancel()

	if err := s.app.StartTurn(ctx, sessionID, taskID, s.sessionWorkdir(sessionID), prompt, imagePaths); err != nil {
		s.finishTaskWithError(sessionID, taskID, err)
	}
}

func (s *sessionStore) runReviewTask(sessionID, taskID, args string) {
	waited := s.acquireTaskSlot(sessionID)
	defer s.releaseTaskSlot()

	if waited {
		s.appendEvent(sessionID, "status", "task dequeued", "")
	}

	s.appendEvent(sessionID, "status", "review started", "")
	cmdArgs := []string{"review", "--uncommitted"}
	if model := s.currentModel(); model != "" {
		cmdArgs = append(cmdArgs, "-m", model)
	}
	if args != "" {
		cmdArgs = append(cmdArgs, args)
	}

	cmd := exec.CommandContext(context.Background(), "codex", cmdArgs...)
	cmd.Dir = s.sessionWorkdir(sessionID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		s.finishTaskWithError(sessionID, taskID, fmt.Errorf("review failed: %s", msg))
		return
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		result = "No review output."
	}
	s.appendMessage(sessionID, "assistant", result)
	s.finishTaskOK(sessionID, taskID)
}

func (s *sessionStore) acquireTaskSlot(sessionID string) bool {
	select {
	case s.taskSlots <- struct{}{}:
		return false
	default:
		s.appendEvent(sessionID, "status", "task queued", fmt.Sprintf("waiting for an available slot (%d max)", s.maxConcurrent))
		s.taskSlots <- struct{}{}
		return true
	}
}

func (s *sessionStore) releaseTaskSlot() {
	select {
	case <-s.taskSlots:
	default:
	}
}

func (s *sessionStore) appendAssistantDelta(sessionID, itemID, delta string) {
	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return
	}

	draft := rt.session.DraftMessage
	if draft == nil || draft.ID != itemID {
		draft = &Message{
			ID:        firstNonEmpty(itemID, uuid.NewString()),
			Role:      "assistant",
			CreatedAt: time.Now(),
		}
	}
	draft.Content += delta
	rt.session.DraftMessage = draft
	rt.session.UpdatedAt = time.Now()
	if err := s.saveLocked(rt.session); err != nil {
		log.Printf("save draft message: %v", err)
	}
	clients := cloneClients(rt.clients)
	copyDraft := *draft
	s.mu.Unlock()

	broadcastJSON(clients, serverEvent{Type: "message_delta", Message: &copyDraft})
}

func (s *sessionStore) completeAssistantMessage(sessionID, itemID, text string) {
	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return
	}

	var msg Message
	if rt.session.DraftMessage != nil && (itemID == "" || rt.session.DraftMessage.ID == itemID) {
		msg = *rt.session.DraftMessage
	} else {
		msg = Message{
			ID:        firstNonEmpty(itemID, uuid.NewString()),
			Role:      "assistant",
			CreatedAt: time.Now(),
		}
	}
	if strings.TrimSpace(text) != "" {
		msg.Content = text
	}
	if strings.TrimSpace(msg.Content) == "" {
		rt.session.DraftMessage = nil
		rt.session.UpdatedAt = time.Now()
		_ = s.saveLocked(rt.session)
		s.mu.Unlock()
		return
	}

	rt.session.Messages = append(rt.session.Messages, msg)
	rt.session.DraftMessage = nil
	rt.session.UpdatedAt = time.Now()
	if err := s.saveLocked(rt.session); err != nil {
		log.Printf("save completed assistant message: %v", err)
	}
	clients := cloneClients(rt.clients)
	s.mu.Unlock()

	broadcastJSON(clients, serverEvent{Type: "message_final", Message: &msg})
}

func (s *sessionStore) flushDraftMessage(sessionID string) {
	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if !ok || rt.session.DraftMessage == nil || strings.TrimSpace(rt.session.DraftMessage.Content) == "" {
		s.mu.Unlock()
		return
	}

	msg := *rt.session.DraftMessage
	rt.session.Messages = append(rt.session.Messages, msg)
	rt.session.DraftMessage = nil
	rt.session.UpdatedAt = time.Now()
	if err := s.saveLocked(rt.session); err != nil {
		log.Printf("save flushed draft: %v", err)
	}
	clients := cloneClients(rt.clients)
	s.mu.Unlock()

	broadcastJSON(clients, serverEvent{Type: "message_final", Message: &msg})
}

func (s *sessionStore) finishTaskOK(sessionID, taskID string) {
	s.flushDraftMessage(sessionID)

	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if ok && rt.session.ActiveTaskID == taskID {
		rt.session.ActiveTaskID = ""
		rt.session.ActiveTurnID = ""
		rt.session.UpdatedAt = time.Now()
		if err := s.saveLocked(rt.session); err != nil {
			log.Printf("save completed task: %v", err)
		}
	}
	clients := map[*clientConn]struct{}{}
	if ok {
		clients = cloneClients(rt.clients)
	}
	s.mu.Unlock()

	broadcastJSON(clients, serverEvent{Type: "task_status", TaskID: taskID, Running: false})
}

func (s *sessionStore) finishActiveTaskOK(sessionID string) {
	taskID := s.activeTaskID(sessionID)
	if taskID == "" {
		return
	}
	s.finishTaskOK(sessionID, taskID)
}

func (s *sessionStore) finishTaskWithError(sessionID, taskID string, err error) {
	s.flushDraftMessage(sessionID)
	s.appendMessage(sessionID, "system", "任务执行失败："+err.Error())

	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if ok && rt.session.ActiveTaskID == taskID {
		rt.session.ActiveTaskID = ""
		rt.session.ActiveTurnID = ""
		rt.session.UpdatedAt = time.Now()
		if saveErr := s.saveLocked(rt.session); saveErr != nil {
			log.Printf("save failed task: %v", saveErr)
		}
	}
	clients := map[*clientConn]struct{}{}
	if ok {
		clients = cloneClients(rt.clients)
	}
	s.mu.Unlock()

	broadcastJSON(clients, serverEvent{Type: "error", Error: err.Error()})
	broadcastJSON(clients, serverEvent{Type: "task_status", TaskID: taskID, Running: false})
}

func (s *sessionStore) finishActiveTaskWithError(sessionID string, err error) {
	taskID := s.activeTaskID(sessionID)
	if taskID == "" {
		return
	}
	s.finishTaskWithError(sessionID, taskID, err)
}

func (s *sessionStore) failAllActiveTasks(message string) {
	s.mu.RLock()
	sessionIDs := make([]string, 0, len(s.sessions))
	for sessionID, rt := range s.sessions {
		if rt.session.ActiveTaskID != "" {
			sessionIDs = append(sessionIDs, sessionID)
		}
	}
	s.mu.RUnlock()

	for _, sessionID := range sessionIDs {
		s.finishActiveTaskWithError(sessionID, errors.New(message))
	}
}

func (s *sessionStore) activeTaskID(sessionID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rt, ok := s.sessions[sessionID]
	if !ok {
		return ""
	}
	return rt.session.ActiveTaskID
}

func (s *sessionStore) appendMessage(sessionID, role, content string) {
	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return
	}

	msg := Message{
		ID:        uuid.NewString(),
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	}
	rt.session.Messages = append(rt.session.Messages, msg)
	rt.session.UpdatedAt = time.Now()
	if err := s.saveLocked(rt.session); err != nil {
		log.Printf("save appended message: %v", err)
	}
	clients := cloneClients(rt.clients)
	s.mu.Unlock()

	broadcastJSON(clients, serverEvent{Type: "message", Message: &msg})
}

func (s *sessionStore) appendEvent(sessionID, kind, title, body string) {
	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return
	}

	category, stepType, phase, target, count := eventFields(kind, title, body)

	logEntry := EventLog{
		ID:        uuid.NewString(),
		Kind:      kind,
		Category:  category,
		StepType:  stepType,
		Phase:     phase,
		Target:    target,
		Count:     count,
		Title:     title,
		Body:      body,
		CreatedAt: time.Now(),
	}
	rt.session.Events = append(rt.session.Events, logEntry)
	rt.session.UpdatedAt = time.Now()
	if err := s.saveLocked(rt.session); err != nil {
		log.Printf("save appended event: %v", err)
	}
	clients := cloneClients(rt.clients)
	s.mu.Unlock()

	broadcastJSON(clients, serverEvent{Type: "log", Log: &logEntry})
}

func (s *sessionStore) updateThreadID(sessionID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rt, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	if rt.session.CodexThreadID == threadID {
		return
	}
	rt.session.CodexThreadID = threadID
	rt.session.UpdatedAt = time.Now()
	if err := s.saveLocked(rt.session); err != nil {
		log.Printf("save thread id: %v", err)
	}
}

func (s *sessionStore) updateActiveTurn(sessionID, turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rt, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	if rt.session.ActiveTurnID == turnID {
		return
	}
	rt.session.ActiveTurnID = turnID
	rt.session.UpdatedAt = time.Now()
	if err := s.saveLocked(rt.session); err != nil {
		log.Printf("save active turn id: %v", err)
	}
}

func (s *sessionStore) deleteSession(sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, errors.New("missing session id")
	}

	s.mu.Lock()
	rt, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return false, nil
	}
	if rt.session.ActiveTaskID != "" {
		s.mu.Unlock()
		return false, errors.New("任务执行中，先用 /stop 终止")
	}
	delete(s.sessions, sessionID)
	s.mu.Unlock()

	path := filepath.Join(dataDir, sessionID+".json")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

func (s *sessionStore) broadcast(sessionID string, event serverEvent) {
	s.mu.RLock()
	rt, ok := s.sessions[sessionID]
	if !ok {
		s.mu.RUnlock()
		return
	}
	clients := cloneClients(rt.clients)
	s.mu.RUnlock()

	broadcastJSON(clients, event)
}

func (s *sessionStore) cloneSession(sessionID string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rt, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	cp := *rt.session
	cp.Messages = append([]Message(nil), rt.session.Messages...)
	cp.Events = append([]EventLog(nil), rt.session.Events...)
	if rt.session.DraftMessage != nil {
		draft := *rt.session.DraftMessage
		cp.DraftMessage = &draft
	}
	return &cp
}

func (s *sessionStore) listSessions() []sessionSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]sessionSummary, 0, len(s.sessions))
	for _, rt := range s.sessions {
		session := rt.session
		summary := sessionSummary{
			ID:           session.ID,
			Workdir:      normalizeWorkdir(session.Workdir),
			UpdatedAt:    session.UpdatedAt,
			MessageCount: len(session.Messages),
			Running:      session.ActiveTaskID != "",
		}
		if n := len(session.Messages); n > 0 {
			last := strings.TrimSpace(session.Messages[n-1].Content)
			summary.LastMessage = compactForSummary(last)
		}
		for i := len(session.Events) - 1; i >= 0; i-- {
			event := session.Events[i]
			if event.Category == "step" && strings.TrimSpace(event.Target) != "" {
				summary.LastEvent = compactForSummary(stepSummaryText(event))
				break
			}
		}
		items = append(items, summary)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items
}

func (s *sessionStore) findSessionByThread(threadID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for sessionID, rt := range s.sessions {
		if rt.session.CodexThreadID == threadID {
			return sessionID
		}
	}
	return ""
}

func (s *sessionStore) load() error {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dataDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var session Session
		if err := json.Unmarshal(raw, &session); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		session.ActiveTaskID = ""
		s.sessions[session.ID] = &sessionRuntime{
			session: &session,
			clients: make(map[*clientConn]struct{}),
		}
	}
	return nil
}

func (s *sessionStore) saveLocked(session *Session) error {
	path := filepath.Join(dataDir, session.ID+".json")
	raw, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func (s *sessionStore) saveMultipartImages(files []*multipart.FileHeader) ([]string, error) {
	imageIDs := make([]string, 0, len(files))
	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			return nil, err
		}
		filename, err := saveUploadedFile(file, header)
		file.Close()
		if err != nil {
			return nil, err
		}
		imageIDs = append(imageIDs, filename)
	}
	return imageIDs, nil
}
