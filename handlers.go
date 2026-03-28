package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func (s *sessionStore) handleNewSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req newSessionRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
	}
	workdir, err := validateWorkdir(req.Workdir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	session := s.ensureSessionWithWorkdir("", workdir)
	writeJSON(w, map[string]string{"sessionId": session.ID})
}

func (s *sessionStore) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if authTokenForPassword(req.Password) != s.authToken {
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    s.authToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 30,
	})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *sessionStore) handleAuth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"authenticated": s.isAuthenticated(r)})
}

func (s *sessionStore) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *sessionStore) handleAppConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte("window.__APP_CONFIG = " + mustJSObject(map[string]interface{}{
		"version":        strings.TrimSpace(appVersion),
		"authGuideSteps": authGuideSteps,
	}) + ";\n"))
}

func (s *sessionStore) handleCodexAuthPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(codexAuthPageHTML(
		codexAuthReturnURL(r.URL.Query().Get("return")),
		codexAuthForce(r.URL.Query().Get("force")),
	)))
}

func (s *sessionStore) handleCodexAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.auth.Status(codexAuthForce(r.URL.Query().Get("force"))))
}

func (s *sessionStore) handleCodexAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	writeJSON(w, s.auth.EnsureStarted(ctx, codexAuthForce(r.URL.Query().Get("force")), codexAuthRestart(r.URL.Query().Get("restart"))))
}

func (s *sessionStore) handleCodexAuthComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req codexAuthCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	s.auth.mu.Lock()
	current := s.auth.session
	s.auth.mu.Unlock()
	if current == nil || strings.TrimSpace(req.SessionID) == "" || req.SessionID != current.ID {
		log.Printf("auth complete rejected: session mismatch current=%v incoming=%q", current != nil, strings.TrimSpace(req.SessionID))
		writeJSONStatus(w, http.StatusBadRequest, codexAuthStatusResponse{
			LoggedIn: false,
			Session: &codexAuthSession{
				ID:     strings.TrimSpace(req.SessionID),
				Status: "failed",
				Error:  authStatusMessage("state mismatch"),
			},
		})
		return
	}
	if parsed, err := url.Parse(strings.TrimSpace(req.CallbackURL)); err != nil || strings.TrimSpace(parsed.Query().Get("state")) == "" || strings.TrimSpace(parsed.Query().Get("state")) != current.State {
		log.Printf("auth complete rejected: state mismatch session=%s", current.ID)
		writeJSONStatus(w, http.StatusBadRequest, codexAuthStatusResponse{
			LoggedIn: false,
			Session: &codexAuthSession{
				ID:     current.ID,
				Status: "failed",
				Error:  authStatusMessage("state mismatch"),
			},
		})
		return
	}
	if err := completeCodexAuth(ctx, req.CallbackURL); err != nil {
		log.Printf("auth complete failed: session=%s error=%v", current.ID, err)
		s.auth.mu.Lock()
		if s.auth.session != nil {
			s.auth.session.Status = "failed"
			s.auth.session.Error = authStatusMessage(err.Error())
		}
		s.auth.mu.Unlock()
		writeJSONStatus(w, http.StatusBadRequest, s.auth.Status(true))
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if loggedIn, _ := codexLoginStatus(); loggedIn {
			log.Printf("auth complete confirmed: session=%s", current.ID)
			writeJSON(w, s.auth.Status(false))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Printf("auth complete pending after callback: session=%s", current.ID)
	writeJSON(w, s.auth.Status(true))
}

func (s *sessionStore) handleCodexAuthCallback(w http.ResponseWriter, r *http.Request) {
	target, err := url.Parse(codexAuthProxyTarget())
	if err != nil {
		http.Error(w, "invalid callback target", http.StatusInternalServerError)
		return
	}
	proxyURL := *target
	proxyURL.Path = "/auth/callback"
	proxyURL.RawQuery = r.URL.RawQuery

	req, err := http.NewRequestWithContext(r.Context(), r.Method, proxyURL.String(), r.Body)
	if err != nil {
		http.Error(w, "build callback request failed", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	req.Host = target.Host

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "codex login callback is not ready", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *sessionStore) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/login" || path == "/api/auth" || path == "/api/logout" || path == "/" || path == "/index.html" || path == "/app.js" || path == "/style.css" || path == "/app-config.js" {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(path, "/api/") && path != "/ws" && !strings.HasPrefix(path, "/uploads/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.isAuthenticated(r) {
			if path == "/ws" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *sessionStore) isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return false
	}
	return cookie.Value != "" && cookie.Value == s.authToken
}

func (s *sessionStore) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var hello clientEvent
	if err := conn.ReadJSON(&hello); err != nil {
		return
	}
	if hello.Type != "hello" {
		_ = conn.WriteJSON(serverEvent{Type: "error", Error: "first message must be hello"})
		return
	}

	rt := s.attachClient(hello.SessionID, conn)
	defer s.detachClient(rt.session.ID, conn)

	if err := conn.WriteJSON(serverEvent{
		Type:    "snapshot",
		Session: s.cloneSession(rt.session.ID),
		Running: rt.session.ActiveTaskID != "",
		TaskID:  rt.session.ActiveTaskID,
		Meta:    &s.meta,
	}); err != nil {
		return
	}

	for {
		var event clientEvent
		if err := conn.ReadJSON(&event); err != nil {
			break
		}

		switch event.Type {
		case "send":
			if err := s.enqueuePrompt(rt.session.ID, strings.TrimSpace(event.Content), event.ImageIDs); err != nil {
				_ = conn.WriteJSON(serverEvent{Type: "error", Error: err.Error()})
			}
		case "ping":
			if err := conn.WriteJSON(serverEvent{Type: "pong"}); err != nil {
				return
			}
		default:
			_ = conn.WriteJSON(serverEvent{Type: "error", Error: "unsupported event"})
		}
	}
}

