package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestFirstAccountIsAdminAndPasswordIsHashed(t *testing.T) {
	a := newTestApplication(t)
	r, w := jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{"name": "관리자", "setupToken": "test-setup-token", "email": "admin@example.com", "password": "safe-password"})
	a.setupAccount(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup status=%d body=%s", w.Code, w.Body.String())
	}
	var name, email, hash string
	var admin bool
	if err := a.db.QueryRow(`SELECT name,email,password_hash,is_admin FROM users WHERE id=1`).Scan(&name, &email, &hash, &admin); err != nil {
		t.Fatal(err)
	}
	if name != "관리자" || email != "admin@example.com" || !admin || hash == "safe-password" {
		t.Fatal("administrator was not stored securely")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("safe-password")); err != nil {
		t.Fatal(err)
	}
	r, w = jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{"name": "두 번째", "setupToken": "test-setup-token", "email": "two@example.com", "password": "safe-password"})
	a.setupAccount(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("second setup status=%d", w.Code)
	}
}

func TestFirstAccountRequiresSetupToken(t *testing.T) {
	a := newTestApplication(t)
	r, w := jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{
		"name": "관리자", "email": "admin@example.com", "password": "safe-password", "setupToken": "wrong-setup-token",
	})
	a.setupAccount(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong setup token status=%d body=%s", w.Code, w.Body.String())
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=1 AND password_hash<>''`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("administrator was created with an invalid setup token")
	}
}

func TestSetupTokenIsRegeneratedSecurely(t *testing.T) {
	first, err := newSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 48 || len(second) != 48 || first == second {
		t.Fatalf("setup tokens were not independently generated: first=%d second=%d equal=%t", len(first), len(second), first == second)
	}
}

func TestLoginRateLimitAndSuccessfulReset(t *testing.T) {
	a := newTestApplication(t)
	setupRequest, setupRecorder := jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{
		"name": "관리자", "setupToken": "test-setup-token", "email": "admin@example.com", "password": "safe-password",
	})
	a.setupAccount(setupRecorder, setupRequest)

	for i := 0; i < authAttemptLimit; i++ {
		request, recorder := jsonRequest(t, http.MethodPost, "/auth/login", map[string]string{"email": "admin@example.com", "password": "wrong-password"})
		a.login(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("failed login %d status=%d body=%s", i+1, recorder.Code, recorder.Body.String())
		}
	}
	request, recorder := jsonRequest(t, http.MethodPost, "/auth/login", map[string]string{"email": "admin@example.com", "password": "safe-password"})
	a.login(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" {
		t.Fatalf("rate-limited login status=%d retry-after=%q", recorder.Code, recorder.Header().Get("Retry-After"))
	}

	a.authLimiter.now = func() time.Time { return time.Now().Add(authAttemptWindow) }
	request, recorder = jsonRequest(t, http.MethodPost, "/auth/login", map[string]string{"email": "admin@example.com", "password": "safe-password"})
	a.login(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login after rate-limit window status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestActiveSessionsAreCapped(t *testing.T) {
	a := newTestApplication(t)
	setupRequest, setupRecorder := jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{
		"name": "관리자", "setupToken": "test-setup-token", "email": "admin@example.com", "password": "safe-password",
	})
	a.setupAccount(setupRecorder, setupRequest)
	for i := 0; i < maxActiveSessions+3; i++ {
		request := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
		request.Header.Set("User-Agent", "session-"+strconv.Itoa(i))
		if err := a.createSession(httptest.NewRecorder(), request, 1); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != maxActiveSessions {
		t.Fatalf("active sessions=%d want=%d", count, maxActiveSessions)
	}
}

func TestAdministratorCanChangeEmail(t *testing.T) {
	a := newTestApplication(t)
	setupRequest, setupRecorder := jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{"name": "관리자", "setupToken": "test-setup-token", "email": "admin@example.com", "password": "safe-password"})
	a.setupAccount(setupRecorder, setupRequest)
	if setupRecorder.Code != http.StatusCreated || len(setupRecorder.Result().Cookies()) != 1 {
		t.Fatalf("setup status=%d cookies=%d", setupRecorder.Code, len(setupRecorder.Result().Cookies()))
	}
	cookie := setupRecorder.Result().Cookies()[0]

	request, recorder := jsonRequest(t, http.MethodPut, "/api/account/email", map[string]string{"email": "next@example.com", "currentPassword": "wrong-password"})
	request.AddCookie(cookie)
	a.updateAccountEmail(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("wrong password status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request, recorder = jsonRequest(t, http.MethodPut, "/api/account/email", map[string]string{"email": " Next@Example.com ", "currentPassword": "safe-password"})
	request.AddCookie(cookie)
	a.updateAccountEmail(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("email update status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var email string
	if err := a.db.QueryRow(`SELECT email FROM users WHERE id=1`).Scan(&email); err != nil {
		t.Fatal(err)
	}
	if email != "next@example.com" {
		t.Fatalf("email=%q", email)
	}
	state, err := a.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.User.Email != email {
		t.Fatalf("state email=%q", state.User.Email)
	}
}

func TestAdministratorCanChangePasswordAndRotateSessions(t *testing.T) {
	a := newTestApplication(t)
	setupRequest, setupRecorder := jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{"name": "관리자", "setupToken": "test-setup-token", "email": "admin@example.com", "password": "safe-password"})
	a.setupAccount(setupRecorder, setupRequest)
	oldCookie := setupRecorder.Result().Cookies()[0]

	request, recorder := jsonRequest(t, http.MethodPut, "/api/account/password", map[string]string{"currentPassword": "safe-password", "newPassword": "new-safe-password"})
	request.AddCookie(oldCookie)
	a.updateAccountPassword(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("password update status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(recorder.Result().Cookies()) != 1 {
		t.Fatalf("new session cookies=%d", len(recorder.Result().Cookies()))
	}
	newCookie := recorder.Result().Cookies()[0]
	oldRequest := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	oldRequest.AddCookie(oldCookie)
	if _, ok := a.authenticatedUser(oldRequest); ok {
		t.Fatal("old session remained valid after password change")
	}
	newRequest := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	newRequest.AddCookie(newCookie)
	if _, ok := a.authenticatedUser(newRequest); !ok {
		t.Fatal("replacement session is not valid")
	}
	var hash string
	var sessions int
	if err := a.db.QueryRow(`SELECT password_hash FROM users WHERE id=1`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("new-safe-password")) != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte("safe-password")) == nil {
		t.Fatal("password hash was not securely replaced")
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=1`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("sessions=%d", sessions)
	}
}

