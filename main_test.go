package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

func newTestApplication(t *testing.T) *application {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "submanager.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	loc, _ := time.LoadLocation("Asia/Seoul")
	a := &application{db: db, location: loc, setupToken: "test-setup-token", authLimiter: newAttemptLimiter()}
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	return a
}

func jsonRequest(t *testing.T, method, target string, value any) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(method, target, bytes.NewReader(body)), httptest.NewRecorder()
}

func compactSource(source string) string {
	return strings.Join(strings.Fields(source), "")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestBuiltinSeedsAreIdempotent(t *testing.T) {
	a := newTestApplication(t)
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	var services, methods, currencies int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM services`).Scan(&services); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM payment_methods WHERE is_builtin=1`).Scan(&methods); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM currencies WHERE code IN ('KRW','USD','JPY','EUR','TRY','ARS') AND is_builtin=1`).Scan(&currencies); err != nil {
		t.Fatal(err)
	}
	if services != 18 || methods != 5 || currencies != 6 {
		t.Fatalf("services=%d methods=%d currencies=%d", services, methods, currencies)
	}
	var category, cycle, currency string
	var supportsTrial bool
	if err := a.db.QueryRow(`SELECT default_category,default_billing_cycle,default_currency,supports_trial FROM services WHERE name=?`, "밀리의 서재").Scan(&category, &cycle, &currency, &supportsTrial); err != nil {
		t.Fatal(err)
	}
	if category != "독서" || cycle != "monthly" || currency != "KRW" || !supportsTrial {
		t.Fatalf("unexpected 밀리의 서재 seed: category=%q cycle=%q currency=%q supportsTrial=%t", category, cycle, currency, supportsTrial)
	}
	for _, name := range []string{"Netflix", "iCloud+", "배민클럽", "쿠팡 와우 멤버십", "TVING", "Wavve", "Disney+", "WATCHA", "Google One"} {
		var count int
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM services WHERE name=?`, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("service %q count=%d", name, count)
		}
	}
}

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

func TestSetupTokenFileIsRegeneratedSecurely(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".submanager-setup-token")
	first, err := createSetupTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := createSetupTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 48 || len(second) != 48 || first == second {
		t.Fatalf("setup tokens were not independently generated: first=%d second=%d equal=%t", len(first), len(second), first == second)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(contents)) != second {
		t.Fatal("setup token file does not contain the latest token")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("setup token permissions=%o", info.Mode().Perm())
	}
}

