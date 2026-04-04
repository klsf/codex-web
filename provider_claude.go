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

type ClaudeProvider struct{}

type claudeStreamPayload struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Error     string `json:"error"`
	Message   struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func (p *ClaudeProvider) Name() string {
	return "claude"
}

func (p *ClaudeProvider) DefaultModel() string {
	return configuredDefaultModel(p.Name(), "opus")
}

func (p *ClaudeProvider) ListSessions(ctx context.Context) ([]*SessionSummary, error) {
	files, err := p.findSessionFiles()
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

func (p *ClaudeProvider) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	path, err := p.findSessionFileByID(sessionID)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return p.readSession(path)
}

func (p *ClaudeProvider) DeleteSession(ctx context.Context, sessionID string) error {
	path, err := p.findSessionFileByID(sessionID)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return os.Remove(path)
}

func (p *ClaudeProvider) Exec(ctx context.Context, session *Session, prompt string, imagePaths []string, onState func(*ProviderStateUpdate), onDelta func(string), onFinal func(string), onEvent func(*Event)) error {
	if len(imagePaths) > 0 {
		var builder strings.Builder
		builder.WriteString(strings.TrimSpace(prompt))
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString("Use these local image files as context:\n")
		for _, path := range imagePaths {
			if trimmed := strings.TrimSpace(path); trimmed != "" {
				builder.WriteString("- ")
				builder.WriteString(trimmed)
				builder.WriteString("\n")
			}
		}
		prompt = strings.TrimSpace(builder.String())
	}
	args := []string{
		"-p", strings.TrimSpace(prompt),
		"--verbose",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
		"--model", func(value, fallback string) string {
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
			return fallback
		}(session.Model, p.DefaultModel()),
	}
	if providerSessionID := strings.TrimSpace(session.ProviderSessionID); providerSessionID != "" {
		args = append(args, "--resume", providerSessionID)
	} else if sessionID := strings.TrimSpace(session.ID); sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	if session.Workdir != "" {
		args = append(args, "--add-dir", session.Workdir)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
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
			return errors.New("未找到 claude 可执行文件，请先确认 Claude CLI 已安装并已加入 PATH")
		}
		return err
	}

	var finalText string
	var streamErr string
	results := make(chan error, 2)

	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		current := ""
		lastEventKey := ""
		for scanner.Scan() {
			line := scanner.Text()
			text, errText, providerSessionID := p.parseClaudeStreamLine(line)
			if errText != "" {
				streamErr = errText
			}
			if providerSessionID != "" {
				onState(&ProviderStateUpdate{ProviderSessionID: providerSessionID})
			}
			if event := p.parseClaudeEventLine(line); event != nil {
				key := event.Kind + "|" + event.StepType + "|" + event.Title + "|" + event.Target + "|" + event.Body
				if key != lastEventKey {
					onEvent(event)
					lastEventKey = key
				}
			}
			if text == "" {
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
			return fmt.Errorf("claude 执行失败: %s", streamErr)
		}
		return err
	}
	if streamErr != "" {
		return errors.New(streamErr)
	}
	if strings.TrimSpace(finalText) != "" {
		onFinal(strings.TrimSpace(finalText))
	}
	return nil
}

func (p *ClaudeProvider) parseClaudeEventLine(line string) *Event {
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return nil
	}

	message, _ := payload["message"].(map[string]any)
	content, _ := message["content"].([]any)
	for _, item := range content {
		block, _ := item.(map[string]any)
		if strings.ToLower(strings.TrimSpace(anyString(block["type"]))) != "tool_use" {
			continue
		}
		name := anyString(block["name"])
		body := stringifyJSON(block["input"])
		return &Event{
			ID:        newUUID(),
			Kind:      "command",
			Category:  "command",
			StepType:  normalizeStepType(name),
			Phase:     "started",
			Title:     "调用工具 " + firstNonEmptyString(name, "tool"),
			Body:      body,
			Target:    name,
			CreatedAt: time.Now(),
		}
	}

	payloadType := strings.ToLower(strings.TrimSpace(anyString(payload["type"])))
	if payloadType == "result" {
		return &Event{
			ID:        newUUID(),
			Kind:      "status",
			Category:  "step",
			StepType:  "result",
			Phase:     "completed",
			Title:     "本轮输出完成",
			Body:      compactEventBody(anyString(payload["subtype"])),
			CreatedAt: time.Now(),
		}
	}
	return nil
}

