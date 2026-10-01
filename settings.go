package main

import (
	"database/sql"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

func (a *application) updateSettings(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Name, Currency, DiscordWebhook, TelegramBotToken, TelegramChatID string
		NotifyDays                                                       int
		NotifyUpcoming, NotifyChanges, NotifyMonthly                     bool
		DiscordEnabled, TelegramEnabled, PWAEnabled                      bool
	}
	if !decode(w, r, &v) {
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		bad(w, "이름을 입력해 주세요")
		return
	}
	if v.NotifyDays < 0 || v.NotifyDays > 30 {
		bad(w, "알림 날짜는 0~30일 사이로 입력해 주세요")
		return
	}
	var err error
	v.DiscordWebhook, err = validateDiscordWebhook(v.DiscordWebhook)
	if err != nil {
		bad(w, "Discord Webhook 주소를 확인해 주세요")
		return
	}
	v.TelegramBotToken = strings.TrimSpace(v.TelegramBotToken)
	v.TelegramChatID = strings.TrimSpace(v.TelegramChatID)
	if err := validateTelegramCredentials(v.TelegramBotToken, v.TelegramChatID); err != nil {
		bad(w, "Telegram Bot Token과 Chat ID를 확인해 주세요")
		return
	}
	v.Currency = strings.ToUpper(strings.TrimSpace(v.Currency))
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	var currencyCount int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM currencies WHERE code=? AND archived=0`, v.Currency).Scan(&currencyCount); err != nil {
		a.fail(w, err)
		return
	}
	if currencyCount != 1 {
		bad(w, "기본 통화를 확인해 주세요")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE users SET name=?,currency=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, v.Name, v.Currency); err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE notification_channels SET discord_webhook=?,discord_enabled=?,telegram_bot_token=?,telegram_chat_id=?,telegram_enabled=?,pwa_enabled=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, strings.TrimSpace(v.DiscordWebhook), v.DiscordEnabled, strings.TrimSpace(v.TelegramBotToken), strings.TrimSpace(v.TelegramChatID), v.TelegramEnabled, v.PWAEnabled)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE notification_rules SET notify_upcoming=?,notify_changes=?,notify_monthly=?,days_before=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, v.NotifyUpcoming, v.NotifyChanges, v.NotifyMonthly, v.NotifyDays)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *application) updateAccountEmail(w http.ResponseWriter, r *http.Request) {
	userID, ok := a.authenticatedUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요해요"})
		return
	}
	var v struct {
		Email, CurrentPassword string
	}
	if !decode(w, r, &v) || !validOrError(w, validateEmail(v.Email)) {
		return
	}
	var hash string
	if err := a.db.QueryRow(`SELECT password_hash FROM users WHERE id=?`, userID).Scan(&hash); err != nil {
		a.fail(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(v.CurrentPassword)) != nil {
		bad(w, "현재 비밀번호가 맞지 않아요")
		return
	}
	email := strings.ToLower(strings.TrimSpace(v.Email))
	if _, err := a.db.Exec(`UPDATE users SET email=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, email, userID); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *application) updateAccountPassword(w http.ResponseWriter, r *http.Request) {
	userID, ok := a.authenticatedUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요해요"})
		return
	}
	var v struct {
		CurrentPassword, NewPassword string
	}
	if !decode(w, r, &v) || !validOrError(w, validatePassword(v.NewPassword)) {
		return
	}
	var currentHash string
	if err := a.db.QueryRow(`SELECT password_hash FROM users WHERE id=?`, userID).Scan(&currentHash); err != nil {
		a.fail(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(v.CurrentPassword)) != nil {
		bad(w, "현재 비밀번호가 맞지 않아요")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(v.NewPassword)) == nil {
		bad(w, "새 비밀번호는 현재 비밀번호와 다르게 입력해 주세요")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(v.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		a.fail(w, err)
		return
	}
	token, tokenHash, expires, err := newSessionCredentials()
	if err != nil {
		a.fail(w, err)
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE users SET password_hash=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(newHash), userID); err == nil {
		_, err = tx.Exec(`DELETE FROM sessions WHERE user_id=?`, userID)
	}
	if err == nil {
		userAgent := strings.TrimSpace(r.UserAgent())
		if len(userAgent) > 512 {
			userAgent = userAgent[:512]
		}
		_, err = tx.Exec(`INSERT INTO sessions(user_id,token_hash,expires_at,created_at,user_agent) VALUES(?,?,?,?,?)`, userID, tokenHash, expires.Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), userAgent)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *application) createPaymentMethod(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Name string
	}
	if !decode(w, r, &v) {
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		bad(w, "결제수단 이름을 입력해 주세요")
		return
	}
	res, err := a.db.ExecContext(r.Context(), `INSERT INTO payment_methods(name,type,is_builtin) VALUES(?,'custom',0)`, v.Name)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			bad(w, "이미 같은 결제수단이 있어요")
			return
		}
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}
func (a *application) updatePaymentMethod(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var v struct{ Name string }
	if !decode(w, r, &v) {
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		bad(w, "이름을 입력해 주세요")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	var builtin bool
	if err := tx.QueryRowContext(r.Context(), `SELECT is_builtin FROM payment_methods WHERE id=? AND archived=0`, id).Scan(&builtin); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	if builtin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "기본 결제수단은 변경할 수 없어요"})
		return
	}
	res, err := tx.ExecContext(r.Context(), `UPDATE payment_methods SET name=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, v.Name, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			bad(w, "이미 같은 결제수단이 있어요")
			return
		}
		a.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	changed(w, res)
}

func (a *application) deletePaymentMethod(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	var builtin bool
	if err := tx.QueryRowContext(r.Context(), `SELECT is_builtin FROM payment_methods WHERE id=? AND archived=0`, id).Scan(&builtin); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	if builtin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "기본 결제수단은 삭제할 수 없어요"})
		return
	}
	var used bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM subscriptions WHERE payment_method_id=?)`, id).Scan(&used); err != nil {
		a.fail(w, err)
		return
	}
	var res sql.Result
	if used {
		res, err = tx.ExecContext(r.Context(), `UPDATE payment_methods SET archived=1,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	} else {
		res, err = tx.ExecContext(r.Context(), `DELETE FROM payment_methods WHERE id=?`, id)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	changed(w, res)
}

var currencyCodePattern = regexp.MustCompile(`^[A-Z]{3}$`)

func (a *application) createCurrency(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Code string
	}
	if !decode(w, r, &v) {
		return
	}
	v.Code = strings.ToUpper(strings.TrimSpace(v.Code))
	if !currencyCodePattern.MatchString(v.Code) {
		bad(w, "통화 코드는 영문 3자리로 입력해 주세요")
		return
	}
	res, err := a.db.ExecContext(r.Context(), `INSERT INTO currencies(code,name,is_builtin) VALUES(?,?,0)`, v.Code, v.Code)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			bad(w, "이미 등록된 통화예요")
			return
		}
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "code": v.Code})
}

func (a *application) deleteCurrency(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	var code string
	var builtin bool
	if err := tx.QueryRowContext(r.Context(), `SELECT code,is_builtin FROM currencies WHERE id=? AND archived=0`, id).Scan(&code, &builtin); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	if builtin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "기본 통화는 삭제할 수 없어요"})
		return
	}
	var defaultCurrency bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE currency=?)`, code).Scan(&defaultCurrency); err != nil {
		a.fail(w, err)
		return
	}
	if defaultCurrency {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "기본 통화를 먼저 변경해 주세요"})
		return
	}
	// Currency references also exist outside the current subscription price.
	var used bool
	if err := tx.QueryRowContext(r.Context(), `SELECT
		EXISTS(SELECT 1 FROM subscriptions WHERE currency=?) OR
		EXISTS(SELECT 1 FROM subscription_price_history WHERE currency=?) OR
		EXISTS(SELECT 1 FROM subscription_occurrences WHERE currency=?) OR
		EXISTS(SELECT 1 FROM activity_events WHERE old_currency=? OR new_currency=?) OR
		EXISTS(SELECT 1 FROM services WHERE default_currency=?)`, code, code, code, code, code, code).Scan(&used); err != nil {
		a.fail(w, err)
		return
	}
	var res sql.Result
	if used {
		res, err = tx.ExecContext(r.Context(), `UPDATE currencies SET archived=1,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	} else {
		res, err = tx.ExecContext(r.Context(), `DELETE FROM currencies WHERE id=?`, id)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	changed(w, res)
}