func TestSetupTokenFileIsRemovedAfterAdministratorCreation(t *testing.T) {
	a := newTestApplication(t)
	a.setupTokenPath = filepath.Join(t.TempDir(), ".submanager-setup-token")
	token, err := createSetupTokenFile(a.setupTokenPath)
	if err != nil {
		t.Fatal(err)
	}
	a.setupToken = token
	request, recorder := jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{
		"name": "관리자", "email": "admin@example.com", "password": "safe-password", "setupToken": token,
	})
	a.setupAccount(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("setup status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(a.setupTokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("setup token file still exists: %v", err)
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

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestRuntimeTimezoneIsExcludedFromStateAndBackup(t *testing.T) {
	a := newTestApplication(t)

	state, err := a.loadState()
	if err != nil {
		t.Fatal(err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(stateJSON)), "timezone") {
		t.Fatalf("runtime timezone leaked into application state: %s", stateJSON)
	}

	recorder := httptest.NewRecorder()
	a.exportData(recorder, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(strings.ToLower(recorder.Body.String()), "timezone") {
		t.Fatalf("runtime timezone leaked into backup: %s", recorder.Body.String())
	}

	var legacyBackup map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &legacyBackup); err != nil {
		t.Fatal(err)
	}
	legacyBackup["settings"].(map[string]any)["timezone"] = "UTC"
	legacyJSON, err := json.Marshal(legacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	importRecorder := httptest.NewRecorder()
	a.importData(importRecorder, httptest.NewRequest(http.MethodPost, "/api/data/import", bytes.NewReader(legacyJSON)))
	if importRecorder.Code != http.StatusOK {
		t.Fatalf("legacy timezone backup import status=%d body=%s", importRecorder.Code, importRecorder.Body.String())
	}
	if a.location.String() != "Asia/Seoul" {
		t.Fatalf("backup timezone changed runtime location to %q", a.location)
	}
}

func TestPriceHistoryKeepsPastMonthsStable(t *testing.T) {
	a := newTestApplication(t)
	var paymentID int64
	if err := a.db.QueryRow(`SELECT id FROM payment_methods WHERE is_builtin=1 LIMIT 1`).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	result, err := a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('가격 변경',2000,'USD','monthly',20,'2026-07-20',?,'2026-07-01')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if _, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,14900,'KRW','2026-07-01'),(?,2000,'USD','2026-08-01')`, id, id); err != nil {
		t.Fatal(err)
	}
	july, err := a.monthTotals("2026-07")
	if err != nil {
		t.Fatal(err)
	}
	august, err := a.monthTotals("2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if july["KRW"] != 14900 || august["USD"] != 2000 {
		t.Fatalf("july=%v august=%v", july, august)
	}
}

func TestPaymentOccurrencesReuseBillingHistoryAndOccurrenceState(t *testing.T) {
	a := newTestApplication(t)
	var paymentID int64
	var paymentName string
	if err := a.db.QueryRow(`SELECT id,name FROM payment_methods WHERE is_builtin=1 ORDER BY id LIMIT 1`).Scan(&paymentID, &paymentName); err != nil {
		t.Fatal(err)
	}

	result, err := a.db.Exec(`INSERT INTO subscriptions(service_name,color,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('Discord Nitro','#5865F2',1000,'USD','monthly',24,'2026-07-24',?,'2026-07-01')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	monthlyID, _ := result.LastInsertId()
	if _, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,900,'USD','2026-07-01'),(?,1000,'USD','2026-09-01')`, monthlyID, monthlyID); err != nil {
		t.Fatal(err)
	}

	result, err = a.db.Exec(`INSERT INTO subscriptions(service_name,color,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at,trial_ends_at) VALUES('첫 결제','#9AB8A8',17000,'KRW','monthly',31,'2026-08-31',?,'2026-08-01','2026-08-30')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	trialID, _ := result.LastInsertId()
	if _, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,17000,'KRW','2026-08-01')`, trialID); err != nil {
		t.Fatal(err)
	}

	result, err = a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('건너뜀',4900,'KRW','monthly',24,'2026-07-24',?,'2026-07-01')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	skippedID, _ := result.LastInsertId()
	if _, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,4900,'KRW','2026-07-01'); INSERT INTO subscription_occurrences(subscription_id,period,scheduled_date,amount,currency,skipped) VALUES(?,'2026-08','2026-08-24',4900,'KRW',1)`, skippedID, skippedID); err != nil {
		t.Fatal(err)
	}

	if _, err = a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('다른 달 연간',12000,'KRW','yearly',10,'2026-09-10',?,'2026-01-01'); INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at,status,cancelled_at) VALUES('해지 완료',5000,'KRW','monthly',5,'2026-01-05',?,'2026-01-01','cancelled','2026-07-31')`, paymentID, paymentID); err != nil {
		t.Fatal(err)
	}

	month, err := a.loadPaymentOccurrences("2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if len(month.Items) != 3 {
		t.Fatalf("unexpected August occurrences: %+v", month.Items)
	}
	if month.Items[0].ServiceName != "Discord Nitro" || month.Items[0].Amount != 900 || month.Items[0].Currency != "USD" || month.Items[0].PaymentMethodName != paymentName {
		t.Fatalf("historical price or payment method was not preserved: %+v", month.Items[0])
	}
	if month.Items[1].ServiceName != "건너뜀" || !month.Items[1].Skipped {
		t.Fatalf("skipped occurrence is missing from calendar details: %+v", month.Items)
	}
	if month.Items[2].ServiceName != "첫 결제" || !month.Items[2].FirstPayment || month.Items[2].ScheduledDate != "2026-08-31" {
		t.Fatalf("trial first payment was not anchored correctly: %+v", month.Items[2])
	}
	if month.Totals["USD"] != 900 || month.Totals["KRW"] != 17000 {
		t.Fatalf("skipped or mixed-currency totals are wrong: %+v", month.Totals)
	}
}

func TestUpcomingMonthRejectsInvalidPeriod(t *testing.T) {
	a := newTestApplication(t)
	recorder := httptest.NewRecorder()
	a.getUpcomingMonth(recorder, httptest.NewRequest(http.MethodGet, "/api/upcoming?month=2026-13", nil))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"error"`) {
		t.Fatalf("invalid period status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestJSONExportImportRoundTrip(t *testing.T) {
	a := newTestApplication(t)
	if _, err := a.db.Exec(`INSERT INTO currencies(code,name,is_builtin) VALUES('GBP','GBP',0)`); err != nil {
		t.Fatal(err)
	}
	var paymentID int64
	if err := a.db.QueryRow(`SELECT id FROM payment_methods WHERE is_builtin=1 LIMIT 1`).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	result, err := a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('백업 구독',1599,'GBP','monthly',10,'2026-08-10',?,'2026-08-01')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if _, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,1599,'GBP','2026-08-01')`, id); err != nil {
		t.Fatal(err)
	}
	exportRecorder := httptest.NewRecorder()
	a.exportData(exportRecorder, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if exportRecorder.Code != http.StatusOK {
		t.Fatalf("export=%d %s", exportRecorder.Code, exportRecorder.Body.String())
	}
	if bytes.Contains(exportRecorder.Body.Bytes(), []byte("password_hash")) {
		t.Fatal("backup exposed password hash")
	}
	var backup dataBackup
	if err := json.Unmarshal(exportRecorder.Body.Bytes(), &backup); err != nil {
		t.Fatal(err)
	}
	if backup.Version != 3 || len(backup.Subscriptions) != 1 || backup.Subscriptions[0].Amount != 1599 {
		t.Fatalf("unexpected backup amount encoding: version=%d subscriptions=%+v", backup.Version, backup.Subscriptions)
	}
	importRecorder := httptest.NewRecorder()
	a.importData(importRecorder, httptest.NewRequest(http.MethodPost, "/api/data/import", bytes.NewReader(exportRecorder.Body.Bytes())))
	if importRecorder.Code != http.StatusOK {
		t.Fatalf("import=%d %s", importRecorder.Code, importRecorder.Body.String())
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE service_name='백업 구독' AND currency='GBP' AND amount=1599`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("subscription was not restored")
	}
}

