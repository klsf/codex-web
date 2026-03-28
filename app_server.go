package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

func newAppServerClient(store *sessionStore, url string) *appServerClient {
	return &appServerClient{
		store:         store,
		url:           url,
		pending:       make(map[string]chan rpcPacket),
		threadSession: make(map[string]string),
		threadTurn:    make(map[string]string),
		loadedThreads: make(map[string]bool),
	}
}

func (c *appServerClient) InvalidateLoadedThreads() {
	c.mu.Lock()
	c.loadedThreads = make(map[string]bool)
	c.mu.Unlock()
}

func (c *appServerClient) Start() error {
	ctx, cancel := context.WithTimeout(context.Background(), appServerInitWait)
	defer cancel()
	return c.ensureConnected(ctx)
}

func (c *appServerClient) Close() {
	c.mu.Lock()
	conn := c.conn
	proc := c.proc
	c.conn = nil
	c.proc = nil
	c.initialized = false
	c.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	if proc != nil && proc.Process != nil {
		_ = proc.Process.Kill()
		_, _ = proc.Process.Wait()
	}
}

func (c *appServerClient) ensureConnected(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil && c.initialized {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	if err := c.connect(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || !c.initialized {
		return errors.New("codex app-server not initialized")
	}
	return nil
}

func (c *appServerClient) connect(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil && c.initialized {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
	if err != nil {
		if startErr := c.startProcess(); startErr != nil {
			return startErr
		}

		var dialErr error
		for {
			select {
			case <-ctx.Done():
				if dialErr != nil {
					return dialErr
				}
				return ctx.Err()
			default:
			}

			conn, _, dialErr = websocket.DefaultDialer.DialContext(ctx, c.url, nil)
			if dialErr == nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}

	c.mu.Lock()
	c.conn = conn
	c.initialized = false
	c.pending = make(map[string]chan rpcPacket)
	c.threadTurn = make(map[string]string)
	c.loadedThreads = make(map[string]bool)
	c.mu.Unlock()

	go c.readLoop(conn)

	initCtx, cancel := context.WithTimeout(ctx, appServerRPCTimeout)
	defer cancel()
	if _, err := c.sendRequestWithFallbackOnConn(initCtx, "initialize", map[string]interface{}{
		"clientInfo": map[string]interface{}{
			"name":    "codex-web",
			"title":   "Codex Web",
			"version": "0.1.0",
		},
		"capabilities": map[string]interface{}{
			"experimentalApi": true,
		},
	}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("initialize app-server: %w", err)
	}

	c.mu.Lock()
	if c.conn == conn {
		c.initialized = true
	}
	c.mu.Unlock()
	return nil
}

func (c *appServerClient) startProcess() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.proc != nil && c.proc.Process != nil {
		return nil
	}

	cmd := exec.Command("codex", "app-server", "--listen", c.url)
	cmd.Dir = defaultWorkdir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("app-server stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("app-server stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start app-server: %w", err)
	}

	c.proc = cmd
	go logPipe("[app-server] ", stdout)
	go logPipe("[app-server] ", stderr)
	go func(local *exec.Cmd) {
		err := local.Wait()
		c.mu.Lock()
		if c.proc == local {
			c.proc = nil
		}
		c.mu.Unlock()
		if err != nil {
			log.Printf("app-server exited: %v", err)
		}
	}(cmd)
	return nil
}

func logPipe(prefix string, r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			text := strings.TrimSpace(string(buf[:n]))
			if text != "" {
				for _, line := range strings.Split(text, "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						log.Printf("%s%s", prefix, line)
					}
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *appServerClient) readLoop(conn *websocket.Conn) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			c.handleDisconnect(conn, err)
			return
		}

		var packet rpcPacket
		if err := json.Unmarshal(raw, &packet); err != nil {
			log.Printf("decode app-server packet: %v", err)
			continue
		}

		if packet.Method != "" {
			c.handleNotification(packet.Method, packet.Params)
			continue
		}

		id := packetID(packet.ID)
		if id == "" {
			continue
		}

		c.mu.Lock()
		ch := c.pending[id]
		if ch != nil {
			delete(c.pending, id)
		}
		c.mu.Unlock()

		if ch != nil {
			ch <- packet
		}
	}
}

func (c *appServerClient) handleDisconnect(conn *websocket.Conn, err error) {
	c.mu.Lock()
	if c.conn != conn {
		c.mu.Unlock()
		return
	}

	pending := c.pending
	c.pending = make(map[string]chan rpcPacket)
	c.conn = nil
	c.initialized = false
	c.threadTurn = make(map[string]string)
	c.loadedThreads = make(map[string]bool)
	c.mu.Unlock()

	for id, ch := range pending {
		ch <- rpcPacket{
			ID: mustMarshalJSON(id),
			Error: &rpcError{
				Code:    -32000,
				Message: "codex app-server disconnected",
			},
		}
	}

	if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		log.Printf("app-server websocket closed: %v", err)
	}
	c.store.failAllActiveTasks("codex app-server disconnected")
}

