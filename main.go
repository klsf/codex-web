package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	addr                = "0.0.0.0:991"
	dataDir             = "data/sessions"
	uploadsDir          = "data/uploads"
	defaultWorkdir      = "/www/codex"
	appServerURL        = "ws://127.0.0.1:8765"
	appServerInitWait   = 15 * time.Second
	appServerRPCTimeout = 30 * time.Second
	authCookieName      = "codex_web_auth"
)

//go:embed static
var embeddedStatic embed.FS

type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	ImageURLs []string  `json:"imageUrls,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type EventLog struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type Session struct {
	ID            string     `json:"id"`
	Workdir       string     `json:"workdir,omitempty"`
	CodexThreadID string     `json:"codexThreadId,omitempty"`
	ActiveTurnID  string     `json:"activeTurnId,omitempty"`
	Messages      []Message  `json:"messages"`
	Events        []EventLog `json:"events,omitempty"`
	DraftMessage  *Message   `json:"draftMessage,omitempty"`
	ActiveTaskID  string     `json:"activeTaskId,omitempty"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type sessionRuntime struct {
	session *Session
	clients map[*clientConn]struct{}
}

type clientConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

type sessionStore struct {
	mu            sync.RWMutex
	sessions      map[string]*sessionRuntime
	meta          appMeta
	app           *appServerClient
	maxConcurrent int
	taskSlots     chan struct{}
	authToken     string
}

type clientEvent struct {
	Type      string   `json:"type"`
	SessionID string   `json:"sessionId,omitempty"`
	Content   string   `json:"content,omitempty"`
	ImageIDs  []string `json:"imageIds,omitempty"`
}

type commandRequest struct {
	SessionID string `json:"sessionId"`
	Command   string `json:"command"`
	Args      string `json:"args,omitempty"`
}

type loginRequest struct {
	Password string `json:"password"`
}

type newSessionRequest struct {
	Workdir string `json:"workdir"`
}

type statusResponse struct {
	Model          string          `json:"model"`
	Cwd            string          `json:"cwd"`
	SessionID      string          `json:"sessionId"`
	Transport      string          `json:"transport"`
	Task           string          `json:"task"`
	ApprovalPolicy string          `json:"approvalPolicy"`
	ServiceTier    string          `json:"serviceTier,omitempty"`
	FastMode       bool            `json:"fastMode"`
	RateLimits     *rateLimitsData `json:"rateLimits,omitempty"`
}

type modelsResponse struct {
	Current string      `json:"current"`
	Items   []modelInfo `json:"items"`
}

type skillsResponse struct {
	Items []skillInfo `json:"items"`
}

type sessionsResponse struct {
	Items []sessionSummary `json:"items"`
}

