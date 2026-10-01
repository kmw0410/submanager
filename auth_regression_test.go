package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentSetupCreatesExactlyOneAdministrator(t *testing.T) {
	a := newTestApplication(t)
	type result struct {
		email  string
		status int
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, email := range []string{"first@example.com", "second@example.com"} {
		r, w := jsonRequest(t, http.MethodPost, "/auth/setup", authInput{Name: "관리자", Email: email, Password: "safe-password", SetupToken: a.setupToken})
		wg.Add(1)
		go func() { defer wg.Done(); <-start; a.setupAccount(w, r); results <- result{email, w.Code} }()
	}
	close(start)
	wg.Wait()
	close(results)
	winner := ""
	for result := range results {
		switch result.status {
		case http.StatusCreated:
			if winner != "" {
				t.Fatal("multiple setup attempts succeeded")
			}
			winner = result.email
		case http.StatusForbidden:
		default:
			t.Fatalf("setup status=%d", result.status)
		}
	}
	var email string
	var accounts, sessions int
	if err := a.db.QueryRow(`SELECT email,(SELECT COUNT(*) FROM users WHERE password_hash<>''),(SELECT COUNT(*) FROM sessions) FROM users WHERE id=1`).Scan(&email, &accounts, &sessions); err != nil {
		t.Fatal(err)
	}
	if winner == "" || email != winner || accounts != 1 || sessions != 1 {
		t.Fatal("concurrent setup replaced the administrator or issued extra sessions")
	}
}

func TestSessionCookieSecurityLogoutAndExpiredAccess(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			a := newTestApplication(t)
			r := httptest.NewRequest(http.MethodPost, scheme+"://example.com/auth/login", nil)
			w := httptest.NewRecorder()
			if err := a.createSession(w, r, 1); err != nil {
				t.Fatal(err)
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatal("session cookie missing")
			}
			cookie := cookies[0]
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure != (scheme == "https") || cookie.MaxAge != 30*24*60*60 || len(cookie.Value) != 64 {
				t.Fatal("session cookie security contract changed")
			}
			var stored string
			if err := a.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored == cookie.Value || stored != sessionTokenHash(cookie.Value) {
				t.Fatal("session token was not stored as a hash")
			}
			protected := a.requireAuth(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			request := httptest.NewRequest(http.MethodGet, "/api/state", nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			protected(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatal("fresh session rejected")
			}
			logout := httptest.NewRecorder()
			a.requireAuth(a.logout)(logout, request)
			expiredCookie := logout.Result().Cookies()
			if len(expiredCookie) != 1 || expiredCookie[0].MaxAge != -1 || expiredCookie[0].Value != "" || !expiredCookie[0].HttpOnly || expiredCookie[0].Path != "/" {
				t.Fatal("logout must expire its cookie")
			}
			response = httptest.NewRecorder()
			protected(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatal("logged out session retained access")
			}
			if _, err := a.db.Exec(`INSERT INTO sessions(user_id,token_hash,expires_at) VALUES(1,?,'2000-01-01T00:00:00Z')`, stored); err != nil {
				t.Fatal(err)
			}
			response = httptest.NewRecorder()
			protected(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatal("expired session retained access")
			}
		})
	}
}

func TestAuthenticationLimitSeparatesIPAndNormalizedIdentity(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := newAttemptLimiter()
	limiter.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	keys := authAttemptKeys(request, " Admin@Example.com ", "login")
	for i := 0; i < authAttemptLimit; i++ {
		limiter.failure(keys...)
	}
	otherIP := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	otherIP.RemoteAddr = "192.0.2.2:5678"
	if limiter.allowed(authAttemptKeys(otherIP, "admin@example.com", "login")...) {
		t.Fatal("changing IP bypassed normalized account limit")
	}
	if limiter.allowed(authAttemptKeys(request, "other@example.com", "login")...) {
		t.Fatal("changing account bypassed IP limit")
	}
	if !limiter.allowed(authAttemptKeys(otherIP, "other@example.com", "login")...) {
		t.Fatal("unrelated account and IP were blocked")
	}
	now = now.Add(authAttemptWindow - time.Nanosecond)
	if limiter.allowed(keys...) {
		t.Fatal("limit expired too early")
	}
	now = now.Add(time.Nanosecond)
	if !limiter.allowed(keys...) {
		t.Fatal("limit did not expire at window boundary")
	}
}

func TestJSONRequestRejectsMalformedUnknownTrailingAndOversizedBodies(t *testing.T) {
	for _, body := range []string{`{"name":`, `{"unexpected":true}`, `{"name":"ok"} {}`, `{"name":"` + strings.Repeat("x", 1<<20) + `"}`} {
		request := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(body))
		response := httptest.NewRecorder()
		var input struct{ Name string }
		if decode(response, request, &input) || response.Code != http.StatusBadRequest {
			t.Fatal("invalid or oversized JSON accepted")
		}
		if strings.Contains(response.Body.String(), "unexpected") {
			t.Fatal("decoder exposed raw request details")
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader("{\"name\":\"ok\"}\n\t "))
	response := httptest.NewRecorder()
	var input struct{ Name string }
	if !decode(response, request, &input) || input.Name != "ok" {
		t.Fatal("valid JSON with trailing whitespace rejected")
	}
}