func TestBackupNotificationCredentialsAreOptional(t *testing.T) {
	a := newTestApplication(t)
	const (
		discordWebhook = "https://discord.com/api/webhooks/123456789012345678/secret_webhook_token"
		telegramToken  = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
		telegramChatID = "-1001234567890"
	)
	if _, err := a.db.Exec(
		`UPDATE notification_channels SET discord_webhook=?,telegram_bot_token=?,telegram_chat_id=? WHERE id=1`,
		discordWebhook,
		telegramToken,
		telegramChatID,
	); err != nil {
		t.Fatal(err)
	}

	excludedRecorder := httptest.NewRecorder()
	a.exportData(excludedRecorder, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if excludedRecorder.Code != http.StatusOK {
		t.Fatalf("excluded export=%d %s", excludedRecorder.Code, excludedRecorder.Body.String())
	}
	for _, secret := range []string{discordWebhook, telegramToken, telegramChatID} {
		if bytes.Contains(excludedRecorder.Body.Bytes(), []byte(secret)) {
			t.Fatalf("default backup exposed notification credential %q", secret)
		}
	}
	var excludedBackup dataBackup
	if err := json.Unmarshal(excludedRecorder.Body.Bytes(), &excludedBackup); err != nil {
		t.Fatal(err)
	}
	if excludedBackup.Version != 3 || excludedBackup.NotificationCredentialsIncluded {
		t.Fatalf("unexpected excluded backup metadata: %+v", excludedBackup)
	}

	const (
		preservedWebhook = "https://discord.com/api/webhooks/987654321098765432/preserved_webhook_token"
		preservedToken   = "987654321:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
		preservedChatID  = "123456789"
	)
	if _, err := a.db.Exec(
		`UPDATE notification_channels SET discord_webhook=?,telegram_bot_token=?,telegram_chat_id=? WHERE id=1`,
		preservedWebhook,
		preservedToken,
		preservedChatID,
	); err != nil {
		t.Fatal(err)
	}
	excludedImport := httptest.NewRecorder()
	a.importData(excludedImport, httptest.NewRequest(http.MethodPost, "/api/data/import", bytes.NewReader(excludedRecorder.Body.Bytes())))
	if excludedImport.Code != http.StatusOK {
		t.Fatalf("excluded import=%d %s", excludedImport.Code, excludedImport.Body.String())
	}
	var currentWebhook, currentToken, currentChatID string
	if err := a.db.QueryRow(`SELECT discord_webhook,telegram_bot_token,telegram_chat_id FROM notification_channels WHERE id=1`).Scan(
		&currentWebhook,
		&currentToken,
		&currentChatID,
	); err != nil {
		t.Fatal(err)
	}
	if currentWebhook != preservedWebhook || currentToken != preservedToken || currentChatID != preservedChatID {
		t.Fatalf("excluded import replaced notification credentials: %q %q %q", currentWebhook, currentToken, currentChatID)
	}

	includedRecorder := httptest.NewRecorder()
	a.exportData(includedRecorder, httptest.NewRequest(http.MethodGet, "/api/data/export?includeNotificationCredentials=true", nil))
	if includedRecorder.Code != http.StatusOK {
		t.Fatalf("included export=%d %s", includedRecorder.Code, includedRecorder.Body.String())
	}
	var includedBackup dataBackup
	if err := json.Unmarshal(includedRecorder.Body.Bytes(), &includedBackup); err != nil {
		t.Fatal(err)
	}
	if !includedBackup.NotificationCredentialsIncluded ||
		includedBackup.Settings.DiscordWebhook != preservedWebhook ||
		includedBackup.Settings.TelegramBotToken != preservedToken ||
		includedBackup.Settings.TelegramChatID != preservedChatID {
		t.Fatalf("notification credentials were not included: %+v", includedBackup.Settings)
	}
	if _, err := a.db.Exec(`UPDATE notification_channels SET discord_webhook='',telegram_bot_token='',telegram_chat_id='' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	includedImport := httptest.NewRecorder()
	a.importData(includedImport, httptest.NewRequest(http.MethodPost, "/api/data/import", bytes.NewReader(includedRecorder.Body.Bytes())))
	if includedImport.Code != http.StatusOK {
		t.Fatalf("included import=%d %s", includedImport.Code, includedImport.Body.String())
	}
	var restoredWebhook, restoredToken, restoredChatID string
	if err := a.db.QueryRow(`SELECT discord_webhook,telegram_bot_token,telegram_chat_id FROM notification_channels WHERE id=1`).Scan(
		&restoredWebhook,
		&restoredToken,
		&restoredChatID,
	); err != nil {
		t.Fatal(err)
	}
	if restoredWebhook != preservedWebhook || restoredToken != preservedToken || restoredChatID != preservedChatID {
		t.Fatalf("included import did not restore notification credentials: %q %q %q", restoredWebhook, restoredToken, restoredChatID)
	}
}

func TestLegacyAmountsMigrateToMinorUnitsOnce(t *testing.T) {
	a := newTestApplication(t)
	var paymentID int64
	if err := a.db.QueryRow(`SELECT id FROM payment_methods WHERE is_builtin=1 LIMIT 1`).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	result, err := a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('리라 구독',125,'TRY','monthly',10,'2026-08-10',?,'2026-08-01')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if _, err := a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,125,'TRY','2026-08-01'); DELETE FROM app_metadata WHERE key='amounts_minor_units_v1'`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	var amount, historyAmount int64
	if err := a.db.QueryRow(`SELECT amount FROM subscriptions WHERE id=?`, id).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT amount FROM subscription_price_history WHERE subscription_id=?`, id).Scan(&historyAmount); err != nil {
		t.Fatal(err)
	}
	if amount != 12500 || historyAmount != 12500 {
		t.Fatalf("legacy amount migrated incorrectly: subscription=%d history=%d", amount, historyAmount)
	}
}

func TestLegacyBackupAmountsAreUpgraded(t *testing.T) {
	oldAmount, newAmount := int64(10), int64(20)
	oldCurrency, newCurrency := "USD", "TRY"
	var backup dataBackup
	backup.Version = 1
	backup.Subscriptions = append(backup.Subscriptions, struct {
		ID                                                                                                                           int64
		ServiceID                                                                                                                    *int64
		ServiceName, Icon, Color, Currency, BillingCycle, BillingAnchor, Category, Memo, Status, StartedAt, CancelledAt, TrialEndsAt string
		Amount                                                                                                                       int64
		BillingDay                                                                                                                   int
		PaymentMethodID                                                                                                              int64
	}{Amount: 25, Currency: "TRY"})
	backup.Activities = append(backup.Activities, struct {
		ID                       int64
		SubscriptionID           *int64
		EventType, ServiceName   string
		OldAmount, NewAmount     *int64
		OldCurrency, NewCurrency *string
		OccurredAt               string
	}{OldAmount: &oldAmount, NewAmount: &newAmount, OldCurrency: &oldCurrency, NewCurrency: &newCurrency})
	upgradeLegacyBackupAmounts(&backup)
	if backup.Subscriptions[0].Amount != 2500 || oldAmount != 1000 || newAmount != 2000 {
		t.Fatalf("legacy backup amounts were not upgraded: subscription=%d old=%d new=%d", backup.Subscriptions[0].Amount, oldAmount, newAmount)
	}
}

func TestMinorUnitMoneyFormatting(t *testing.T) {
	for _, test := range []struct {
		amount   int64
		currency string
		want     string
	}{{12345, "TRY", "₺123.45"}, {14900, "KRW", "₩14,900"}, {12345, "KWD", "KWD 12.345"}} {
		if got := money(test.amount, test.currency); got != test.want {
			t.Fatalf("money(%d, %q)=%q want %q", test.amount, test.currency, got, test.want)
		}
	}
}

func TestImportControlUsesStyledLabel(t *testing.T) {
	source, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		`class="button import-label"`,
		`id="importData"`,
		`type="file"`,
		`aria-label="JSON 백업 가져오기"`,
		`id="includeNotificationCredentials"`,
		`includeNotificationCredentials: String(includeNotificationCredentials)`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("import control is missing %q", want)
		}
	}
	if strings.Contains(text, `#importData").click()`) {
		t.Fatal("import label must not depend on a programmatic file picker click")
	}
}

func TestSubscriptionAmountInputSupportsCurrencyDecimals(t *testing.T) {
	source, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{`inputmode="decimal"`, `amountMinorUnits`, `currencyDigits`, `통화의 소수 자릿수에 맞게 금액을 입력해 주세요.`} {
		if !strings.Contains(text, want) {
			t.Fatalf("amount input is missing %q", want)
		}
	}
}

func TestDashboardNavigationAndPresentation(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsSource)
	for _, want := range []string{
		`class="back-button"`,
		`data-view="dashboard"`,
		`aria-label="대시보드로 돌아가기"`,
		`<path d="m15 18-6-6 6-6"/>`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("dashboard back button is missing %q", want)
		}
	}

	cssSource, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := compactSource(string(cssSource))
	for _, rule := range []string{".main:focus{outline:none", ".upcoming-row .sub-content{min-width:0;grid-column:1/-1", "#addSubscriptionButton{height:39px", ".skip-status{display:inline-flex", ".sub-card.skipped{opacity:1;border:2px solid", ".button.skip-action{color:"} {
		if !strings.Contains(css, compactSource(rule)) {
			t.Fatalf("missing presentation rule %q", rule)
		}
	}
	if !strings.Contains(js, `이번 달 결제 건너뜀`) || !strings.Contains(js, `skip-status`) || !strings.Contains(js, `button skip-action`) {
		t.Fatal("skipped subscriptions must show an explicit status badge")
	}
	for _, want := range []string{`const followingPayment = (subscription) =>`, `const rowNextPayment = followingPayment(s);`, `dueText(rowNextPayment)`} {
		if !strings.Contains(js, want) {
			t.Fatalf("skipped subscription next-payment presentation is missing %q", want)
		}
	}
	if strings.Contains(css, ".list-row.skipped") || strings.Contains(js, `["list-row", s.Skipped && "skipped"`) {
		t.Fatal("only dashboard cards should receive the red skipped-payment treatment")
	}

	htmlSource, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlSource)
	if !strings.Contains(html, "<title>SubManager</title>") || strings.Contains(html, "나의 구독 관리") {
		t.Fatal("page title must contain only SubManager")
	}
	for _, want := range []string{`id="themeButton"`, `dataset.themePreference=preference`, `submanager-theme`, `prefers-color-scheme: dark`} {
		if !strings.Contains(html, want) && !strings.Contains(js, want) {
			t.Fatalf("theme controls must contain %q", want)
		}
	}
	if !strings.Contains(css, `:root[data-theme=light]`) || !strings.Contains(js, `themeMedia.addEventListener("change"`) {
		t.Fatal("light and system theme behavior must be defined")
	}
	if strings.Contains(html, `>×</button>`) || strings.Contains(html, `<span>+</span>`) {
		t.Fatal("header and modal action icons must use SVG")
	}
	if !strings.Contains(html, `href="/assets/app.css?v=20260821-upcoming-grid"`) || !strings.Contains(html, `src="/assets/app.js?v=20260821-upcoming-grid"`) {
		t.Fatal("dashboard assets must use the current cache version")
	}
	authSource, err := webFS.ReadFile("web/auth.html")
	if err != nil {
		t.Fatal(err)
	}
	auth := string(authSource)
	if !strings.Contains(auth, `href="/assets/app.css?v=20260821-upcoming-grid"`) {
		t.Fatal("authentication stylesheet must use the current cache version")
	}
	for _, want := range []string{`name="setupToken"`, `minlength="48" maxlength="48"`, `cat /data/.submanager-setup-token`} {
		if !strings.Contains(auth, want) {
			t.Fatalf("setup token instructions are missing %q", want)
		}
	}
}