func (s *sessionStore) attachClient(sessionID string, conn *websocket.Conn) *sessionRuntime {
	rt := s.ensureRuntime(sessionID, "")
	client := &clientConn{conn: conn}

	s.mu.Lock()
	rt.clients[client] = struct{}{}
	s.mu.Unlock()

	return rt
}

func (s *sessionStore) detachClient(sessionID string, conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rt, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	for client := range rt.clients {
		if client.conn == conn {
			delete(rt.clients, client)
			return
		}
	}
}

func (s *sessionStore) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(r.FormValue("sessionId"))
	content := strings.TrimSpace(r.FormValue("content"))
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	imageIDs, err := s.saveMultipartImages(r.MultipartForm.File["images"])
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.enqueuePrompt(sessionID, content, imageIDs); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]bool{"ok": true})
}

func (s *sessionStore) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	resp := statusResponse{
		Model:          s.currentModel(),
		Cwd:            defaultWorkdir,
		SessionID:      sessionID,
		Transport:      "connected",
		Task:           "idle",
		ApprovalPolicy: s.currentApprovalPolicy(),
		ServiceTier:    s.currentServiceTier(),
		FastMode:       s.currentFastMode(),
	}

	if sessionID != "" {
		session := s.cloneSession(sessionID)
		if session != nil {
			resp.SessionID = session.ID
			resp.Cwd = normalizeWorkdir(session.Workdir)
			if session.ActiveTaskID != "" {
				resp.Task = "running"
			}
		}
	}

	if s.app != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		limits, err := s.app.ReadRateLimits(ctx)
		if err == nil {
			resp.RateLimits = limits
		}
	}

	writeJSON(w, resp)
}

func (s *sessionStore) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := modelsResponse{Current: s.currentModel(), Items: []modelInfo{}}
	if s.app != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		items, err := s.app.ListModels(ctx)
		if err == nil {
			resp.Items = items
		}
	}
	writeJSON(w, resp)
}

func (s *sessionStore) handleSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := listInstalledSkills()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, skillsResponse{Items: items})
}

func (s *sessionStore) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, sessionsResponse{Items: s.listSessions()})
}

func (s *sessionStore) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req commandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	req.SessionID = strings.TrimSpace(req.SessionID)
	req.Command = strings.TrimSpace(req.Command)
	req.Args = strings.TrimSpace(req.Args)
	if req.SessionID == "" || req.Command == "" {
		http.Error(w, "missing sessionId or command", http.StatusBadRequest)
		return
	}

	switch req.Command {
	case "/review":
		if err := s.enqueueReview(req.SessionID, req.Args); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "/model":
		model, err := s.setModel(req.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "model": model})
	case "/approvals":
		mode, err := s.setApprovalPolicy(req.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "approvalPolicy": mode})
	case "/fast":
		mode, serviceTier, err := s.setFastMode(req.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "fastMode": mode, "serviceTier": serviceTier})
	case "/compact":
		compacted, err := s.compactSession(req.SessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "compacted": compacted})
	case "/stop":
		stopped, err := s.stopActiveTask(req.SessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "stopped": stopped})
	case "/delete":
		targetID := req.SessionID
		if req.Args != "" {
			targetID = req.Args
		}
		deleted, err := s.deleteSession(targetID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "deleted": deleted, "sessionId": targetID})
	default:
		http.Error(w, "unsupported command", http.StatusBadRequest)
	}
}