func (c *appServerClient) handleNotification(method string, raw json.RawMessage) {
	var payload notificationEnvelope
	if err := json.Unmarshal(raw, &payload); err != nil {
		log.Printf("decode %s: %v", method, err)
		return
	}
	threadID := notificationThreadID(payload)

	switch method {
	case "thread/started":
		if threadID != "" {
			c.recordThreadSession("", threadID, true)
		}
	case "turn/started":
		if sessionID := c.sessionIDForThread(threadID); sessionID != "" {
			c.recordThreadTurn(threadID, payload.TurnID)
			c.store.updateActiveTurn(sessionID, payload.TurnID)
			c.store.appendEvent(sessionID, "status", "turn started", "")
		}
	case "item/started":
		c.handleItemStarted(payload)
	case "item/agentMessage/delta":
		if sessionID := c.sessionIDForThread(threadID); sessionID != "" && payload.Delta != "" {
			c.store.appendAssistantDelta(sessionID, payload.ItemID, payload.Delta)
		}
	case "item/completed":
		c.handleItemCompleted(payload)
	case "turn/completed":
		if sessionID := c.sessionIDForThread(threadID); sessionID != "" {
			c.clearThreadTurn(threadID)
			c.store.updateActiveTurn(sessionID, "")
			c.store.appendEvent(sessionID, "status", "turn completed", "")
			c.store.finishActiveTaskOK(sessionID)
		}
	case "turn/failed":
		if sessionID := c.sessionIDForThread(threadID); sessionID != "" {
			c.clearThreadTurn(threadID)
			c.store.updateActiveTurn(sessionID, "")
			message := strings.TrimSpace(payload.Message)
			if message == "" {
				message = "任务执行失败"
			}
			c.store.finishActiveTaskWithError(sessionID, errors.New(message))
		}
	case "error":
		if sessionID := c.sessionIDForThread(threadID); sessionID != "" {
			c.clearThreadTurn(threadID)
			c.store.updateActiveTurn(sessionID, "")
			message := extractAppServerErrorMessage(raw, payload)
			if message == "" {
				message = "codex app-server returned an error"
			}
			log.Printf("app-server error for session %s: %s raw=%s", sessionID, message, strings.TrimSpace(string(raw)))
			c.store.finishActiveTaskWithError(sessionID, errors.New(message))
		}
	}
}

func (c *appServerClient) handleItemStarted(payload notificationEnvelope) {
	sessionID := c.sessionIDForThread(notificationThreadID(payload))
	if sessionID == "" {
		return
	}
	itemType := normalizeItemType(stringField(payload.Item, "type"))
	switch itemType {
	case "commandexecution", "command_execution":
		c.store.appendEvent(sessionID, "command", "shell command started", strings.TrimSpace(stringField(payload.Item, "command")))
	default:
		if itemType != "" {
			c.store.appendEvent(sessionID, "status", "item started", itemType)
		}
	}
}

func (c *appServerClient) handleItemCompleted(payload notificationEnvelope) {
	sessionID := c.sessionIDForThread(notificationThreadID(payload))
	if sessionID == "" {
		return
	}

	itemType := normalizeItemType(stringField(payload.Item, "type"))
	switch itemType {
	case "agentmessage", "agent_message":
		text := stringField(payload.Item, "text")
		c.store.completeAssistantMessage(sessionID, stringField(payload.Item, "id"), text)
	case "commandexecution", "command_execution":
		title := "shell command completed"
		if exitCode, ok := intField(payload.Item, "exitCode"); ok && exitCode != 0 {
			title = fmt.Sprintf("shell command failed (exit %d)", exitCode)
		}
		body := strings.TrimSpace(stringField(payload.Item, "command"))
		output := strings.TrimSpace(firstNonEmpty(
			stringField(payload.Item, "aggregatedOutput"),
			stringField(payload.Item, "output"),
			stringField(payload.Item, "aggregated_output"),
		))
		if output != "" {
			if body != "" {
				body += "\n\n"
			}
			body += output
		}
		c.store.appendEvent(sessionID, "command", title, body)
	default:
		if itemType != "" {
			c.store.appendEvent(sessionID, "status", "item completed", itemType)
		}
	}
}

