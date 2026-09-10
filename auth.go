package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
)

func (a *application) accountExists() (bool, error) {
	var count int
	err := a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=1 AND password_hash<>''`).Scan(&count)
	return count == 1, err
}

type authInput struct {
	Name, Email, Password, SetupToken string
}

var dummyPasswordHash = func() []byte {
	hash, err := bcrypt.GenerateFromPassword([]byte("submanager-dummy-password"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return hash
}()

func validateAuth(v authInput, setup bool) error {
	if setup && (strings.TrimSpace(v.Name) == "" || len([]rune(strings.TrimSpace(v.Name))) > 50) {
		return errors.New("이름을 확인해 주세요")
	}
	if err := validateEmail(v.Email); err != nil {
		return err
	}
	return validatePassword(v.Password)
}

func validateEmail(email string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return errors.New("이메일을 확인해 주세요")
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("비밀번호는 8자 이상 입력해 주세요")
	}
	if len(password) > 72 {
		return errors.New("비밀번호는 72자 이하로 입력해 주세요")
	}
	return nil
}

func (a *application) setupAccount(w http.ResponseWriter, r *http.Request) {
	var v authInput
	if !decode(w, r, &v) || !validOrError(w, validateAuth(v, true)) {
		return
	}
	keys := authAttemptKeys(r, v.Email, "setup")
	if !a.authLimiter.allowed(keys...) {
		authRateLimited(w)
		return
	}
	exists, err := a.accountExists()
	if err != nil {
		a.fail(w, err)
		return
	}
	if exists {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "관리자 계정이 이미 설정되어 있어요"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(v.SetupToken), []byte(a.setupToken)) != 1 {
		a.authLimiter.failure(keys...)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "최초 설정 키를 확인해 주세요"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(v.Password), bcrypt.DefaultCost)
	if err != nil {
		a.fail(w, err)
		return
	}
	res, err := a.db.Exec(`UPDATE users SET name=?,email=?,password_hash=?,is_admin=1,updated_at=CURRENT_TIMESTAMP WHERE id=1 AND password_hash=''`, strings.TrimSpace(v.Name), strings.ToLower(strings.TrimSpace(v.Email)), string(hash))
	if err != nil {
		a.fail(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "관리자 계정이 이미 설정되어 있어요"})
		return
	}
	if err := a.createSession(w, r, 1); err != nil {
		a.fail(w, err)
		return
	}
	if a.setupTokenPath != "" {
		if err := os.Remove(a.setupTokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("remove setup token file: %v", err)
		}
	}
	a.authLimiter.reset(keys...)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (a *application) login(w http.ResponseWriter, r *http.Request) {
	var v authInput
	if !decode(w, r, &v) {
		return
	}
	keys := authAttemptKeys(r, v.Email, "login")
	if !a.authLimiter.allowed(keys...) {
		authRateLimited(w)
		return
	}
	var id int64
	hash := string(dummyPasswordHash)
	err := a.db.QueryRow(`SELECT id,password_hash FROM users WHERE email=? AND password_hash<>''`, strings.ToLower(strings.TrimSpace(v.Email))).Scan(&id, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.fail(w, err)
		return
	}
	passwordMatches := bcrypt.CompareHashAndPassword([]byte(hash), []byte(v.Password)) == nil
	if errors.Is(err, sql.ErrNoRows) || !passwordMatches {
		a.authLimiter.failure(keys...)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "이메일 또는 비밀번호가 맞지 않아요"})
		return
	}
	if err := a.createSession(w, r, id); err != nil {
		a.fail(w, err)
		return
	}
	a.authLimiter.reset(keys...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func authAttemptKeys(r *http.Request, email, scope string) []string {
	host := r.RemoteAddr
	if parsed, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = parsed
	}
	emailHash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return []string{
		scope + ":ip:" + host,
		scope + ":email:" + hex.EncodeToString(emailHash[:]),
	}
}

func authRateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(authAttemptWindow/time.Second)))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "로그인 시도가 너무 많아요. 잠시 후 다시 시도해 주세요"})
}

func (a *application) createSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, tokenHash, expires, err := newSessionCredentials()
	if err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM sessions WHERE expires_at<=?`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`DELETE FROM sessions WHERE user_id=? AND id NOT IN (SELECT id FROM sessions WHERE user_id=? ORDER BY created_at DESC,id DESC LIMIT ?)`,
		userID,
		userID,
		maxActiveSessions-1,
	); err != nil {
		return err
	}
	userAgent := strings.TrimSpace(r.UserAgent())
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	if _, err := tx.Exec(`INSERT INTO sessions(user_id,token_hash,expires_at,created_at,user_agent) VALUES(?,?,?,?,?)`, userID, tokenHash, expires.Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), userAgent); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	setSessionCookie(w, r, token, expires)
	return nil
}