func TestUpcomingCalendarSourceIncludesAccessibleMonthlyView(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsSource)
	for _, want := range []string{
		`let upcomingView = "list"`,
		`data-upcoming-view="calendar"`,
		`/api/upcoming?month=`,
		`data-calendar-move="-1"`,
		`data-calendar-today`,
		`data-calendar-date=`,
		`결제 예정 ${items.length}건`,
		"openModal(`${monthNumber}월 ${day}일 결제 예정`",
		`item.skipped ? "skipped"`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("upcoming calendar is missing %q", want)
		}
	}

	cssSource, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := compactSource(string(cssSource))
	for _, rule := range []string{
		`.calendar-grid{display:grid;grid-template-columns:repeat(7,minmax(0,1fr))`,
		`.calendar-day{position:relative;display:flex;min-width:0;min-height:86px`,
		`.calendar-day.has-payments{cursor:pointer`,
		`.calendar-day.today .calendar-date{background:`,
		`@media(max-width:620px)`,
		`.calendar-day{min-height:58px`,
	} {
		if !strings.Contains(css, compactSource(rule)) {
			t.Fatalf("upcoming calendar styles are missing %q", rule)
		}
	}
}

func TestDashboardResponsiveLayout(t *testing.T) {
	cssSource, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := compactSource(string(cssSource))
	for _, rule := range []string{
		"grid-template-columns:minmax(0,1.45fr)",
		".currency-tabs{max-width:100%;overflow-x:auto",
		".date-group{display:grid;grid-template-columns:repeat(4,minmax(0,1fr))",
		".upcoming-row{display:grid;width:100%;height:128px",
		".date-group{grid-template-columns:repeat(2,minmax(0,1fr))",
		".date-group{grid-template-columns:minmax(0,1fr)",
		"max-height:93dvh",
		"@media(max-width:420px)",
		".form-actions,.edit-actions,.data-actions{display:grid;grid-template-columns:1fr",
	} {
		if !strings.Contains(css, compactSource(rule)) {
			t.Fatalf("responsive dashboard is missing %q", rule)
		}
	}
}