func TestAdministratorCanManageOtherSessions(t *testing.T) {
	a := newTestApplication(t)
	setupRequest, setupRecorder := jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{"name": "관리자", "setupToken": "test-setup-token", "email": "admin@example.com", "password": "safe-password"})
	setupRequest.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0) Chrome/127.0")
	a.setupAccount(setupRecorder, setupRequest)
	if setupRecorder.Code != http.StatusCreated {
		t.Fatalf("setup status=%d body=%s", setupRecorder.Code, setupRecorder.Body.String())
	}
	windowsCookie := setupRecorder.Result().Cookies()[0]

	login := func(userAgent string) *http.Cookie {
		t.Helper()
		request, recorder := jsonRequest(t, http.MethodPost, "/auth/login", map[string]string{"email": "admin@example.com", "password": "safe-password"})
		request.Header.Set("User-Agent", userAgent)
		a.login(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("login status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		return recorder.Result().Cookies()[0]
	}
	currentCookie := login("Mozilla/5.0 (iPhone) Version/17.0 Mobile Safari/604.1")
	linuxCookie := login("Mozilla/5.0 (X11; Linux x86_64) Firefox/128.0")

	listRequest := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	listRequest.AddCookie(currentCookie)
	listRecorder := httptest.NewRecorder()
	a.listSessions(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var sessions struct {
		Current    sessionState   `json:"current"`
		Registered []sessionState `json:"registered"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if sessions.Current.Device != "Safari · iPhone" || len(sessions.Registered) != 2 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}

	deleteCurrent := httptest.NewRequest(http.MethodDelete, "/api/sessions/current", nil)
	deleteCurrent.SetPathValue("id", strconv.FormatInt(sessions.Current.ID, 10))
	deleteCurrent.AddCookie(currentCookie)
	deleteCurrentRecorder := httptest.NewRecorder()
	a.deleteSession(deleteCurrentRecorder, deleteCurrent)
	if deleteCurrentRecorder.Code != http.StatusForbidden {
		t.Fatalf("delete current status=%d body=%s", deleteCurrentRecorder.Code, deleteCurrentRecorder.Body.String())
	}

	var windowsID int64
	if err := a.db.QueryRow(`SELECT id FROM sessions WHERE token_hash=?`, sessionTokenHash(windowsCookie.Value)).Scan(&windowsID); err != nil {
		t.Fatal(err)
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/sessions/other", nil)
	deleteRequest.SetPathValue("id", strconv.FormatInt(windowsID, 10))
	deleteRequest.AddCookie(currentCookie)
	deleteRecorder := httptest.NewRecorder()
	a.deleteSession(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	windowsRequest := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	windowsRequest.AddCookie(windowsCookie)
	if _, ok := a.authenticatedUser(windowsRequest); ok {
		t.Fatal("individually ended session remained valid")
	}

	deleteAllRequest := httptest.NewRequest(http.MethodDelete, "/api/sessions", nil)
	deleteAllRequest.AddCookie(currentCookie)
	deleteAllRecorder := httptest.NewRecorder()
	a.deleteOtherSessions(deleteAllRecorder, deleteAllRequest)
	if deleteAllRecorder.Code != http.StatusOK {
		t.Fatalf("delete all status=%d body=%s", deleteAllRecorder.Code, deleteAllRecorder.Body.String())
	}
	linuxRequest := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	linuxRequest.AddCookie(linuxCookie)
	if _, ok := a.authenticatedUser(linuxRequest); ok {
		t.Fatal("bulk-ended session remained valid")
	}
	currentRequest := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	currentRequest.AddCookie(currentCookie)
	if _, ok := a.authenticatedUser(currentRequest); !ok {
		t.Fatal("bulk ending other sessions removed the current session")
	}
}
