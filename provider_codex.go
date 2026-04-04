package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type CodexProvider struct{}

type codexStreamPayload struct {
	Type string `json:"type"`
	Item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
	Payload struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Text    string `json:"text"`
		Message string `json:"message"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
	ThreadID string `json:"thread_id"`
}

func (p *CodexProvider) Name() string {
	return "codex"
}

func (p *CodexProvider) DefaultModel() string {
	return configuredDefaultModel(p.Name(), "gpt-5.4")
}

func (p *CodexProvider) ListSessions(ctx context.Context) ([]*SessionSummary, error) {
	files, err := p.findTranscriptFiles()
	if err != nil {
		return nil, err
	}

	items := make([]*SessionSummary, 0, len(files))
	for _, path := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		summary, err := p.readSessionSummary(path)
		if err != nil || summary == nil {
			continue
		}
		items = append(items, summary)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

func (p *CodexProvider) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is empty")
	}
	path, err := p.findTranscriptPath(sessionID)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return p.readSession(path, sessionID)
}

func (p *CodexProvider) DeleteSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is empty")
	}
	path, err := p.findTranscriptPath(sessionID)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (p *CodexProvider) Exec(ctx context.Context, session *Session, prompt string, imagePaths []string, onState func(*ProviderStateUpdate), onDelta func(string), onFinal func(string), onEvent func(*Event)) error {
	args := []string{"exec"}
	threadID := strings.TrimSpace(session.ProviderSessionID)
	if threadID != "" {
		args = append(args, "resume", threadID)
	}
	args = append(args, "--json")
	if model := strings.TrimSpace(session.Model); model != "" {
		args = append(args, "-m", model)
	}
	for _, imagePath := range imagePaths {
		if trimmed := strings.TrimSpace(imagePath); trimmed != "" {
			args = append(args, "-i", trimmed)
		}
	}
	if threadID == "" && session.Workdir != "" {
		args = append(args, "-C", session.Workdir)
	}
	args = append(args,
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
	)
	if text := strings.TrimSpace(prompt); text != "" {
		args = append(args, text)
	}

	cmd := exec.CommandContext(ctx, "codex", args...)
	if session.Workdir != "" {
		cmd.Dir = session.Workdir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("未找到 codex 可执行文件，请先确认 Codex CLI 已安装并已加入 PATH")
		}
		return err
	}

	var finalText string
	var streamErr string
	finalDelivered := false
	results := make(chan error, 2)

	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		current := ""
		lastEventKey := ""
		for scanner.Scan() {
			line := scanner.Text()
			text, errText, state := p.parseCodexStreamLine(line)
			if errText != "" {
				streamErr = errText
			}
			if state != nil {
				onState(state)
				if text := strings.TrimSpace(state.FinalText); text != "" && !finalDelivered {
					onFinal(text)
					finalText = text
					current = text
					finalDelivered = true
					continue
				}
			}
			if event := p.parseCodexEventLine(line); event != nil {
				key := event.Kind + "|" + event.StepType + "|" + event.Title + "|" + event.Target + "|" + event.Body
				if key != lastEventKey {
					onEvent(event)
					lastEventKey = key
				}
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			next, delta := mergeStreamText(current, text)
			if delta != "" {
				onDelta(delta)
			}
			current = next
			finalText = next
		}
		results <- scanner.Err()
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 16*1024), 256*1024)
		var lines []string
		for scanner.Scan() {
			if text := strings.TrimSpace(scanner.Text()); text != "" {
				lines = append(lines, text)
			}
		}
		if len(lines) > 0 && streamErr == "" {
			streamErr = strings.Join(lines, "\n")
		}
		results <- scanner.Err()
	}()

	if err := <-results; err != nil {
		return err
	}
	if err := <-results; err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		if streamErr != "" {
			return fmt.Errorf("codex 执行失败: %s", streamErr)
		}
		return err
	}
	if !finalDelivered && strings.TrimSpace(finalText) != "" {
		onFinal(strings.TrimSpace(finalText))
	}
	return nil
}

func (p *CodexProvider) parseCodexEventLine(line string) *Event {
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return nil
	}

	topType := strings.ToLower(strings.TrimSpace(anyString(payload["type"])))
	item, _ := payload["item"].(map[string]any)
	itemType := strings.ToLower(strings.TrimSpace(anyString(item["type"])))
	if itemType == "" {
		itemType = topType
	}
	if itemType == "" {
		return nil
	}
	if strings.HasPrefix(topType, "thread.") || strings.HasPrefix(topType, "turn.") {
		return nil
	}
	if itemType == "thread" || itemType == "turn" {
		return nil
	}
	if itemType == "agentmessage" || itemType == "agent_message" || itemType == "assistant_message" {
		return nil
	}

	title := p.humanizeStepLabel(itemType)
	body := compactEventBody(p.joinEventText(anyString(item["text"]), stringifyJSON(item["content"])))
	target := firstNonEmptyString(anyString(item["command"]), anyString(item["path"]), anyString(item["name"]))
	if body == "" && target == "" && (strings.Contains(itemType, "delta") || strings.Contains(itemType, "message")) {
		return nil
	}
	return &Event{
		ID:        newUUID(),
		Kind:      p.eventKindForStep(itemType),
		Category:  "step",
		StepType:  normalizeStepType(itemType),
		Phase:     p.eventPhaseForStep(itemType),
		Title:     title,
		Body:      body,
		Target:    target,
		CreatedAt: time.Now(),
	}
}

// humanizeStepLabel 把 Codex 事件类型转换成更适合前端展示的中文标题。
func (p *CodexProvider) humanizeStepLabel(stepType string) string {
	text := normalizeStepType(stepType)
	switch text {
	case "reasoning":
		return "思考中"
	case "thread", "turn":
		return text
	case "shellcommand", "shell_command", "exec_command_begin", "exec_command":
		return "执行命令"
	case "readfile", "read_file":
		return "读取文件"
	case "writefile", "write_file":
		return "写入文件"
	case "editfile", "patchfile", "apply_patch", "patch_file":
		return "修改文件"
	case "searchfiles", "findfiles", "glob", "grep", "search_text":
		return "检索内容"
	case "openurl", "fetchurl", "web_search":
		return "访问网页"
	case "tool_call", "tool_use", "function_call":
		return "调用工具"
	default:
		return strings.ReplaceAll(text, "_", " ")
	}
}

// eventKindForStep 根据 Codex 事件类型推断前端展示的事件种类。
func (p *CodexProvider) eventKindForStep(stepType string) string {
	text := normalizeStepType(stepType)
	if strings.Contains(text, "command") || strings.Contains(text, "tool") || strings.Contains(text, "call") {
		return "command"
	}
	return "status"
}

// eventPhaseForStep 根据 Codex 事件类型推断当前步骤所处阶段。
func (p *CodexProvider) eventPhaseForStep(stepType string) string {
	text := normalizeStepType(stepType)
	if strings.Contains(text, "end") || strings.Contains(text, "done") || strings.Contains(text, "complete") || strings.Contains(text, "finish") {
		return "completed"
	}
	return "started"
}

// joinEventText 合并 Codex 事件里的多段说明文本。
func (p *CodexProvider) joinEventText(values ...string) string {
	var parts []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (p *CodexProvider) parseCodexStreamLine(line string) (text string, errText string, state *ProviderStateUpdate) {
	var payload codexStreamPayload
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return "", "", nil
	}
	if threadID := strings.TrimSpace(payload.ThreadID); threadID != "" {
		state = &ProviderStateUpdate{
			ProviderSessionID: threadID,
		}
	}
	topType := strings.ToLower(strings.TrimSpace(payload.Type))
	if topType == "turn.completed" {
		if state == nil {
			state = &ProviderStateUpdate{}
		}
		state.IsComplete = true
	}
	if strings.EqualFold(strings.TrimSpace(payload.Type), "error") {
		if strings.TrimSpace(payload.Item.Text) != "" {
			return "", strings.TrimSpace(payload.Item.Text), state
		}
		return "", "codex exec failed", state
	}
	if topType == "response_item" &&
		strings.EqualFold(strings.TrimSpace(payload.Payload.Type), "message") &&
		strings.EqualFold(strings.TrimSpace(payload.Payload.Role), "assistant") {
		if text := strings.TrimSpace(payload.Payload.Text); text != "" {
			if state == nil {
				state = &ProviderStateUpdate{}
			}
			state.FinalText = text
			state.IsComplete = true
			return "", "", state
		}
		var parts []string
		for _, item := range payload.Payload.Content {
			if strings.EqualFold(strings.TrimSpace(item.Type), "output_text") || strings.EqualFold(strings.TrimSpace(item.Type), "text") {
				if text := strings.TrimSpace(item.Text); text != "" {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			if state == nil {
				state = &ProviderStateUpdate{}
			}
			state.FinalText = strings.Join(parts, "\n\n")
			state.IsComplete = true
			return "", "", state
		}
	}
	itemType := strings.ToLower(strings.TrimSpace(payload.Item.Type))
	if itemType == "agentmessage" || itemType == "agent_message" || itemType == "assistant_message" {
		if text := strings.TrimSpace(payload.Item.Text); text != "" {
			if topType == "item.completed" {
				if state == nil {
					state = &ProviderStateUpdate{}
				}
				state.FinalText = text
				state.IsComplete = true
				return "", "", state
			}
			return text, "", state
		}
		var parts []string
		for _, item := range payload.Item.Content {
			if strings.EqualFold(strings.TrimSpace(item.Type), "text") && strings.TrimSpace(item.Text) != "" {
				parts = append(parts, strings.TrimSpace(item.Text))
			}
		}
		if len(parts) > 0 {
			text := strings.Join(parts, "\n\n")
			if topType == "item.completed" {
				if state == nil {
					state = &ProviderStateUpdate{}
				}
				state.FinalText = text
				state.IsComplete = true
				return "", "", state
			}
			return text, "", state
		}
	}
	if strings.Contains(topType, "delta") {
		return strings.TrimSpace(payload.Item.Text), "", state
	}
	return "", "", state
}

type codexTranscriptEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexTranscriptMeta struct {
	ID    string `json:"id"`
	Cwd   string `json:"cwd"`
	Model string `json:"model"`
}

type codexTranscriptEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type codexTranscriptTurnContext struct {
	Model string `json:"model"`
	Cwd   string `json:"cwd"`
}

// findTranscriptFiles 扫描 Codex sessions 目录下的所有 transcript 文件。
func (p *CodexProvider) findTranscriptFiles() ([]string, error) {
	root := filepath.Join(p.homeDir(), "sessions")
	files := make([]string, 0, 64)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return files, nil
}

// readSessionSummary 从 transcript 文件读取一条会话摘要。
func (p *CodexProvider) readSessionSummary(path string) (*SessionSummary, error) {
	sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	session, err := p.readSession(path, sessionID)
	if err != nil || session == nil {
		return nil, err
	}
	return &SessionSummary{
		ID:        sessionID,
		Provider:  "codex",
		Model:     session.Model,
		Title:     p.firstMessageText(session.Messages),
		UpdatedAt: session.UpdatedAt,
	}, nil
}

func (p *CodexProvider) findTranscriptPath(sessionID string) (string, error) {
	root := filepath.Join(p.homeDir(), "sessions")
	var matched string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		if strings.HasSuffix(name, sessionID) {
			matched = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("codex session not found")
		}
		return "", err
	}
	if matched == "" {
		return "", errors.New("codex session not found")
	}
	return matched, nil
}

func (p *CodexProvider) homeDir() string {
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return filepath.Clean(value)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(os.Getenv("USERPROFILE"), ".codex")
	}
	return filepath.Join(home, ".codex")
}

func (p *CodexProvider) readSession(path, sessionID string) (*Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	session := &Session{
		ID:                sessionID,
		ProviderSessionID: sessionID,
		Provider:          "codex",
		Model:             "gpt-5",
		Messages:          make([]*Message, 0, 32),
		IsRunning:         false,
		UpdatedAt:         time.Time{},
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope codexTranscriptEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			continue
		}

		ts := parseStoredTime(envelope.Timestamp)
		if ts.After(session.UpdatedAt) {
			session.UpdatedAt = ts
		}

		switch strings.TrimSpace(envelope.Type) {
		case "session_meta":
			var meta codexTranscriptMeta
			if err := json.Unmarshal(envelope.Payload, &meta); err == nil {
				if cwd := normalizeWorkdir(meta.Cwd); cwd != "" {
					session.Workdir = cwd
				}
				if model := strings.TrimSpace(meta.Model); model != "" {
					session.Model = model
				}
			}
		case "turn_context":
			var meta codexTranscriptTurnContext
			if err := json.Unmarshal(envelope.Payload, &meta); err == nil {
				if cwd := normalizeWorkdir(meta.Cwd); cwd != "" {
					session.Workdir = cwd
				}
				if model := strings.TrimSpace(meta.Model); model != "" {
					session.Model = model
				}
			}
		case "event_msg":
			var event codexTranscriptEvent
			if err := json.Unmarshal(envelope.Payload, &event); err == nil {
				if msg := p.parseStoredMessage(sessionID, event, ts); msg != nil {
					session.Messages = append(session.Messages, msg)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if session.UpdatedAt.IsZero() {
		if info, err := os.Stat(path); err == nil {
			session.UpdatedAt = info.ModTime()
		} else {
			session.UpdatedAt = time.Now()
		}
	}
	return session, nil
}

// firstMessageText 返回第一条非空消息，优先用于列表标题展示。
func (p *CodexProvider) firstMessageText(messages []*Message) string {
	for _, msg := range messages {
		if msg != nil && strings.TrimSpace(msg.Content) != "" {
			return strings.TrimSpace(msg.Content)
		}
	}
	return ""
}

func (p *CodexProvider) parseStoredMessage(sessionID string, event codexTranscriptEvent, createdAt time.Time) *Message {
	text := strings.TrimSpace(event.Message)
	if text == "" {
		return nil
	}
	role := ""
	switch strings.TrimSpace(event.Type) {
	case "user_message":
		role = "user"
	case "agent_message":
		role = "assistant"
	default:
		return nil
	}
	return &Message{
		ID:        role + "-" + sessionID + "-" + createdAt.Format("20060102150405.000000000"),
		Role:      role,
		Content:   text,
		CreatedAt: createdAt,
	}
}
