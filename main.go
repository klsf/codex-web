package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed static
var embeddedStatic embed.FS

func main() {
	passwordFlag := flag.String("password", "codex", "login password for Code Web New")
	flag.Parse()

	staticFS, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		log.Fatalf("init static fs: %v", err)
	}
	if err := ensureUploadsDir(); err != nil {
		log.Fatalf("init uploads dir: %v", err)
	}

	store := newSessionStore(*passwordFlag)

	mux := http.NewServeMux()
	mux.HandleFunc("/", store.handleIndex(staticFS))
	mux.Handle("/app/", withCache(http.FileServer(http.FS(staticFS))))
	mux.Handle("/style.css", withCache(http.FileServer(http.FS(staticFS))))
	mux.Handle("/uploads/", withCache(http.StripPrefix("/uploads/", http.FileServer(http.Dir(uploadsDir)))))
	mux.HandleFunc("/app-config.js", store.handleAppConfig)
	mux.HandleFunc("/api/login", store.handleLogin)
	mux.HandleFunc("/api/auth", store.handleAuth)
	mux.HandleFunc("/api/logout", store.handleLogout)
	mux.HandleFunc("/api/sessions", store.handleSessions)
	mux.HandleFunc("/api/session/restore", store.handleRestoreSession)
	mux.HandleFunc("/api/session/delete", store.handleDeleteSession)
	mux.HandleFunc("/api/session/new", store.handleNewSession)
	mux.HandleFunc("/api/send", store.handleSend)
	mux.HandleFunc("/api/status", store.handleStatus)
	mux.HandleFunc("/ws", store.handleWS)

	server := &http.Server{
		Addr:    ":8080",
		Handler: store.withAuth(mux),
	}

	go func() {
		log.Printf("Code Web New listening on http://127.0.0.1%s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}

func withCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, max-age=0")
		next.ServeHTTP(w, r)
	})
}
