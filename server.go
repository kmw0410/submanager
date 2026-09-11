package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed web/*
var webFS embed.FS

type application struct {
	db                     *sql.DB
	tpl                    *template.Template
	authTpl                *template.Template
	location               *time.Location
	setupToken             string
	authLimiter            *attemptLimiter
	notificationHTTPClient *http.Client
}

const (
	authAttemptLimit  = 5
	authAttemptWindow = 15 * time.Minute
	maxActiveSessions = 10
)

type attemptState struct {
	count       int
	windowStart time.Time
}

type attemptLimiter struct {
	mu       sync.Mutex
	attempts map[string]attemptState
	now      func() time.Time
}

func newAttemptLimiter() *attemptLimiter {
	return &attemptLimiter{attempts: make(map[string]attemptState), now: time.Now}
}

func (l *attemptLimiter) allowed(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, key := range keys {
		state, ok := l.attempts[key]
		if ok && now.Sub(state.windowStart) >= authAttemptWindow {
			delete(l.attempts, key)
			continue
		}
		if ok && state.count >= authAttemptLimit {
			return false
		}
	}
	return true
}

func (l *attemptLimiter) failure(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, key := range keys {
		state, ok := l.attempts[key]
		if !ok || now.Sub(state.windowStart) >= authAttemptWindow {
			state = attemptState{windowStart: now}
		}
		state.count++
		l.attempts[key] = state
	}
}

func (l *attemptLimiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		delete(l.attempts, key)
	}
}

func main() {
	dbPath := env("DB_PATH", "./data/submanager.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatal(err)
	}
	if alreadyComplete, err := migrateDataIfRequested(os.Getenv("MIGRATE_DATA"), dbPath, env("MIGRATE_DATA_SOURCE", "/migration-source/submanager.db")); err != nil {
		log.Fatalf("data migration: %v", err)
	} else if alreadyComplete {
		log.Print("migrate is already complete. skipping environment")
	}
	db, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	loc, err := time.LoadLocation(env("TZ", "Asia/Seoul"))
	if err != nil {
		loc = time.FixedZone("Asia/Seoul", 9*60*60)
	}
	app := &application{
		db:          db,
		location:    loc,
		authLimiter: newAttemptLimiter(),
	}
	app.tpl = template.Must(template.New("index.html").ParseFS(webFS, "web/index.html"))
	app.authTpl = template.Must(template.New("auth.html").ParseFS(webFS, "web/auth.html"))
	if err := app.migrate(); err != nil {
		log.Fatal(err)
	}
	accountExists, err := app.accountExists()
	if err != nil {
		log.Fatal(err)
	}
	legacySetupTokenPath := filepath.Join(filepath.Dir(dbPath), ".submanager-setup-token")
	if err := os.Remove(legacySetupTokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("remove legacy setup token file: %v", err)
	}
	if !accountExists {
		app.setupToken, err = newSetupToken()
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Initial setup token: %s", app.setupToken)
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	go app.notificationLoop(workerCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", app.index)
	mux.HandleFunc("GET /assets/app.css", serveEmbedded("web/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /assets/app.js", serveEmbedded("web/app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /manifest.webmanifest", serveEmbedded("web/manifest.webmanifest", "application/manifest+json; charset=utf-8"))
	mux.HandleFunc("GET /icon.svg", serveEmbedded("web/icon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /sw.js", serveEmbedded("web/sw.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("POST /auth/setup", app.setupAccount)
	mux.HandleFunc("POST /auth/login", app.login)
	mux.HandleFunc("POST /auth/logout", app.requireAuth(app.logout))
	mux.HandleFunc("GET /api/state", app.requireAuth(app.getState))
	mux.HandleFunc("GET /api/upcoming", app.requireAuth(app.getUpcomingMonth))
	mux.HandleFunc("GET /api/upcoming/export", app.requireAuth(app.exportUpcoming))
	mux.HandleFunc("POST /api/subscriptions", app.requireAuth(app.createSubscription))
	mux.HandleFunc("PUT /api/subscriptions/{id}", app.requireAuth(app.updateSubscription))
	mux.HandleFunc("POST /api/subscriptions/{id}/skip", app.requireAuth(app.skipSubscription))
	mux.HandleFunc("POST /api/subscriptions/{id}/cancel", app.requireAuth(app.cancelSubscription))
	mux.HandleFunc("PUT /api/settings", app.requireAuth(app.updateSettings))
	mux.HandleFunc("PUT /api/account/email", app.requireAuth(app.updateAccountEmail))
	mux.HandleFunc("PUT /api/account/password", app.requireAuth(app.updateAccountPassword))
	mux.HandleFunc("GET /api/sessions", app.requireAuth(app.listSessions))
	mux.HandleFunc("DELETE /api/sessions", app.requireAuth(app.deleteOtherSessions))
	mux.HandleFunc("DELETE /api/sessions/{id}", app.requireAuth(app.deleteSession))
	mux.HandleFunc("POST /api/payment-methods", app.requireAuth(app.createPaymentMethod))
	mux.HandleFunc("PUT /api/payment-methods/{id}", app.requireAuth(app.updatePaymentMethod))
	mux.HandleFunc("DELETE /api/payment-methods/{id}", app.requireAuth(app.deletePaymentMethod))
	mux.HandleFunc("POST /api/currencies", app.requireAuth(app.createCurrency))
	mux.HandleFunc("DELETE /api/currencies/{id}", app.requireAuth(app.deleteCurrency))
	mux.HandleFunc("POST /api/notifications/test", app.requireAuth(app.testNotification))
	mux.HandleFunc("GET /api/data/export", app.requireAuth(app.exportData))
	mux.HandleFunc("POST /api/data/import", app.requireAuth(app.importData))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{
		Addr:              ":" + env("PORT", "8080"),
		Handler:           logging(recoverer(securityHeaders(mux))),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Printf("Submanager listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	stopWorker()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func env(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}

func (a *application) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	exists, err := a.accountExists()
	if err != nil {
		a.fail(w, err)
		return
	}
	if !exists {
		a.renderAuth(w, "setup")
		return
	}
	if _, ok := a.authenticatedUser(r); !ok {
		a.renderAuth(w, "login")
		return
	}
	state, err := a.loadState()
	if err != nil {
		a.fail(w, err)
		return
	}
	b, _ := json.Marshal(state)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tpl.Execute(w, map[string]any{"InitialState": template.JS(b)}); err != nil {
		log.Print(err)
	}
}

func (a *application) renderAuth(w http.ResponseWriter, mode string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.authTpl.Execute(w, map[string]string{"Mode": mode}); err != nil {
		log.Print(err)
	}
}

func serveEmbedded(path, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		b, err := webFS.ReadFile(path)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic: %v", v)
				writeJSON(w, 500, map[string]string{"error": "잠시 후 다시 시도해 주세요"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