func newSessionCredentials() (string, string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", time.Time{}, err
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	expires := time.Now().UTC().Add(30 * 24 * time.Hour)
	return token, hex.EncodeToString(sum[:]), expires, nil
}

func createSetupTokenFile(path string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: "submanager_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 30 * 24 * 60 * 60})
}

func (a *application) authenticatedUser(r *http.Request) (int64, bool) {
	_, userID, ok := a.authenticatedSession(r)
	return userID, ok
}

func (a *application) authenticatedSession(r *http.Request) (int64, int64, bool) {
	c, err := r.Cookie("submanager_session")
	if err != nil || len(c.Value) != 64 {
		return 0, 0, false
	}
	sum := sha256.Sum256([]byte(c.Value))
	var sessionID, userID int64
	err = a.db.QueryRow(`SELECT id,user_id FROM sessions WHERE token_hash=? AND expires_at>?`, hex.EncodeToString(sum[:]), time.Now().UTC().Format(time.RFC3339)).Scan(&sessionID, &userID)
	return sessionID, userID, err == nil
}

func (a *application) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.authenticatedUser(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요해요"})
			return
		}
		next(w, r)
	}
}

func (a *application) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("submanager_session"); err == nil {
		sum := sha256.Sum256([]byte(c.Value))
		_, _ = a.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hex.EncodeToString(sum[:]))
	}
	http.SetCookie(w, &http.Cookie{Name: "submanager_session", Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *application) listSessions(w http.ResponseWriter, r *http.Request) {
	currentID, userID, ok := a.authenticatedSession(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요해요"})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = a.db.Exec(`DELETE FROM sessions WHERE expires_at<=?`, now)
	rows, err := a.db.Query(`SELECT id,user_agent,created_at,expires_at FROM sessions WHERE user_id=? AND expires_at>? ORDER BY created_at DESC,id DESC`, userID, now)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	response := struct {
		Current    sessionState   `json:"current"`
		Registered []sessionState `json:"registered"`
	}{Registered: []sessionState{}}
	for rows.Next() {
		var session sessionState
		var userAgent, createdAt, expiresAt string
		if err := rows.Scan(&session.ID, &userAgent, &createdAt, &expiresAt); err != nil {
			a.fail(w, err)
			return
		}
		session.Device = sessionDeviceName(userAgent)
		session.CreatedAt = a.sessionTimeLabel(createdAt)
		session.ExpiresAt = a.sessionTimeLabel(expiresAt)
		if session.ID == currentID {
			response.Current = session
		} else {
			response.Registered = append(response.Registered, session)
		}
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *application) deleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	currentID, userID, authenticated := a.authenticatedSession(r)
	if !authenticated {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요해요"})
		return
	}
	if id == currentID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "현재 세션은 여기에서 종료할 수 없어요"})
		return
	}
	res, err := a.db.Exec(`DELETE FROM sessions WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		a.fail(w, err)
		return
	}
	changed(w, res)
}

func (a *application) deleteOtherSessions(w http.ResponseWriter, r *http.Request) {
	currentID, userID, ok := a.authenticatedSession(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요해요"})
		return
	}
	if _, err := a.db.Exec(`DELETE FROM sessions WHERE user_id=? AND id<>?`, userID, currentID); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func sessionDeviceName(userAgent string) string {
	if userAgent == "" {
		return "알 수 없는 기기"
	}
	lower := strings.ToLower(userAgent)
	browser := "브라우저"
	switch {
	case strings.Contains(lower, "edg/"):
		browser = "Microsoft Edge"
	case strings.Contains(lower, "firefox/"):
		browser = "Firefox"
	case strings.Contains(lower, "chrome/") || strings.Contains(lower, "crios/"):
		browser = "Chrome"
	case strings.Contains(lower, "safari/"):
		browser = "Safari"
	}
	device := "기기"
	switch {
	case strings.Contains(lower, "iphone"):
		device = "iPhone"
	case strings.Contains(lower, "ipad"):
		device = "iPad"
	case strings.Contains(lower, "android"):
		device = "Android"
	case strings.Contains(lower, "windows"):
		device = "Windows"
	case strings.Contains(lower, "macintosh") || strings.Contains(lower, "mac os"):
		device = "Mac"
	case strings.Contains(lower, "linux"):
		device = "Linux"
	}
	return browser + " · " + device
}

func (a *application) sessionTimeLabel(value string) string {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.In(a.location).Format("2006. 01. 02. 15:04")
		}
	}
	return value
}
