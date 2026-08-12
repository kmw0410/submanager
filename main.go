package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

//go:embed web/*
var webFS embed.FS

type application struct {
	db       *sql.DB
	tpl      *template.Template
	authTpl  *template.Template
	location *time.Location
}

type service struct {
	ID                                                  int64
	Name, Icon, Category, BillingCycle, Currency, Color string
	SupportsTrial                                       bool
}

type paymentMethod struct {
	ID                  int64  `json:"id"`
	Name                string `json:"name"`
	IsBuiltin, Archived bool
}

type currencyOption struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Digits    int    `json:"digits"`
	IsBuiltin bool   `json:"isBuiltin"`
	Archived  bool   `json:"archived"`
}

type subscription struct {
	ID                                                                                          int64  `json:"id"`
	ServiceID                                                                                   *int64 `json:"serviceId,omitempty"`
	ServiceName, Icon, Color, Category, Currency, BillingCycle, PaymentMethodName, Status, Memo string
	Amount                                                                                      int64 `json:"amount"`
	BillingDay                                                                                  int   `json:"billingDay"`
	PaymentMethodID                                                                             int64 `json:"paymentMethodId"`
	NextPayment, CreatedAt, CancelledAt                                                         string
	BillingDate                                                                                 string `json:"billingDate"`
	TrialEndsAt                                                                                 string `json:"trialEndsAt"`
	IsTrial                                                                                     bool   `json:"isTrial"`
	Skipped                                                                                     bool   `json:"skipped"`
}

type currencyStat struct {
	Currency      string  `json:"currency"`
	MonthTotal    int64   `json:"monthTotal"`
	YearEstimate  int64   `json:"yearEstimate"`
	PreviousTotal int64   `json:"previousTotal"`
	Delta         int64   `json:"delta"`
	MonthlyTotals []int64 `json:"monthlyTotals"`
}

type userState struct {
	Name     string
	Email    string
	Currency string
}

type settingsState struct {
	NotifyDays       int
	DiscordWebhook   string
	TelegramBotToken string
	TelegramChatID   string
	NotifyUpcoming   bool
	NotifyChanges    bool
	NotifyMonthly    bool
}

type dashboardStats struct {
	ActiveCount   int
	UpcomingCount int
	Months        []string       `json:"months"`
	Currencies    []currencyStat `json:"currencies"`
	Greeting      string
	Summary       string
}

type appState struct {
	User           userState        `json:"user"`
	Settings       settingsState    `json:"settings"`
	Services       []service        `json:"services"`
	PaymentMethods []paymentMethod  `json:"paymentMethods"`
	Currencies     []currencyOption `json:"currencies"`
	Subscriptions  []subscription   `json:"subscriptions"`
	Stats          dashboardStats   `json:"stats"`
}

