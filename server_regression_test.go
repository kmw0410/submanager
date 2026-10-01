package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegisteredAPIRoutesRequireAuthentication(t *testing.T) {
	a := newTestApplication(t)
	routes := securityHeaders(a.routes())
	for _, entry := range []string{
		"GET /api/state", "GET /api/upcoming", "GET /api/upcoming/export", "POST /api/subscriptions", "PUT /api/subscriptions/1", "POST /api/subscriptions/1/skip", "POST /api/subscriptions/1/cancel", "PUT /api/settings", "PUT /api/account/email", "PUT /api/account/password", "GET /api/sessions", "DELETE /api/sessions", "DELETE /api/sessions/1", "POST /api/payment-methods", "PUT /api/payment-methods/1", "DELETE /api/payment-methods/1", "POST /api/currencies", "DELETE /api/currencies/1", "POST /api/notifications/test", "GET /api/pwa/vapid-public", "POST /api/pwa/subscriptions", "DELETE /api/pwa/subscriptions", "GET /api/data/export", "POST /api/data/import", "POST /auth/logout",
	} {
		t.Run(entry, func(t *testing.T) {
			method, path, _ := strings.Cut(entry, " ")
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d", w.Code)
			}
			if w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "same-origin" {
				t.Fatal("security headers missing")
			}
		})
	}
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("health depends on database status=%d", w.Code)
	}
}

func TestRegisteredAssetsAreServed(t *testing.T) {
	a := newTestApplication(t)
	for _, path := range []string{"/assets/app.css", "/assets/app.js", "/sw.js", "/manifest.webmanifest", "/icon.svg"} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Fatalf("asset %s status=%d", path, w.Code)
		}
	}
}

func TestExpiredSessionCannotReachProtectedRoute(t *testing.T) {
	a := newTestApplication(t)
	token, hash, _, err := newSessionCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`INSERT INTO sessions(user_id,token_hash,expires_at) VALUES(1,?,?)`, hash, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	r.AddCookie(&http.Cookie{Name: "submanager_session", Value: token})
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status=%d", w.Code)
	}
}

func TestRecoveredPanicDoesNotExposeItsValue(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	marker := "private-test-marker"
	w := httptest.NewRecorder()
	recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(marker) })).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), marker) || strings.Contains(output.String(), marker) {
		t.Fatal("panic details exposed")
	}
}

func TestAuthLimiterSweepsUnvisitedExpiredKeys(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newAttemptLimiter()
	l.now = func() time.Time { return now }
	l.failure("old-ip", "old-account")
	now = now.Add(authAttemptWindow + time.Second)
	l.failure("new-ip", "new-account")
	if len(l.attempts) != 2 {
		t.Fatalf("retained expired keys count=%d", len(l.attempts))
	}
}