type sessionSummary struct {
	ID           string    `json:"id"`
	Workdir      string    `json:"workdir,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
	LastMessage  string    `json:"lastMessage,omitempty"`
	MessageCount int       `json:"messageCount"`
}

type serverEvent struct {
	Type    string      `json:"type"`
	Session *Session    `json:"session,omitempty"`
	Message *Message    `json:"message,omitempty"`
	Log     *EventLog   `json:"log,omitempty"`
	TaskID  string      `json:"taskId,omitempty"`
	Running bool        `json:"running,omitempty"`
	Error   string      `json:"error,omitempty"`
	Payload interface{} `json:"payload,omitempty"`
	Meta    *appMeta    `json:"meta,omitempty"`
}

type appMeta struct {
	Model          string `json:"model"`
	Cwd            string `json:"cwd"`
	ApprovalPolicy string `json:"approvalPolicy"`
	ServiceTier    string `json:"serviceTier,omitempty"`
	FastMode       bool   `json:"fastMode"`
}

type rpcPacket struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type appServerClient struct {
	store *sessionStore
	url   string

	mu            sync.Mutex
	writeMu       sync.Mutex
	conn          *websocket.Conn
	proc          *exec.Cmd
	initialized   bool
	pending       map[string]chan rpcPacket
	threadSession map[string]string
	threadTurn    map[string]string
	loadedThreads map[string]bool
}

type threadStartResult struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type notificationEnvelope struct {
	ThreadID string                 `json:"threadId"`
	TurnID   string                 `json:"turnId"`
	ItemID   string                 `json:"itemId"`
	Delta    string                 `json:"delta"`
	Message  string                 `json:"message"`
	Thread   map[string]interface{} `json:"thread"`
	Item     map[string]interface{} `json:"item"`
	Turn     map[string]interface{} `json:"turn"`
}

type rateLimitsResult struct {
	RateLimits rateLimitsData `json:"rateLimits"`
}

type rateLimitsData struct {
	LimitID   string            `json:"limitId"`
	LimitName *string           `json:"limitName"`
	Primary   *rateWindow       `json:"primary"`
	Secondary *rateWindow       `json:"secondary"`
	Credits   *rateCredits      `json:"credits"`
	PlanType  string            `json:"planType"`
	Extra     map[string]string `json:"-"`
}

type rateWindow struct {
	UsedPercent        int   `json:"usedPercent"`
	WindowDurationMins int   `json:"windowDurationMins"`
	ResetsAt           int64 `json:"resetsAt"`
}

type rateCredits struct {
	HasCredits bool     `json:"hasCredits"`
	Unlimited  bool     `json:"unlimited"`
	Balance    *float64 `json:"balance"`
}

type modelListResult struct {
	Data []modelInfo `json:"data"`
}

type configReadResult struct {
	Config configSnapshot `json:"config"`
}

type configSnapshot struct {
	ServiceTier string `json:"service_tier"`
}

type modelInfo struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	IsDefault   bool   `json:"isDefault"`
}

type skillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	passwordFlag := flag.String("password", "codex", "login password for codex-web")
	flag.Parse()

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		log.Fatalf("create upload dir: %v", err)
	}

	store := &sessionStore{
		sessions: make(map[string]*sessionRuntime),
		meta: appMeta{
			Model:          detectCodexModel(),
			Cwd:            defaultWorkdir,
			ApprovalPolicy: "never",
			ServiceTier:    detectServiceTier(),
		},
		authToken: authTokenForPassword(*passwordFlag),
	}
	store.meta.FastMode = strings.EqualFold(store.meta.ServiceTier, "fast")
	store.maxConcurrent = detectTaskConcurrency()
	store.taskSlots = make(chan struct{}, store.maxConcurrent)
	if err := store.load(); err != nil {
		log.Fatalf("load sessions: %v", err)
	}

	app := newAppServerClient(store, appServerURL)
	store.app = app
	if err := app.Start(); err != nil {
		log.Fatalf("start codex app-server: %v", err)
	}
	if tier, err := app.ReadServiceTier(context.Background()); err == nil {
		store.mu.Lock()
		store.meta.ServiceTier = strings.TrimSpace(tier)
		store.meta.FastMode = strings.EqualFold(strings.TrimSpace(tier), "fast")
		store.mu.Unlock()
	}
	defer app.Close()

	staticFS, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		log.Fatalf("load embedded static files: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(uploadsDir))))
	mux.HandleFunc("/ws", store.handleWS)
	mux.HandleFunc("/api/login", store.handleLogin)
	mux.HandleFunc("/api/auth", store.handleAuth)
	mux.HandleFunc("/api/logout", store.handleLogout)
	mux.HandleFunc("/api/session/new", store.handleNewSession)
	mux.HandleFunc("/api/send", store.handleSend)
	mux.HandleFunc("/api/command", store.handleCommand)
	mux.HandleFunc("/api/status", store.handleStatus)
	mux.HandleFunc("/api/models", store.handleModels)
	mux.HandleFunc("/api/skills", store.handleSkills)
	mux.HandleFunc("/api/sessions", store.handleSessions)

	log.Printf("listening on %s", addr)
	log.Printf("codex task concurrency limit: %d", store.maxConcurrent)
	if err := http.ListenAndServe(addr, store.withAuth(mux)); err != nil {
		log.Fatal(err)
	}
}

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

func (s *sessionStore) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/login" || path == "/api/auth" || path == "/api/logout" || path == "/" || path == "/index.html" || path == "/app.js" || path == "/style.css" {
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
	case "/init":
		path, created, err := s.initAgentsFile(req.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "path": path, "created": created})
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
	if err := s.app.WriteConfigValue(ctx, "service_tier", value); err != nil {
		return err
	}
	return nil
}

func (s *sessionStore) clearServiceTier() error {
	if s.app == nil {
		return errors.New("codex app-server is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.app.ClearConfigValue(ctx, "service_tier"); err != nil {
		return err
	}
	return nil
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

func (s *sessionStore) initAgentsFile(args string) (string, bool, error) {
	path := filepath.Join(defaultWorkdir, "AGENTS.md")
	if _, err := os.Stat(path); err == nil {
		return path, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}

	content := "# AGENTS\n\n"
	content += "## Project Notes\n\n"
	content += "- Keep changes focused and minimal.\n"
	content += "- Validate behavior before finishing.\n"
	content += "- Preserve the existing mobile Codex web UX.\n"
	if strings.TrimSpace(args) != "" {
		content += "\n## User Instructions\n\n" + strings.TrimSpace(args) + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", false, err
	}
	return path, true, nil
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

	switch method {
	case "thread/started":
		threadID := payload.ThreadID
		if threadID == "" {
			threadID = stringField(payload.Thread, "id")
		}
		if threadID != "" {
			c.recordThreadSession("", threadID, true)
		}
	case "turn/started":
		if sessionID := c.sessionIDForThread(payload.ThreadID); sessionID != "" {
			c.recordThreadTurn(payload.ThreadID, payload.TurnID)
			c.store.updateActiveTurn(sessionID, payload.TurnID)
			c.store.appendEvent(sessionID, "status", "turn started", "")
		}
	case "item/started":
		c.handleItemStarted(payload)
	case "item/agentMessage/delta":
		if sessionID := c.sessionIDForThread(payload.ThreadID); sessionID != "" && payload.Delta != "" {
			c.store.appendAssistantDelta(sessionID, payload.ItemID, payload.Delta)
		}
	case "item/completed":
		c.handleItemCompleted(payload)
	case "turn/completed":
		if sessionID := c.sessionIDForThread(payload.ThreadID); sessionID != "" {
			c.clearThreadTurn(payload.ThreadID)
			c.store.updateActiveTurn(sessionID, "")
			c.store.appendEvent(sessionID, "status", "turn completed", "")
			c.store.finishActiveTaskOK(sessionID)
		}
	case "turn/failed":
		if sessionID := c.sessionIDForThread(payload.ThreadID); sessionID != "" {
			c.clearThreadTurn(payload.ThreadID)
			c.store.updateActiveTurn(sessionID, "")
			message := strings.TrimSpace(payload.Message)
			if message == "" {
				message = "任务执行失败"
			}
			c.store.finishActiveTaskWithError(sessionID, errors.New(message))
		}
	case "error":
		if sessionID := c.sessionIDForThread(payload.ThreadID); sessionID != "" {
			c.clearThreadTurn(payload.ThreadID)
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
	sessionID := c.sessionIDForThread(payload.ThreadID)
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
	sessionID := c.sessionIDForThread(payload.ThreadID)
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

	logEntry := EventLog{
		ID:        uuid.NewString(),
		Kind:      kind,
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
		}
		if n := len(session.Messages); n > 0 {
			last := strings.TrimSpace(session.Messages[n-1].Content)
			summary.LastMessage = compactForSummary(last)
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

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func saveUploadedFile(file multipart.File, header *multipart.FileHeader) (string, error) {
	ext := strings.ToLower(filepath.Ext(header.Filename))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
	default:
		return "", errors.New("unsupported image type")
	}

	filename := uuid.NewString() + ext
	path := filepath.Join(uploadsDir, filename)
	dst, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		return "", err
	}
	return filename, nil
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

func resolveImageFiles(imageIDs []string) ([]string, []string, error) {
	urls := make([]string, 0, len(imageIDs))
	paths := make([]string, 0, len(imageIDs))
	for _, id := range imageIDs {
		id = filepath.Base(strings.TrimSpace(id))
		if id == "." || id == "" {
			continue
		}
		path := filepath.Join(uploadsDir, id)
		if _, err := os.Stat(path); err != nil {
			return nil, nil, fmt.Errorf("image not found: %s", id)
		}
		urls = append(urls, "/uploads/"+id)
		paths = append(paths, path)
	}
	return urls, paths, nil
}

func detectCodexModel() string {
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".codex", "config.toml"))
	if err != nil {
		return "unknown"
	}

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "model") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, "\"'")
		if value != "" {
			return value
		}
	}

	return "unknown"
}

func listInstalledSkills() ([]skillInfo, error) {
	root := filepath.Join(os.Getenv("HOME"), ".codex", "skills")
	items := make([]skillInfo, 0, 16)
	seen := make(map[string]bool)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) != "SKILL.md" {
			return nil
		}
		name, description, parseErr := parseSkillFrontmatter(path)
		if parseErr != nil {
			return nil
		}
		if name == "" || seen[name] {
			return nil
		}
		seen[name] = true
		items = append(items, skillInfo{
			Name:        name,
			Description: description,
			Path:        path,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func parseSkillFrontmatter(path string) (string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", errors.New("missing frontmatter")
	}
	var name, description string
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "---" {
			break
		}
		if strings.HasPrefix(line, "name:") {
			name = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "name:")), "\"'")
		}
		if strings.HasPrefix(line, "description:") {
			description = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "description:")), "\"'")
		}
	}
	return name, description, nil
}

func detectTaskConcurrency() int {
	cpus := runtime.NumCPU()
	switch {
	case cpus <= 2:
		return 1
	case cpus <= 4:
		return 2
	default:
		return cpus / 2
	}
}

func mergeMaps(base map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func shouldRetryFallback(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "approval") ||
		strings.Contains(text, "unknown variant") ||
		strings.Contains(text, "expected one of") ||
		strings.Contains(text, "sandbox") ||
		strings.Contains(text, "on-request") ||
		strings.Contains(text, "onrequest")
}

func rpcCallError(method string, rpcErr *rpcError) error {
	if rpcErr == nil {
		return fmt.Errorf("%s failed", method)
	}
	return fmt.Errorf("%s failed: %s", method, strings.TrimSpace(rpcErr.Message))
}

func packetID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		return num.String()
	}
	return ""
}

func mustMarshalJSON(v interface{}) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func stringField(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			if text, ok := value.(string); ok {
				return text
			}
		}
	}
	return ""
}

func intField(m map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		}
	}
	return 0, false
}

func normalizeItemType(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func extractAppServerErrorMessage(raw json.RawMessage, payload notificationEnvelope) string {
	if msg := strings.TrimSpace(payload.Message); msg != "" {
		return msg
	}

	var envelope map[string]interface{}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if msg := firstNonEmpty(
			lookupNestedString(envelope, "message"),
			lookupNestedString(envelope, "error.message"),
			lookupNestedString(envelope, "error.details"),
			lookupNestedString(envelope, "details"),
			lookupNestedString(envelope, "additionalDetails"),
			lookupNestedString(envelope, "error.additionalDetails"),
		); strings.TrimSpace(msg) != "" {
			return strings.TrimSpace(msg)
		}
	}

	return strings.TrimSpace(string(raw))
}

func lookupNestedString(data map[string]interface{}, path string) string {
	current := interface{}(data)
	for _, part := range strings.Split(path, ".") {
		node, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current, ok = node[part]
		if !ok {
			return ""
		}
	}
	if text, ok := current.(string); ok {
		return text
	}
	return ""
}

func detectServiceTier() string {
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".codex", "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "service_tier") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		if value != "" {
			return value
		}
	}
	return ""
}

func compactForSummary(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(text) <= 72 {
		return text
	}
	return text[:72]
}

func authTokenForPassword(password string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(password)))
	return hex.EncodeToString(sum[:])
}

func normalizeWorkdir(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultWorkdir
	}
	if !filepath.IsAbs(value) {
		return defaultWorkdir
	}
	return filepath.Clean(value)
}

func validateWorkdir(value string) (string, error) {
	workdir := normalizeWorkdir(value)
	info, err := os.Stat(workdir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("工作目录不存在")
		}
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("工作目录不是目录")
	}
	return workdir, nil
}

func tierToFastArg(tier string) string {
	if strings.EqualFold(strings.TrimSpace(tier), "fast") {
		return "on"
	}
	return "off"
}

func cloneClients(src map[*clientConn]struct{}) map[*clientConn]struct{} {
	dst := make(map[*clientConn]struct{}, len(src))
	for client := range src {
		dst[client] = struct{}{}
	}
	return dst
}

func broadcastJSON(clients map[*clientConn]struct{}, event serverEvent) {
	for client := range clients {
		client.mu.Lock()
		_ = client.conn.WriteJSON(event)
		client.mu.Unlock()
	}
}