func TestSubscriptionSearchAndCategoryFilters(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsSource)
	compactJS := compactSource(js)
	for _, want := range []string{
		`id="subscriptionSearch"`,
		`type="search"`,
		`data-sub-category=`,
		`aria-label="카테고리 필터"`,
		`document.addEventListener("input"`,
		`[s.ServiceName,category,s.PaymentMethodName,s.Memo]`,
		`renderSubscriptionResults()`,
	} {
		if !strings.Contains(compactJS, compactSource(want)) {
			t.Fatalf("subscription filtering is missing %q", want)
		}
	}
	if strings.Contains(js, `id="subscriptionSearchButton"`) {
		t.Fatal("subscription search must update without a search button")
	}

	cssSource, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := compactSource(string(cssSource))
	for _, want := range []string{".subscription-tools{display:grid", ".category-filters{display:flex", ".category-filters button[aria-pressed=true]"} {
		if !strings.Contains(css, compactSource(want)) {
			t.Fatalf("subscription filter styles are missing %q", want)
		}
	}
}

func TestAccountSettingsControls(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsSource)
	compactJS := compactSource(js)
	for _, want := range []string{
		`["account","계정"]`,
		`id="emailChangeForm"`,
		`id="passwordChangeForm"`,
		`autocomplete="current-password"`,
		`autocomplete="new-password"`,
		`/api/account/email`,
		`/api/account/password`,
		`새 비밀번호 확인이 일치하지 않아요.`,
		`new Set(["profile", "notifications", "channels"])`,
		`saveArea.hidden = !tabsUsingSettingsSave.has(b.dataset.tab)`,
	} {
		if !strings.Contains(compactJS, compactSource(want)) {
			t.Fatalf("account settings are missing %q", want)
		}
	}
}

