package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

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
	Type      string      `json:"type"`
	SessionID string      `json:"sessionId,omitempty"`
	Session   *Session    `json:"session,omitempty"`
	Message   *Message    `json:"message,omitempty"`
	Log       *EventLog   `json:"log,omitempty"`
	TaskID    string      `json:"taskId,omitempty"`
	Running   bool        `json:"running,omitempty"`
	Error     string      `json:"error,omitempty"`
	Payload   interface{} `json:"payload,omitempty"`
	Meta      *appMeta    `json:"meta,omitempty"`
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

func authTokenForPassword(password string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(password)))
	return hex.EncodeToString(sum[:])
}