func notificationThreadID(payload notificationEnvelope) string {
	threadID := strings.TrimSpace(payload.ThreadID)
	if threadID != "" {
		return threadID
	}
	return strings.TrimSpace(stringField(payload.Thread, "id"))
}

func (c *appServerClient) sessionIDForThread(threadID string) string {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ""
	}

	c.mu.Lock()
	sessionID := c.threadSession[threadID]
	c.mu.Unlock()
	if sessionID != "" {
		return sessionID
	}

	sessionID = c.store.findSessionByThread(threadID)
	if sessionID != "" {
		c.recordThreadSession(sessionID, threadID, true)
	}
	return sessionID
}

func (c *appServerClient) StartTurn(ctx context.Context, sessionID, taskID, workdir, prompt string, imagePaths []string) error {
	if err := c.ensureConnected(ctx); err != nil {
		return err
	}

	threadID, err := c.ensureThread(ctx, sessionID, workdir)
	if err != nil {
		return err
	}
	c.recordThreadSession(sessionID, threadID, true)

	input := make([]map[string]interface{}, 0, len(imagePaths)+1)
	if strings.TrimSpace(prompt) != "" {
		input = append(input, map[string]interface{}{
			"type":          "text",
			"text":          strings.TrimSpace(prompt),
			"text_elements": []interface{}{},
		})
	}
	for _, path := range imagePaths {
		input = append(input, map[string]interface{}{
			"type": "localImage",
			"path": path,
		})
	}
	if len(input) == 0 {
		return errors.New("message is empty")
	}

	params := map[string]interface{}{
		"threadId": threadID,
		"input":    input,
	}
	if model := c.store.currentModel(); model != "" {
		params["model"] = model
	}
	_, err = c.sendRequestWithFallback(ctx, "turn/start", params)
	if err != nil {
		return err
	}

	c.store.appendEvent(sessionID, "status", "task submitted", fmt.Sprintf("task id: %s", taskID))
	return nil
}

func (c *appServerClient) ReadRateLimits(ctx context.Context) (*rateLimitsData, error) {
	result, err := c.sendRequest(ctx, "account/rateLimits/read", map[string]interface{}{}, true)
	if err != nil {
		return nil, err
	}

	var parsed rateLimitsResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, fmt.Errorf("parse account/rateLimits/read result: %w", err)
	}
	return &parsed.RateLimits, nil
}

func (c *appServerClient) ListModels(ctx context.Context) ([]modelInfo, error) {
	result, err := c.sendRequest(ctx, "model/list", map[string]interface{}{
		"cursor":        nil,
		"limit":         50,
		"includeHidden": false,
	}, true)
	if err != nil {
		return nil, err
	}
	var parsed modelListResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, fmt.Errorf("parse model/list result: %w", err)
	}
	return parsed.Data, nil
}

func (c *appServerClient) ReadServiceTier(ctx context.Context) (string, error) {
	result, err := c.sendRequest(ctx, "config/read", map[string]interface{}{
		"includeLayers": false,
	}, true)
	if err != nil {
		return "", err
	}
	var parsed configReadResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		return "", fmt.Errorf("parse config/read result: %w", err)
	}
	return strings.TrimSpace(parsed.Config.ServiceTier), nil
}

func (c *appServerClient) WriteConfigValue(ctx context.Context, keyPath, value string) error {
	_, err := c.sendRequest(ctx, "config/value/write", map[string]interface{}{
		"keyPath":       keyPath,
		"value":         value,
		"mergeStrategy": "upsert",
	}, true)
	return err
}

func (c *appServerClient) ClearConfigValue(ctx context.Context, keyPath string) error {
	_, err := c.sendRequest(ctx, "config/value/write", map[string]interface{}{
		"keyPath":       keyPath,
		"value":         nil,
		"mergeStrategy": "upsert",
	}, true)
	return err
}

func (c *appServerClient) ensureThread(ctx context.Context, sessionID, workdir string) (string, error) {
	session := c.store.cloneSession(sessionID)
	if session == nil {
		return "", errors.New("session not found")
	}
	workdir = normalizeWorkdir(workdir)

	if session.CodexThreadID == "" {
		params := map[string]interface{}{
			"cwd":                    workdir,
			"persistExtendedHistory": true,
		}
		if model := c.store.currentModel(); model != "" {
			params["model"] = model
		}
		result, err := c.sendRequestWithFallback(ctx, "thread/start", params)
		if err != nil {
			return "", err
		}

		var parsed threadStartResult
		if err := json.Unmarshal(result, &parsed); err != nil {
			return "", fmt.Errorf("parse thread/start result: %w", err)
		}
		threadID := strings.TrimSpace(parsed.Thread.ID)
		if threadID == "" {
			return "", errors.New("thread/start response missing thread id")
		}
		c.store.updateThreadID(sessionID, threadID)
		c.recordThreadSession(sessionID, threadID, true)
		return threadID, nil
	}

	c.recordThreadSession(sessionID, session.CodexThreadID, false)

	c.mu.Lock()
	loaded := c.loadedThreads[session.CodexThreadID]
	c.mu.Unlock()
	if loaded {
		return session.CodexThreadID, nil
	}

	params := map[string]interface{}{
		"threadId":               session.CodexThreadID,
		"cwd":                    workdir,
		"persistExtendedHistory": true,
	}
	if model := c.store.currentModel(); model != "" {
		params["model"] = model
	}
	if _, err := c.sendRequestWithFallback(ctx, "thread/resume", params); err != nil {
		return "", err
	}

	c.recordThreadSession(sessionID, session.CodexThreadID, true)
	return session.CodexThreadID, nil
}