func TestSessionManagementControls(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	compactJS := compactSource(string(jsSource))
	for _, want := range []string{
		`["sessions", "세션 관리"]`,
		`<h3>현재 세션</h3>`,
		`<h3>등록된 세션</h3>`,
		`id="endAllSessions"`,
		`data-end-session`,
		`api("/api/sessions")`,
		`method: "DELETE"`,
	} {
		if !strings.Contains(compactJS, compactSource(want)) {
			t.Fatalf("session management controls are missing %q", want)
		}
	}
}

func TestTimezoneSettingIsNotRendered(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsSource)
	for _, unwanted := range []string{"Timezone", `name="timezone"`, "state.user.Timezone"} {
		if strings.Contains(js, unwanted) {
			t.Fatalf("runtime timezone must not appear in settings: %q", unwanted)
		}
	}
}

func TestModalFocusManagement(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsSource)
	for _, want := range []string{
		`modalReturnFocus = document.activeElement`,
		`region.inert = true`,
		`region.inert = false`,
		`modalReturnFocus.focus({ preventScroll: true })`,
		`function trapModalFocus(event)`,
		`e.key === "Tab" && !backdrop.hidden`,
		`event.shiftKey && document.activeElement === first`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("modal focus management is missing %q", want)
		}
	}

	htmlSource, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlSource)
	if !strings.Contains(html, `role="dialog" aria-modal="true"`) ||
		!strings.Contains(html, `aria-labelledby="modalTitle" tabindex="-1"`) {
		t.Fatal("modal must expose dialog semantics and a fallback focus target")
	}
}