func main() {
	dbPath := env("DB_PATH", "./data/submanager.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatal(err)
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
	app := &application{db: db, location: loc}
	app.tpl = template.Must(template.New("index.html").ParseFS(webFS, "web/index.html"))
	app.authTpl = template.Must(template.New("auth.html").ParseFS(webFS, "web/auth.html"))
	if err := app.migrate(); err != nil {
		log.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	go app.notificationLoop(workerCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", app.index)
	mux.HandleFunc("GET /assets/app.css", serveEmbedded("web/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /assets/app.js", serveEmbedded("web/app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("POST /auth/setup", app.setupAccount)
	mux.HandleFunc("POST /auth/login", app.login)
	mux.HandleFunc("POST /auth/logout", app.requireAuth(app.logout))
	mux.HandleFunc("GET /api/state", app.requireAuth(app.getState))
	mux.HandleFunc("POST /api/subscriptions", app.requireAuth(app.createSubscription))
	mux.HandleFunc("PUT /api/subscriptions/{id}", app.requireAuth(app.updateSubscription))
	mux.HandleFunc("POST /api/subscriptions/{id}/skip", app.requireAuth(app.skipSubscription))
	mux.HandleFunc("POST /api/subscriptions/{id}/cancel", app.requireAuth(app.cancelSubscription))
	mux.HandleFunc("PUT /api/settings", app.requireAuth(app.updateSettings))
	mux.HandleFunc("PUT /api/account/email", app.requireAuth(app.updateAccountEmail))
	mux.HandleFunc("PUT /api/account/password", app.requireAuth(app.updateAccountPassword))
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

func (a *application) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY CHECK(id=1),
    name TEXT NOT NULL DEFAULT '사용자',
    currency TEXT NOT NULL DEFAULT 'KRW',
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS services (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    icon TEXT NOT NULL,
    default_category TEXT NOT NULL DEFAULT '',
    default_billing_cycle TEXT NOT NULL DEFAULT 'monthly',
    default_currency TEXT NOT NULL DEFAULT 'KRW',
    color TEXT NOT NULL DEFAULT '#9AB8A8',
    is_builtin INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS payment_methods (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    type TEXT NOT NULL DEFAULT 'custom',
    is_builtin INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS currencies (
    id INTEGER PRIMARY KEY,
    code TEXT NOT NULL COLLATE NOCASE UNIQUE,
    name TEXT NOT NULL DEFAULT '',
    is_builtin INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS categories (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    color TEXT NOT NULL DEFAULT '#9AB8A8'
);
CREATE TABLE IF NOT EXISTS subscriptions (
    id INTEGER PRIMARY KEY,
    service_id INTEGER REFERENCES services(id),
    service_name TEXT NOT NULL,
    icon TEXT NOT NULL DEFAULT 'S',
    color TEXT NOT NULL DEFAULT '#9AB8A8',
    amount INTEGER NOT NULL CHECK(amount >= 0),
    currency TEXT NOT NULL DEFAULT 'KRW',
    billing_cycle TEXT NOT NULL CHECK(billing_cycle IN ('monthly','yearly')),
    billing_day INTEGER NOT NULL CHECK(billing_day BETWEEN 1 AND 31),
    billing_anchor TEXT NOT NULL DEFAULT '',
    payment_method_id INTEGER NOT NULL REFERENCES payment_methods(id),
    category TEXT NOT NULL DEFAULT '',
    memo TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','cancelled')),
    started_at TEXT NOT NULL DEFAULT CURRENT_DATE,
    cancelled_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS subscription_occurrences (
    id INTEGER PRIMARY KEY,
    subscription_id INTEGER NOT NULL REFERENCES subscriptions(id),
    period TEXT NOT NULL,
    scheduled_date TEXT NOT NULL,
    amount INTEGER NOT NULL,
    skipped INTEGER NOT NULL DEFAULT 0,
    paid INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(subscription_id, period)
);
CREATE TABLE IF NOT EXISTS subscription_price_history (
    id INTEGER PRIMARY KEY,
    subscription_id INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    amount INTEGER NOT NULL CHECK(amount >= 0),
    currency TEXT NOT NULL,
    effective_from TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_price_history_subscription_date
    ON subscription_price_history(subscription_id,effective_from,id);
CREATE TABLE IF NOT EXISTS activity_events (
    id INTEGER PRIMARY KEY,
    subscription_id INTEGER REFERENCES subscriptions(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    service_name TEXT NOT NULL,
    old_amount INTEGER,
    old_currency TEXT,
    new_amount INTEGER,
    new_currency TEXT,
    occurred_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_activity_events_date
    ON activity_events(occurred_at,id);
CREATE TABLE IF NOT EXISTS notification_channels (
    id INTEGER PRIMARY KEY CHECK(id=1),
    discord_webhook TEXT NOT NULL DEFAULT '',
    telegram_bot_token TEXT NOT NULL DEFAULT '',
    telegram_chat_id TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS notification_rules (
    id INTEGER PRIMARY KEY CHECK(id=1),
    notify_upcoming INTEGER NOT NULL DEFAULT 1,
    notify_changes INTEGER NOT NULL DEFAULT 1,
    notify_monthly INTEGER NOT NULL DEFAULT 1,
    days_before INTEGER NOT NULL DEFAULT 3,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS notification_deliveries (
    id INTEGER PRIMARY KEY,
    delivery_key TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS app_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT OR IGNORE INTO users(id,name,currency) VALUES(1,'사용자','KRW');
INSERT OR IGNORE INTO notification_channels(id) VALUES(1);
INSERT OR IGNORE INTO notification_rules(id) VALUES(1);
`
	if _, err := a.db.Exec(schema); err != nil {
		return err
	}
	if err := a.ensureColumn("subscriptions", "billing_anchor", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := a.ensureColumn("subscriptions", "trial_ends_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := a.ensureColumn("services", "supports_trial", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := a.ensureColumn("subscription_occurrences", "currency", "TEXT NOT NULL DEFAULT 'KRW'"); err != nil {
		return err
	}
	columns := []struct {
		name       string
		definition string
	}{
		{"email", "TEXT NOT NULL DEFAULT ''"},
		{"password_hash", "TEXT NOT NULL DEFAULT ''"},
		{"is_admin", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		if err := a.ensureColumn("users", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := a.migrateAmountsToMinorUnits(); err != nil {
		return err
	}
	services := []service{
		{Name: "네이버플러스 멤버십", Icon: "N+", Category: "생활", BillingCycle: "monthly", Currency: "KRW", Color: "#8FC9A3"},
		{Name: "YouTube Premium", Icon: "YT", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#D99A9A"},
		{Name: "ChatGPT", Icon: "AI", Category: "AI", BillingCycle: "monthly", Currency: "KRW", Color: "#91B8A7"},
		{Name: "Claude", Icon: "CL", Category: "AI", BillingCycle: "monthly", Currency: "KRW", Color: "#C6A98D"},
		{Name: "Spotify", Icon: "SP", Category: "음악", BillingCycle: "monthly", Currency: "KRW", Color: "#91C89C"},
		{Name: "벅스", Icon: "BG", Category: "음악", BillingCycle: "monthly", Currency: "KRW", Color: "#B2A7D6"},
		{Name: "멜론", Icon: "ML", Category: "음악", BillingCycle: "monthly", Currency: "KRW", Color: "#A6C99A"},
		{Name: "FLO", Icon: "FL", Category: "음악", BillingCycle: "monthly", Currency: "KRW", Color: "#AAB5D8"},
		{Name: "Netflix", Icon: "NF", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#D99191"},
		{Name: "TVING", Icon: "TV", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#D9879B"},
		{Name: "Wavve", Icon: "WV", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#829FD1"},
		{Name: "Disney+", Icon: "D+", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#879BC8"},
		{Name: "WATCHA", Icon: "WA", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#D98DAA"},
		{Name: "iCloud+", Icon: "iC", Category: "클라우드", BillingCycle: "monthly", Currency: "KRW", Color: "#8CB8D5"},
		{Name: "Google One", Icon: "G1", Category: "클라우드", BillingCycle: "monthly", Currency: "KRW", Color: "#8EB79D"},
		{Name: "쿠팡 와우 멤버십", Icon: "CW", Category: "생활", BillingCycle: "monthly", Currency: "KRW", Color: "#A593CE"},
		{Name: "배민클럽", Icon: "BM", Category: "생활", BillingCycle: "monthly", Currency: "KRW", Color: "#82C6C4"},
		{Name: "밀리의 서재", Icon: "MI", Category: "독서", BillingCycle: "monthly", Currency: "KRW", Color: "#B4C985"},
	}
	for _, s := range services {
		if _, err := a.db.Exec(`INSERT OR IGNORE INTO services(name,icon,default_category,default_billing_cycle,default_currency,color) VALUES(?,?,?,?,?,?)`, s.Name, s.Icon, s.Category, s.BillingCycle, s.Currency, s.Color); err != nil {
			return err
		}
	}
	for _, name := range []string{"신용(체크) 카드", "휴대폰결제", "계좌이체", "네이버페이", "카카오페이"} {
		if _, err := a.db.Exec(`INSERT OR IGNORE INTO payment_methods(name,type,is_builtin) VALUES(?,'builtin',1)`, name); err != nil {
			return err
		}
	}
	currencies := []struct {
		code string
		name string
	}{
		{"KRW", "대한민국 원"},
		{"USD", "미국 달러"},
		{"JPY", "일본 엔"},
		{"EUR", "유로"},
		{"TRY", "튀르키예 리라"},
		{"ARS", "아르헨티나 페소"},
	}
	for _, currency := range currencies {
		if _, err := a.db.Exec(`INSERT OR IGNORE INTO currencies(code,name,is_builtin) VALUES(?,?,1)`, currency.code, currency.name); err != nil {
			return err
		}
	}
	_, _ = a.db.Exec(`UPDATE services SET supports_trial=1 WHERE name IN ('YouTube Premium','ChatGPT','Claude','Spotify','FLO','밀리의 서재')`)
	_, err := a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) SELECT s.id,s.amount,s.currency,substr(s.started_at,1,10) FROM subscriptions s WHERE NOT EXISTS(SELECT 1 FROM subscription_price_history h WHERE h.subscription_id=s.id)`)
	if err != nil {
		return err
	}
	return nil
}

func (a *application) migrateAmountsToMinorUnits() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM app_metadata WHERE key='amounts_minor_units_v1'`).Scan(&done); err != nil {
		return err
	}
	if done != 0 {
		return tx.Commit()
	}
	rows, err := tx.Query(`SELECT DISTINCT currency FROM (SELECT currency FROM subscriptions UNION SELECT currency FROM subscription_occurrences UNION SELECT currency FROM subscription_price_history UNION SELECT old_currency FROM activity_events UNION SELECT new_currency FROM activity_events) WHERE currency IS NOT NULL AND currency<>''`)
	if err != nil {
		return err
	}
	var currencies []string
	for rows.Next() {
		var currency string
		if err := rows.Scan(&currency); err != nil {
			rows.Close()
			return err
		}
		currencies = append(currencies, strings.ToUpper(currency))
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, currency := range currencies {
		factor := minorUnitFactor(currency)
		if factor == 1 {
			continue
		}
		for _, query := range []string{
			`UPDATE subscriptions SET amount=amount*? WHERE UPPER(currency)=?`,
			`UPDATE subscription_occurrences SET amount=amount*? WHERE UPPER(currency)=?`,
			`UPDATE subscription_price_history SET amount=amount*? WHERE UPPER(currency)=?`,
			`UPDATE activity_events SET old_amount=old_amount*? WHERE old_amount IS NOT NULL AND UPPER(old_currency)=?`,
			`UPDATE activity_events SET new_amount=new_amount*? WHERE new_amount IS NOT NULL AND UPPER(new_currency)=?`,
		} {
			if _, err := tx.Exec(query, factor, currency); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO app_metadata(key,value) VALUES('amounts_minor_units_v1','1')`); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *application) ensureColumn(table, column, definition string) error {
	rows, err := a.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			return rows.Close()
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = a.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
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

func (a *application) accountExists() (bool, error) {
	var count int
	err := a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=1 AND password_hash<>''`).Scan(&count)
	return count == 1, err
}

type authInput struct {
	Name, Email, Password string
}

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
	exists, err := a.accountExists()
	if err != nil {
		a.fail(w, err)
		return
	}
	if exists {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "관리자 계정이 이미 설정되어 있어요"})
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
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (a *application) login(w http.ResponseWriter, r *http.Request) {
	var v authInput
	if !decode(w, r, &v) {
		return
	}
	var id int64
	var hash string
	err := a.db.QueryRow(`SELECT id,password_hash FROM users WHERE email=? AND password_hash<>''`, strings.ToLower(strings.TrimSpace(v.Email))).Scan(&id, &hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(v.Password)) != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "이메일 또는 비밀번호가 맞지 않아요"})
		return
	}
	if err := a.createSession(w, r, id); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *application) createSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, tokenHash, expires, err := newSessionCredentials()
	if err != nil {
		return err
	}
	_, _ = a.db.Exec(`DELETE FROM sessions WHERE expires_at<=?`, time.Now().UTC().Format(time.RFC3339))
	if _, err := a.db.Exec(`INSERT INTO sessions(user_id,token_hash,expires_at) VALUES(?,?,?)`, userID, tokenHash, expires.Format(time.RFC3339)); err != nil {
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

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: "submanager_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 30 * 24 * 60 * 60})
}

func (a *application) authenticatedUser(r *http.Request) (int64, bool) {
	c, err := r.Cookie("submanager_session")
	if err != nil || len(c.Value) != 64 {
		return 0, false
	}
	sum := sha256.Sum256([]byte(c.Value))
	var id int64
	err = a.db.QueryRow(`SELECT user_id FROM sessions WHERE token_hash=? AND expires_at>?`, hex.EncodeToString(sum[:]), time.Now().UTC().Format(time.RFC3339)).Scan(&id)
	return id, err == nil
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
func (a *application) getState(w http.ResponseWriter, _ *http.Request) {
	s, e := a.loadState()
	if e != nil {
		a.fail(w, e)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *application) loadState() (appState, error) {
	var s appState
	s.Services = []service{}
	s.PaymentMethods = []paymentMethod{}
	s.Currencies = []currencyOption{}
	s.Subscriptions = []subscription{}
	s.Stats.Months = []string{}
	s.Stats.Currencies = []currencyStat{}
	stateQuery := `
		SELECT
			u.name,
			u.email,
			u.currency,
			n.days_before,
			c.discord_webhook,
			c.telegram_bot_token,
			c.telegram_chat_id,
			n.notify_upcoming,
			n.notify_changes,
			n.notify_monthly
		FROM users u, notification_rules n, notification_channels c
		WHERE u.id=1 AND n.id=1 AND c.id=1`
	err := a.db.QueryRow(stateQuery).Scan(
		&s.User.Name,
		&s.User.Email,
		&s.User.Currency,
		&s.Settings.NotifyDays,
		&s.Settings.DiscordWebhook,
		&s.Settings.TelegramBotToken,
		&s.Settings.TelegramChatID,
		&s.Settings.NotifyUpcoming,
		&s.Settings.NotifyChanges,
		&s.Settings.NotifyMonthly,
	)
	if err != nil {
		return s, err
	}
	rows, err := a.db.Query(`
		SELECT
			id,
			name,
			icon,
			default_category,
			default_billing_cycle,
			default_currency,
			color,
			supports_trial
		FROM services
		ORDER BY id`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var v service
		if err := rows.Scan(
			&v.ID,
			&v.Name,
			&v.Icon,
			&v.Category,
			&v.BillingCycle,
			&v.Currency,
			&v.Color,
			&v.SupportsTrial,
		); err != nil {
			return s, err
		}
		s.Services = append(s.Services, v)
	}
	pm, err := a.db.Query(`SELECT id,name,is_builtin,archived FROM payment_methods ORDER BY is_builtin DESC,id`)
	if err != nil {
		return s, err
	}
	defer pm.Close()
	for pm.Next() {
		var v paymentMethod
		if err := pm.Scan(&v.ID, &v.Name, &v.IsBuiltin, &v.Archived); err != nil {
			return s, err
		}
		s.PaymentMethods = append(s.PaymentMethods, v)
	}
	currencyRows, err := a.db.Query(`SELECT id,code,name,is_builtin,archived FROM currencies ORDER BY is_builtin DESC,id`)
	if err != nil {
		return s, err
	}
	for currencyRows.Next() {
		var v currencyOption
		if err := currencyRows.Scan(&v.ID, &v.Code, &v.Name, &v.IsBuiltin, &v.Archived); err != nil {
			currencyRows.Close()
			return s, err
		}
		v.Digits = currencyFractionDigits(v.Code)
		s.Currencies = append(s.Currencies, v)
	}
	if err := currencyRows.Close(); err != nil {
		return s, err
	}
	s.Subscriptions, err = a.loadSubscriptions()
	if err != nil {
		return s, err
	}
	if s.Subscriptions == nil {
		s.Subscriptions = []subscription{}
	}
	now := time.Now().In(a.location)
	currencyStats := map[string]*currencyStat{}
	currencyOrder := []string{}
	for i := range s.Subscriptions {
		v := &s.Subscriptions[i]
		if v.TrialEndsAt != "" {
			if end, err := time.ParseInLocation("2006-01-02", v.TrialEndsAt, a.location); err == nil {
				v.IsTrial = now.Before(end.AddDate(0, 0, 1))
			}
		}
		v.NextPayment = nextPayment(now, v.BillingDay, v.BillingCycle, v.BillingDate)
		if v.Status == "active" {
			s.Stats.ActiveCount++
			currency := strings.ToUpper(v.Currency)
			if _, ok := currencyStats[currency]; !ok {
				currencyStats[currency] = &currencyStat{Currency: currency, MonthlyTotals: []int64{}}
				currencyOrder = append(currencyOrder, currency)
			}
			due, _ := time.ParseInLocation("2006-01-02", v.NextPayment, a.location)
			if due.Sub(now) <= 7*24*time.Hour {
				s.Stats.UpcomingCount++
			}
			if v.BillingCycle == "yearly" {
				currencyStats[currency].YearEstimate += v.Amount
			} else {
				currencyStats[currency].YearEstimate += v.Amount * 12
			}
		}
	}
	historyCurrencies, err := a.db.Query(`SELECT DISTINCT UPPER(h.currency) FROM subscription_price_history h JOIN subscriptions s ON s.id=h.subscription_id WHERE s.status='active' ORDER BY 1`)
	if err != nil {
		return s, err
	}
	for historyCurrencies.Next() {
		var currency string
		if err := historyCurrencies.Scan(&currency); err != nil {
			historyCurrencies.Close()
			return s, err
		}
		if _, ok := currencyStats[currency]; !ok {
			currencyStats[currency] = &currencyStat{Currency: currency, MonthlyTotals: []int64{}}
			currencyOrder = append(currencyOrder, currency)
		}
	}
	if err := historyCurrencies.Close(); err != nil {
		return s, err
	}
	sort.SliceStable(currencyOrder, func(i, j int) bool {
		if currencyOrder[i] == currencyOrder[j] {
			return false
		}
		if currencyOrder[i] == s.User.Currency {
			return true
		}
		if currencyOrder[j] == s.User.Currency {
			return false
		}
		return currencyOrder[i] < currencyOrder[j]
	})
	for i := -5; i <= 0; i++ {
		d := now.AddDate(0, i, 0)
		p := d.Format("2006-01")
		s.Stats.Months = append(s.Stats.Months, d.Format("1월"))
		totals, err := a.monthTotals(p)
		if err != nil {
			return s, err
		}
		for _, currency := range currencyOrder {
			stat := currencyStats[currency]
			value := totals[currency]
			stat.MonthlyTotals = append(stat.MonthlyTotals, value)
			if i == -1 {
				stat.PreviousTotal = value
			}
			if i == 0 {
				stat.MonthTotal = value
			}
		}
	}
	for _, currency := range currencyOrder {
		stat := currencyStats[currency]
		stat.Delta = stat.MonthTotal - stat.PreviousTotal
		s.Stats.Currencies = append(s.Stats.Currencies, *stat)
	}
	s.Stats.Greeting = "안녕하세요, " + s.User.Name + "님."
	if summary, ok, err := a.latestActivitySummary(now); err != nil {
		return s, err
	} else if ok {
		s.Stats.Summary = summary
	} else if s.Stats.ActiveCount == 0 {
		s.Stats.Summary = "첫 구독을 추가하면 한눈에 정리해 드릴게요."
	} else if len(s.Stats.Currencies) > 1 {
		s.Stats.Summary = "이번 달 구독비를 통화별로 정리했어요."
	} else if abs(s.Stats.Currencies[0].Delta) < 1 {
		s.Stats.Summary = "이번 달은 저번 달과 비슷해요."
	} else if s.Stats.Currencies[0].Delta > 0 {
		s.Stats.Summary = "이번 달은 저번 달보다 " + money(s.Stats.Currencies[0].Delta, s.Stats.Currencies[0].Currency) + " 더 나가요."
	} else {
		s.Stats.Summary = "이번 달은 저번 달보다 " + money(-s.Stats.Currencies[0].Delta, s.Stats.Currencies[0].Currency) + " 적게 나가요."
	}
	return s, nil
}

func (a *application) latestActivitySummary(now time.Time) (string, bool, error) {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, a.location).UTC().Format("2006-01-02 15:04:05")
	var eventType, name string
	var oldAmount, newAmount sql.NullInt64
	var oldCurrency, newCurrency sql.NullString
	err := a.db.QueryRow(`SELECT event_type,service_name,old_amount,old_currency,new_amount,new_currency FROM activity_events WHERE occurred_at>=? ORDER BY occurred_at DESC,id DESC LIMIT 1`, monthStart).Scan(&eventType, &name, &oldAmount, &oldCurrency, &newAmount, &newCurrency)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	switch eventType {
	case "added":
		return "이번 달은 " + name + " (" + money(newAmount.Int64, newCurrency.String) + ")이 추가되었어요.", true, nil
	case "cancelled":
		return "이번 달은 " + name + " 구독이 삭제되었어요.", true, nil
	case "price_changed":
		return name + "의 금액이 " + money(oldAmount.Int64, oldCurrency.String) + "에서 " + money(newAmount.Int64, newCurrency.String) + "으로 변경됐어요.", true, nil
	default:
		return "", false, nil
	}
}

func (a *application) loadSubscriptions() ([]subscription, error) {
	query := `
		SELECT
			s.id,
			s.service_id,
			s.service_name,
			s.icon,
			s.color,
			s.amount,
			s.currency,
			s.billing_cycle,
			s.billing_day,
			s.payment_method_id,
			p.name,
			s.category,
			s.memo,
			s.status,
			s.started_at,
			COALESCE(NULLIF(s.billing_anchor,''),s.started_at),
			COALESCE(s.cancelled_at,''),
			COALESCE(s.trial_ends_at,''),
			COALESCE(o.skipped,0)
		FROM subscriptions s
		JOIN payment_methods p ON p.id=s.payment_method_id
		LEFT JOIN subscription_occurrences o
			ON o.subscription_id=s.id AND o.period=?
		ORDER BY s.status,s.billing_day,s.id`
	period := time.Now().In(a.location).Format("2006-01")
	rows, err := a.db.Query(query, period)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subscription
	for rows.Next() {
		var v subscription
		if err := rows.Scan(
			&v.ID,
			&v.ServiceID,
			&v.ServiceName,
			&v.Icon,
			&v.Color,
			&v.Amount,
			&v.Currency,
			&v.BillingCycle,
			&v.BillingDay,
			&v.PaymentMethodID,
			&v.PaymentMethodName,
			&v.Category,
			&v.Memo,
			&v.Status,
			&v.CreatedAt,
			&v.BillingDate,
			&v.CancelledAt,
			&v.TrialEndsAt,
			&v.Skipped,
		); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (a *application) monthTotals(period string) (map[string]int64, error) {
	y, m, err := parsePeriod(period)
	if err != nil {
		return nil, err
	}
	type pricePoint struct {
		amount                  int64
		currency, effectiveFrom string
	}
	history := map[int][]pricePoint{}
	historyRows, err := a.db.Query(`SELECT subscription_id,amount,currency,effective_from FROM subscription_price_history ORDER BY subscription_id,effective_from,id`)
	if err != nil {
		return nil, err
	}
	for historyRows.Next() {
		var id int
		var point pricePoint
		if err := historyRows.Scan(&id, &point.amount, &point.currency, &point.effectiveFrom); err != nil {
			historyRows.Close()
			return nil, err
		}
		history[id] = append(history[id], point)
	}
	if err := historyRows.Close(); err != nil {
		return nil, err
	}
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, a.location).Day()
	rows, err := a.db.Query(`SELECT s.id,s.amount,s.currency,s.billing_cycle,s.billing_day,COALESCE(NULLIF(s.billing_anchor,''),s.started_at),COALESCE(s.cancelled_at,''),COALESCE(o.skipped,0),o.amount,o.currency FROM subscriptions s LEFT JOIN subscription_occurrences o ON o.subscription_id=s.id AND o.period=?`, period)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := map[string]int64{}
	for rows.Next() {
		var id, day int
		var amount int64
		var currency, cycle, anchor, cancelled string
		var skipped bool
		var occurrenceAmount sql.NullInt64
		var occurrenceCurrency sql.NullString
		if err := rows.Scan(&id, &amount, &currency, &cycle, &day, &anchor, &cancelled, &skipped, &occurrenceAmount, &occurrenceCurrency); err != nil {
			return nil, err
		}
		if skipped {
			continue
		}
		billDate := time.Date(y, m, min(day, last), 0, 0, 0, 0, a.location)
		billingAnchor, _ := time.ParseInLocation("2006-01-02", anchor[:10], a.location)
		if billDate.Before(billingAnchor) {
			continue
		}
		if cancelled != "" {
			c, _ := time.ParseInLocation("2006-01-02", cancelled[:10], a.location)
			if billDate.After(c) {
				continue
			}
		}
		if cycle == "yearly" && int(m) != int(billingAnchor.Month()) {
			continue
		}
		if occurrenceAmount.Valid {
			amount = occurrenceAmount.Int64
			if occurrenceCurrency.Valid && occurrenceCurrency.String != "" {
				currency = occurrenceCurrency.String
			}
		} else {
			billDateText := billDate.Format("2006-01-02")
			for _, point := range history[id] {
				if point.effectiveFrom <= billDateText {
					amount = point.amount
					currency = point.currency
				} else {
					break
				}
			}
		}
		totals[strings.ToUpper(currency)] += amount
	}
	return totals, rows.Err()
}

type subInput struct {
	ServiceID                                                        *int64 `json:"serviceId"`
	ServiceName, Icon, Color, Category, Currency, BillingCycle, Memo string
	TrialEndsAt                                                      string `json:"trialEndsAt"`
	BillingDate                                                      string `json:"billingDate"`
	Amount                                                           int64
	BillingDay                                                       int
	PaymentMethodID                                                  int64
}

func validateSub(v subInput) error {
	if strings.TrimSpace(v.ServiceName) == "" {
		return errors.New("서비스명을 입력해 주세요")
	}
	if v.Amount < 0 {
		return errors.New("금액을 확인해 주세요")
	}
	if _, err := time.Parse("2006-01-02", v.BillingDate); err != nil {
		return errors.New("결제 날짜를 선택해 주세요")
	}
	if v.TrialEndsAt != "" {
		trialEnd, err := time.Parse("2006-01-02", v.TrialEndsAt)
		if err != nil {
			return errors.New("무료 체험 종료일을 확인해 주세요")
		}
		billingDate, _ := time.Parse("2006-01-02", v.BillingDate)
		if billingDate.Before(trialEnd) {
			return errors.New("첫 결제일은 무료 체험 종료일 이후여야 해요")
		}
	}
	if v.BillingCycle != "monthly" && v.BillingCycle != "yearly" {
		return errors.New("결제 주기를 확인해 주세요")
	}
	if !currencyCodePattern.MatchString(strings.ToUpper(v.Currency)) {
		return errors.New("통화를 확인해 주세요")
	}
	if v.PaymentMethodID < 1 {
		return errors.New("결제수단을 선택해 주세요")
	}
	return nil
}
func (a *application) createSubscription(w http.ResponseWriter, r *http.Request) {
	var v subInput
	if !decode(w, r, &v) {
		return
	}
	v.Currency = strings.ToUpper(strings.TrimSpace(v.Currency))
	if v.Currency == "" {
		v.Currency = "KRW"
	}
	if !validOrError(w, validateSub(v)) {
		return
	}
	var currencyCount int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM currencies WHERE code=? AND archived=0`, v.Currency).Scan(&currencyCount); err != nil {
		a.fail(w, err)
		return
	}
	if currencyCount != 1 {
		bad(w, "사용할 수 없는 통화예요")
		return
	}
	if v.Icon == "" {
		v.Icon = initial(v.ServiceName)
	}
	if v.Color == "" {
		v.Color = "#9AB8A8"
	}
	billingDate, _ := time.Parse("2006-01-02", v.BillingDate)
	v.BillingDay = billingDate.Day()
	startedAt := time.Now().In(a.location).Format("2006-01-02")
	tx, err := a.db.Begin()
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO subscriptions(
			service_id,
			service_name,
			icon,
			color,
			amount,
			currency,
			billing_cycle,
			billing_day,
			billing_anchor,
			payment_method_id,
			category,
			memo,
			started_at,
			trial_ends_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ServiceID,
		strings.TrimSpace(v.ServiceName),
		v.Icon,
		v.Color,
		v.Amount,
		v.Currency,
		v.BillingCycle,
		v.BillingDay,
		v.BillingDate,
		v.PaymentMethodID,
		strings.TrimSpace(v.Category),
		strings.TrimSpace(v.Memo),
		startedAt,
		v.TrialEndsAt,
	)
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	if _, err = tx.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,?,?,?)`, id, v.Amount, v.Currency, startedAt); err == nil {
		_, err = tx.Exec(`INSERT INTO activity_events(subscription_id,event_type,service_name,new_amount,new_currency) VALUES(?,'added',?,?,?)`, id, strings.TrimSpace(v.ServiceName), v.Amount, v.Currency)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	go a.notifyChange("➕ 구독 추가\n\n" + strings.TrimSpace(v.ServiceName) + "\n" + money(v.Amount, v.Currency))
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (a *application) updateSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var v subInput
	if !decode(w, r, &v) {
		return
	}
	v.Currency = strings.ToUpper(strings.TrimSpace(v.Currency))
	if !validOrError(w, validateSub(v)) {
		return
	}
	var currencyCount int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM currencies WHERE code=? AND archived=0`, v.Currency).Scan(&currencyCount); err != nil {
		a.fail(w, err)
		return
	}
	if currencyCount != 1 {
		bad(w, "사용할 수 없는 통화예요")
		return
	}
	var oldName, oldCurrency string
	var oldAmount int64
	if err := a.db.QueryRow(`SELECT service_name,amount,currency FROM subscriptions WHERE id=? AND status='active'`, id).Scan(&oldName, &oldAmount, &oldCurrency); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	billingDate, _ := time.Parse("2006-01-02", v.BillingDate)
	v.BillingDay = billingDate.Day()
	tx, err := a.db.Begin()
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`UPDATE subscriptions SET
			service_id=?,
			service_name=?,
			icon=?,
			color=?,
			amount=?,
			currency=?,
			billing_cycle=?,
			billing_day=?,
			billing_anchor=?,
			payment_method_id=?,
			category=?,
			memo=?,
			trial_ends_at=?,
			updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status='active'`,
		v.ServiceID,
		strings.TrimSpace(v.ServiceName),
		v.Icon,
		v.Color,
		v.Amount,
		v.Currency,
		v.BillingCycle,
		v.BillingDay,
		v.BillingDate,
		v.PaymentMethodID,
		strings.TrimSpace(v.Category),
		strings.TrimSpace(v.Memo),
		v.TrialEndsAt,
		id,
	)
	if err != nil {
		a.fail(w, err)
		return
	}
	if oldAmount != v.Amount || oldCurrency != v.Currency {
		effective := time.Now().In(a.location).Format("2006-01-02")
		_, err = tx.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,?,?,?)`, id, v.Amount, v.Currency, effective)
		if err == nil {
			_, err = tx.Exec(`INSERT INTO activity_events(subscription_id,event_type,service_name,old_amount,old_currency,new_amount,new_currency) VALUES(?,'price_changed',?,?,?,?,?)`, id, strings.TrimSpace(v.ServiceName), oldAmount, oldCurrency, v.Amount, v.Currency)
		}
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	go a.notifyChange("✏️ 구독 변경\n\n" + strings.TrimSpace(v.ServiceName) + "\n" + money(v.Amount, v.Currency))
	changed(w, res)
}

func (a *application) skipSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var v struct {
		Skipped bool `json:"skipped"`
	}
	if !decode(w, r, &v) {
		return
	}
	now := time.Now().In(a.location)
	var amount int64
	var day int
	var currency string
	if err := a.db.QueryRow(`SELECT amount,billing_day,currency FROM subscriptions WHERE id=? AND status='active'`, id).Scan(&amount, &day, &currency); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	lastDay := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, a.location).Day()
	date := time.Date(
		now.Year(),
		now.Month(),
		min(day, lastDay),
		0,
		0,
		0,
		0,
		a.location,
	).Format("2006-01-02")
	_, err := a.db.Exec(
		`INSERT INTO subscription_occurrences(
			subscription_id,
			period,
			scheduled_date,
			amount,
			currency,
			skipped
		) VALUES(?,?,?,?,?,?)
		ON CONFLICT(subscription_id,period) DO UPDATE SET
			skipped=excluded.skipped,
			amount=excluded.amount,
			currency=excluded.currency,
			updated_at=CURRENT_TIMESTAMP`,
		id,
		now.Format("2006-01"),
		date,
		amount,
		currency,
		v.Skipped,
	)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *application) cancelSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var name string
	if err := a.db.QueryRow(`SELECT service_name FROM subscriptions WHERE id=? AND status='active'`, id).Scan(&name); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	res, err := a.db.Exec(`UPDATE subscriptions SET status='cancelled',cancelled_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='active'`, time.Now().In(a.location).Format("2006-01-02"), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	_, _ = a.db.Exec(`INSERT INTO activity_events(subscription_id,event_type,service_name) VALUES(?,'cancelled',?)`, id, name)
	go a.notifyChange("👋 구독 해지\n\n" + name + " 구독을 해지했어요.")
	changed(w, res)
}

func (a *application) updateSettings(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Name, Currency, DiscordWebhook, TelegramBotToken, TelegramChatID string
		NotifyDays                                                       int
		NotifyUpcoming, NotifyChanges, NotifyMonthly                     bool
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
	v.Currency = strings.ToUpper(strings.TrimSpace(v.Currency))
	var currencyCount int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM currencies WHERE code=? AND archived=0`, v.Currency).Scan(&currencyCount); err != nil {
		a.fail(w, err)
		return
	}
	if currencyCount != 1 {
		bad(w, "기본 통화를 확인해 주세요")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE users SET name=?,currency=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, v.Name, v.Currency); err == nil {
		_, err = tx.Exec(`UPDATE notification_channels SET discord_webhook=?,telegram_bot_token=?,telegram_chat_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, strings.TrimSpace(v.DiscordWebhook), strings.TrimSpace(v.TelegramBotToken), strings.TrimSpace(v.TelegramChatID))
	}
	if err == nil {
		_, err = tx.Exec(`UPDATE notification_rules SET notify_upcoming=?,notify_changes=?,notify_monthly=?,days_before=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, v.NotifyUpcoming, v.NotifyChanges, v.NotifyMonthly, v.NotifyDays)
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
		_, err = tx.Exec(`INSERT INTO sessions(user_id,token_hash,expires_at) VALUES(?,?,?)`, userID, tokenHash, expires.Format(time.RFC3339))
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
	res, err := a.db.Exec(`INSERT INTO payment_methods(name,type,is_builtin) VALUES(?,'custom',0)`, v.Name)
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
	var v struct {
		Name string
	}
	if !decode(w, r, &v) {
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		bad(w, "이름을 입력해 주세요")
		return
	}
	res, err := a.db.Exec(`UPDATE payment_methods SET name=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND is_builtin=0 AND archived=0`, v.Name, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			bad(w, "이미 같은 결제수단이 있어요")
			return
		}
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
	var used int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE payment_method_id=?`, id).Scan(&used); err != nil {
		a.fail(w, err)
		return
	}
	var res sql.Result
	var err error
	if used > 0 {
		res, err = a.db.Exec(`UPDATE payment_methods SET archived=1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND is_builtin=0`, id)
	} else {
		res, err = a.db.Exec(`DELETE FROM payment_methods WHERE id=? AND is_builtin=0`, id)
	}
	if err != nil {
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
	res, err := a.db.Exec(`INSERT INTO currencies(code,name,is_builtin) VALUES(?,?,0)`, v.Code, v.Code)
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
	var code string
	var builtin bool
	if err := a.db.QueryRow(`SELECT code,is_builtin FROM currencies WHERE id=? AND archived=0`, id).Scan(&code, &builtin); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	if builtin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "기본 통화는 삭제할 수 없어요"})
		return
	}
	var used int
	if err := a.db.QueryRow(`SELECT (SELECT COUNT(*) FROM subscriptions WHERE currency=?)+(SELECT COUNT(*) FROM subscription_price_history WHERE currency=?)+(SELECT COUNT(*) FROM users WHERE currency=?)`, code, code, code).Scan(&used); err != nil {
		a.fail(w, err)
		return
	}
	var res sql.Result
	var err error
	if used > 0 {
		res, err = a.db.Exec(`UPDATE currencies SET archived=1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND is_builtin=0`, id)
	} else {
		res, err = a.db.Exec(`DELETE FROM currencies WHERE id=? AND is_builtin=0`, id)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	changed(w, res)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		bad(w, "입력 내용을 확인해 주세요")
		return false
	}
	return true
}
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		bad(w, "잘못된 항목이에요")
		return 0, false
	}
	return id, true
}
func changed(w http.ResponseWriter, res sql.Result) {
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "항목을 찾을 수 없어요"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func notFoundOrFail(a *application, w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "항목을 찾을 수 없어요"})
	} else {
		a.fail(w, err)
	}
}
func validOrError(w http.ResponseWriter, err error) bool {
	if err != nil {
		bad(w, err.Error())
		return false
	}
	return true
}
func bad(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}
func (a *application) fail(w http.ResponseWriter, err error) {
	log.Print(err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "잠시 후 다시 시도해 주세요"})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func parsePeriod(p string) (int, time.Month, error) {
	t, e := time.Parse("2006-01", p)
	return t.Year(), t.Month(), e
}
func nextPayment(now time.Time, day int, cycle, startedAt string) string {
	month := now.Month()
	var anchor time.Time
	if len(startedAt) >= 10 {
		anchor, _ = time.ParseInLocation("2006-01-02", startedAt[:10], now.Location())
	}
	if cycle == "yearly" && !anchor.IsZero() {
		if started, err := time.ParseInLocation("2006-01-02", startedAt[:10], now.Location()); err == nil {
			month = started.Month()
		}
	}
	last := time.Date(now.Year(), month+1, 0, 0, 0, 0, 0, now.Location()).Day()
	candidate := time.Date(now.Year(), month, min(day, last), 0, 0, 0, 0, now.Location())
	if !anchor.IsZero() && candidate.Before(anchor) {
		candidate = anchor
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for candidate.Before(today) {
		if cycle == "yearly" {
			candidate = candidate.AddDate(1, 0, 0)
		} else {
			candidate = candidate.AddDate(0, 1, 0)
		}
	}
	return candidate.Format("2006-01-02")
}
func initial(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "S"
	}
	return strings.ToUpper(string(r[0]))
}
func money(v int64, currency string) string {
	symbol := map[string]string{"KRW": "₩", "USD": "$", "JPY": "¥", "EUR": "€", "TRY": "₺", "ARS": "ARS $"}[strings.ToUpper(currency)]
	if symbol == "" {
		return strings.ToUpper(currency) + " " + formatMinorUnits(v, currency)
	}
	return symbol + formatMinorUnits(v, currency)
}

func currencyFractionDigits(currency string) int {
	currency = strings.ToUpper(currency)
	if strings.Contains(" BIF CLP DJF GNF ISK JPY KMF KRW PYG RWF UGX VND VUV XAF XOF XPF ", " "+currency+" ") {
		return 0
	}
	if strings.Contains(" BHD IQD JOD KWD LYD OMR TND ", " "+currency+" ") {
		return 3
	}
	return 2
}

func minorUnitFactor(currency string) int64 {
	factor := int64(1)
	for i := 0; i < currencyFractionDigits(currency); i++ {
		factor *= 10
	}
	return factor
}

func formatMinorUnits(v int64, currency string) string {
	factor := minorUnitFactor(currency)
	if factor == 1 {
		return formatNumber(v)
	}
	return formatNumber(v/factor) + "." + fmt.Sprintf("%0*d", currencyFractionDigits(currency), v%factor)
}
func formatNumber(v int64) string {
	s := strconv.FormatInt(v, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