func (c *appServerClient) recordThreadSession(sessionID, threadID string, loaded bool) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(sessionID) != "" {
		c.threadSession[threadID] = sessionID
	}
	if loaded {
		c.loadedThreads[threadID] = true
	}
}

func (c *appServerClient) recordThreadTurn(threadID, turnID string) {
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	if threadID == "" || turnID == "" {
		return
	}
	c.mu.Lock()
	c.threadTurn[threadID] = turnID
	c.mu.Unlock()
}

func (c *appServerClient) clearThreadTurn(threadID string) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	c.mu.Lock()
	delete(c.threadTurn, threadID)
	c.mu.Unlock()
}

func (c *appServerClient) InterruptTurn(ctx context.Context, sessionID string) error {
	session := c.store.cloneSession(sessionID)
	if session == nil {
		return errors.New("session not found")
	}
	threadID := strings.TrimSpace(session.CodexThreadID)
	turnID := strings.TrimSpace(session.ActiveTurnID)
	if threadID == "" || turnID == "" {
		return errors.New("no active turn to stop")
	}
	_, err := c.sendRequestWithFallback(ctx, "turn/interrupt", map[string]interface{}{
		"threadId":       threadID,
		"expectedTurnId": turnID,
	})
	return err
}

func (c *appServerClient) CompactThread(ctx context.Context, threadID string) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return errors.New("no thread to compact")
	}
	_, err := c.sendRequestWithFallback(ctx, "thread/compact/start", map[string]interface{}{
		"threadId": threadID,
	})
	return err
}

func (c *appServerClient) sendRequestWithFallback(ctx context.Context, method string, baseParams map[string]interface{}) (json.RawMessage, error) {
	return c.sendRequestWithFallbackInternal(ctx, method, baseParams, true)
}

func (c *appServerClient) sendRequestWithFallbackOnConn(ctx context.Context, method string, baseParams map[string]interface{}) (json.RawMessage, error) {
	return c.sendRequestWithFallbackInternal(ctx, method, baseParams, false)
}

func (c *appServerClient) sendRequestWithFallbackInternal(ctx context.Context, method string, baseParams map[string]interface{}, requireInit bool) (json.RawMessage, error) {
	attempts := []map[string]interface{}{
		mergeMaps(baseParams, map[string]interface{}{
			"approvalPolicy": "never",
			"sandboxPolicy": map[string]interface{}{
				"type": "dangerFullAccess",
			},
		}),
		mergeMaps(baseParams, map[string]interface{}{
			"approvalPolicy": "never",
			"sandbox":        "danger-full-access",
		}),
		mergeMaps(baseParams, map[string]interface{}{
			"approvalPolicy": "never",
		}),
		baseParams,
	}

	var lastErr error
	for _, params := range attempts {
		result, err := c.sendRequest(ctx, method, params, requireInit)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !shouldRetryFallback(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *appServerClient) sendRequest(ctx context.Context, method string, params map[string]interface{}, requireInit bool) (json.RawMessage, error) {
	if requireInit {
		if err := c.ensureConnected(ctx); err != nil {
			return nil, err
		}
	} else {
		c.mu.Lock()
		hasConn := c.conn != nil
		c.mu.Unlock()
		if !hasConn {
			return nil, errors.New("codex app-server websocket is not connected")
		}
	}

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil, errors.New("codex app-server websocket is not connected")
	}

	id := uuid.NewString()
	ch := make(chan rpcPacket, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	packet := map[string]interface{}{
		"id":     id,
		"method": method,
		"params": params,
	}

	c.writeMu.Lock()
	err := conn.WriteJSON(packet)
	c.writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", method, err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, rpcCallError(method, resp.Error)
		}
		return resp.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s timed out: %w", method, ctx.Err())
	}
}