func TestUpcomingNotificationItemsIncludePrices(t *testing.T) {
	items := []string{
		upcomingNotificationItem("Discord Nitro", 1000, "USD"),
		upcomingNotificationItem("네이버플러스 멤버십", 4900, "KRW"),
	}
	if items[0] != "Discord Nitro ($10.00)" {
		t.Fatalf("unexpected USD notification item: %q", items[0])
	}
	if items[1] != "네이버플러스 멤버십 (₩4,900)" {
		t.Fatalf("unexpected KRW notification item: %q", items[1])
	}

	notification := upcomingNotification{Days: 3, Items: items}
	for _, want := range items {
		if !strings.Contains(notification.plainText(), "- "+want) {
			t.Fatalf("plain notification is missing %q: %s", want, notification.plainText())
		}
	}
	if !strings.Contains(notification.telegramMarkdown(), `Discord Nitro \($10\.00\)`) {
		t.Fatalf("Telegram notification did not escape the priced item: %s", notification.telegramMarkdown())
	}

	payload := discordWebhookPayload(notification.plainText())
	embeds, ok := payload["embeds"].([]map[string]any)
	if !ok || len(embeds) != 1 || embeds[0]["title"] != "🔔 결제 예정" {
		t.Fatalf("Discord upcoming notification must be an embed: %#v", payload)
	}
}