func (p *ClaudeProvider) parseClaudeStreamLine(line string) (text string, errText string, providerSessionID string) {
	var payload claudeStreamPayload
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return "", "", ""
	}
	providerSessionID = strings.TrimSpace(payload.SessionID)
	if payload.Error != "" {
		errText = strings.TrimSpace(payload.Error)
	}
	if payload.IsError && errText == "" {
		errText = strings.TrimSpace(payload.Result)
	}
	var chunks []string
	for _, item := range payload.Message.Content {
		if strings.EqualFold(item.Type, "text") && strings.TrimSpace(item.Text) != "" {
			chunks = append(chunks, item.Text)
		}
	}
	if len(chunks) > 0 {
		return strings.Join(chunks, "\n\n"), errText, providerSessionID
	}
	if strings.EqualFold(payload.Type, "result") && strings.TrimSpace(payload.Result) != "" {
		return strings.TrimSpace(payload.Result), errText, providerSessionID
	}
	return "", errText, providerSessionID
}

func (p *ClaudeProvider) findSessionFiles() ([]string, error) {
	root, err := p.projectsDir()
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, 32)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
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

func (p *ClaudeProvider) projectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

func (p *ClaudeProvider) findSessionFileByID(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id is empty")
	}
	files, err := p.findSessionFiles()
	if err != nil {
		return "", err
	}
	for _, path := range files {
		if strings.EqualFold(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), sessionID) {
			return path, nil
		}
	}
	return "", errors.New("claude session not found")
}

func (p *ClaudeProvider) readSessionSummary(path string) (*SessionSummary, error) {
	session, err := p.readSession(path)
	if err != nil || session == nil {
		return nil, err
	}
	return &SessionSummary{
		ID:        session.ID,
		Provider:  session.Provider,
		Model:     session.Model,
		Title:     p.firstMessageText(session.Messages),
		UpdatedAt: session.UpdatedAt,
	}, nil
}

func (p *ClaudeProvider) readSession(path string) (*Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	session := &Session{
		ID:                sessionID,
		ProviderSessionID: sessionID,
		Provider:          "claude",
		Model:             "sonnet",
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
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		ts := parseStoredTime(anyString(entry["timestamp"]))
		if ts.After(session.UpdatedAt) {
			session.UpdatedAt = ts
		}
		if cwd := normalizeWorkdir(anyString(entry["cwd"])); cwd != "" {
			session.Workdir = cwd
		}

		switch strings.TrimSpace(anyString(entry["type"])) {
		case "user":
			if msg := p.parseStoredUser(entry, ts); msg != nil {
				session.Messages = append(session.Messages, msg)
			}
		case "assistant":
			msg, model := p.parseStoredAssistant(entry, ts)
			if model != "" {
				session.Model = model
			}
			if msg != nil {
				session.Messages = append(session.Messages, msg)
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
	if session.Workdir == "" {
		session.Workdir = p.decodeClaudeProjectDir(filepath.Base(filepath.Dir(path)))
	}
	return session, nil
}

func (p *ClaudeProvider) parseStoredUser(entry map[string]any, createdAt time.Time) *Message {
	message, ok := entry["message"].(map[string]any)
	if !ok {
		return nil
	}
	text := strings.TrimSpace(p.extractContent(message["content"]))
	if text == "" {
		return nil
	}
	return &Message{
		ID:        firstNonEmptyString(anyString(entry["uuid"]), anyString(entry["sessionId"])+"-"+createdAt.Format(time.RFC3339Nano)),
		Role:      "user",
		Content:   text,
		CreatedAt: createdAt,
	}
}

func (p *ClaudeProvider) parseStoredAssistant(entry map[string]any, createdAt time.Time) (*Message, string) {
	message, ok := entry["message"].(map[string]any)
	if !ok {
		return nil, ""
	}
	text := strings.TrimSpace(p.extractContent(message["content"]))
	if text == "" {
		return nil, anyString(message["model"])
	}
	return &Message{
		ID:        firstNonEmptyString(anyString(entry["uuid"]), anyString(entry["sessionId"])+"-"+createdAt.Format(time.RFC3339Nano)),
		Role:      "assistant",
		Content:   text,
		CreatedAt: createdAt,
	}, anyString(message["model"])
}

func (p *ClaudeProvider) extractContent(value any) string {
	switch node := value.(type) {
	case string:
		return strings.TrimSpace(node)
	case []any:
		parts := make([]string, 0, len(node))
		for _, item := range node {
			if block, ok := item.(map[string]any); ok && strings.EqualFold(anyString(block["type"]), "text") {
				if text := strings.TrimSpace(anyString(block["text"])); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n\n")
	default:
		return ""
	}
}

func (p *ClaudeProvider) decodeClaudeProjectDir(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	decoded := strings.ReplaceAll(name, "--", `\`)
	if len(decoded) >= 2 && decoded[1] == '-' {
		decoded = decoded[:1] + ":" + decoded[2:]
	}
	return decoded
}

func (p *ClaudeProvider) firstMessageText(messages []*Message) string {
	for _, msg := range messages {
		if msg != nil && strings.TrimSpace(msg.Content) != "" {
			return strings.TrimSpace(msg.Content)
		}
	}
	return ""
}
