package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

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

	server := &http.Server{
		Addr:    addr,
		Handler: store.withAuth(mux),
	}
	serverErr := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	shutdownSignals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()

	log.Printf("listening on %s", addr)
	log.Printf("codex task concurrency limit: %d", store.maxConcurrent)
	select {
	case err := <-serverErr:
		if err == nil {
			return
		}
		log.Fatal(err)
	case <-ctx.Done():
	}

	log.Printf("shutdown signal received, stopping services")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown failed: %v", err)
		if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			log.Printf("force close http server failed: %v", closeErr)
		}
	}
}
