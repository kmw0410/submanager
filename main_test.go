package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	a := &application{db: db, location: loc}
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
	r, w := jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{"name": "관리자", "email": "admin@example.com", "password": "safe-password"})
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
	r, w = jsonRequest(t, http.MethodPost, "/auth/setup", map[string]string{"name": "두 번째", "email": "two@example.com", "password": "safe-password"})
	a.setupAccount(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("second setup status=%d", w.Code)
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
	if backup.Version != 2 || len(backup.Subscriptions) != 1 || backup.Subscriptions[0].Amount != 1599 {
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
	if !strings.Contains(text, `<label class="button import-label">JSON 가져오기<input id="importData" type="file"`) {
		t.Fatal("import control must use the styled JSON import label")
	}
	if strings.Contains(text, `#importData').click()`) {
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
	if !strings.Contains(js, `class="back-button" type="button" data-view="dashboard"`) || !strings.Contains(js, `<svg aria-hidden="true" viewBox="0 0 24 24"><path d="m15 18-6-6 6-6"/></svg>돌아가기`) {
		t.Fatal("monthly statistics must provide an in-page dashboard back button")
	}

	cssSource, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssSource)
	for _, rule := range []string{".main:focus{outline:none}", ".upcoming-row .sub-content{flex:1;text-align:left}", "#addSubscriptionButton{height:39px", ".skip-status{display:inline-flex", `@media(max-width:620px){.skip-status-mobile{display:inline-flex}}`} {
		if !strings.Contains(css, rule) {
			t.Fatalf("missing presentation rule %q", rule)
		}
	}
	if !strings.Contains(js, `s.Skipped?'이번 달 결제 건너뜀'`) || !strings.Contains(js, `s.Skipped?'skip-status':''`) {
		t.Fatal("skipped subscriptions must show an explicit status badge")
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
	if !strings.Contains(css, `:root[data-theme=light]`) || !strings.Contains(js, `themeMedia.addEventListener('change'`) {
		t.Fatal("light and system theme behavior must be defined")
	}
	if strings.Contains(html, `>×</button>`) || strings.Contains(html, `<span>+</span>`) {
		t.Fatal("header and modal action icons must use SVG")
	}
	if !strings.Contains(html, `href="/assets/app.css"`) || !strings.Contains(html, `src="/assets/app.js"`) || strings.Contains(html, `?v=`) {
		t.Fatal("dashboard assets must use stable URLs without version query tags")
	}
	authSource, err := webFS.ReadFile("web/auth.html")
	if err != nil {
		t.Fatal(err)
	}
	auth := string(authSource)
	if !strings.Contains(auth, `href="/assets/app.css"`) || strings.Contains(auth, `?v=`) {
		t.Fatal("authentication assets must use stable URLs without version query tags")
	}
}

func TestDashboardResponsiveLayout(t *testing.T) {
	cssSource, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssSource)
	for _, rule := range []string{
		"grid-template-columns:minmax(0,1.45fr)",
		".currency-tabs{max-width:100%;overflow-x:auto",
		".upcoming-row{display:grid;grid-template-columns:minmax(0,1fr) auto",
		"max-height:93dvh",
		"@media(max-width:420px)",
		".form-actions,.edit-actions,.data-actions{display:grid;grid-template-columns:1fr}",
	} {
		if !strings.Contains(css, rule) {
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
	for _, want := range []string{
		`id="subscriptionSearch" type="search"`,
		`data-sub-category=`,
		`aria-label="카테고리 필터"`,
		`document.addEventListener('input'`,
		`[s.ServiceName,category,s.PaymentMethodName,s.Memo]`,
		`renderSubscriptionResults()`,
	} {
		if !strings.Contains(js, want) {
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
	css := string(cssSource)
	for _, want := range []string{".subscription-tools{display:grid", ".category-filters{display:flex", ".category-filters button[aria-pressed=true]"} {
		if !strings.Contains(css, want) {
			t.Fatalf("subscription filter styles are missing %q", want)
		}
	}
}

func TestNotificationTestMessageIncludesExamplePayment(t *testing.T) {
	message := notificationTestMessage(time.Date(2026, time.August, 11, 0, 0, 0, 0, time.FixedZone("Asia/Seoul", 9*60*60)))
	for _, want := range []string{"SubManager 알림 테스트", "정기결제 알림 테스트", "1,000원", "2026.08.11 결제 예정이에요."} {
		if !strings.Contains(message, want) {
			t.Fatalf("test notification is missing %q: %s", want, message)
		}
	}
	payload := discordWebhookPayload(message)
	embeds, ok := payload["embeds"].([]map[string]any)
	if !ok || len(embeds) != 1 || embeds[0]["title"] != "SubManager 알림 테스트" {
		t.Fatalf("discord test notification must be an embed: %#v", payload)
	}
}