func TestNotificationDestinationsAreRestricted(t *testing.T) {
	valid := "https://discord.com/api/webhooks/123456789012345678/secret_webhook_token"
	if got, err := validateDiscordWebhook(valid); err != nil || got != valid {
		t.Fatalf("valid Discord webhook rejected: got=%q err=%v", got, err)
	}
	for _, value := range []string{
		"http://discord.com/api/webhooks/123/token",
		"https://127.0.0.1/api/webhooks/123/token",
		"https://discord.com.evil.example/api/webhooks/123/token",
		"https://discord.com/api/webhooks/123/token?redirect=http://127.0.0.1",
	} {
		if _, err := validateDiscordWebhook(value); err == nil {
			t.Fatalf("unsafe Discord webhook accepted: %q", value)
		}
	}
	if err := validateTelegramCredentials("not-a-token", "1234"); err == nil {
		t.Fatal("invalid Telegram token was accepted")
	}
}

func TestNotificationErrorsDoNotExposeTelegramToken(t *testing.T) {
	a := newTestApplication(t)
	const token = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
	a.notificationHTTPClient = &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed for " + request.URL.String())
		}),
	}
	request, recorder := jsonRequest(t, http.MethodPost, "/api/notifications/test", map[string]string{
		"channel": "telegram", "telegramBotToken": token, "telegramChatID": "123456789",
	})
	a.testNotification(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("notification test status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), token) {
		t.Fatalf("Telegram token leaked in error response: %s", recorder.Body.String())
	}
}
