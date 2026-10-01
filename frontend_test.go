package main

import (
	"strings"
	"testing"
)

func TestSubscriptionFormKeepsActionsVisibleAndOptionsCollapsible(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`class="optional-fields wide"`, `class="form-actions subscription-form-actions"`, `form="subscriptionForm"`, `subscription-modal`} {
		if !strings.Contains(string(jsSource), want) {
			t.Fatalf("subscription form source is missing %q", want)
		}
	}
	if strings.Contains(string(jsSource), `무료 체험, 카테고리, 메모`) || strings.Contains(string(jsSource), `s.TrialEndsAt || s.Category || s.Memo ? "open"`) {
		t.Fatal("subscription options must not show a description or open by default")
	}
	htmlSource, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlSource), `id="modalFooter"`) {
		t.Fatal("subscription form requires a dedicated modal footer")
	}
	cssSource, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := compactSource(string(cssSource))
	for _, rule := range []string{`[hidden]{display:none!important`, `.modal.subscription-modal{--subscription-modal-padding:27px;display:flex`, `.modal.subscription-modal#modalFooter{flex:0 0 auto`, `.optional-fields{grid-column:1/-1`} {
		if !strings.Contains(css, compactSource(rule)) {
			t.Fatalf("subscription form style is missing %q", rule)
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
	for _, want := range []string{`inputmode="decimal"`, `amountMinorUnits`, `currencyDigits`, `0 이상의 금액을 통화의 소수 자릿수에 맞게 입력해 주세요.`} {
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
	for _, rule := range []string{".main:focus{outline:none", ".upcoming-row .sub-content{min-width:0;grid-column:1/-1", "#addSubscriptionButton{height:39px", ".skip-status{display:inline-flex", ".sub-card.skipped{opacity:1;border:2px solid", ".button.skip-action{color:", ".sidebar .primary-metric{grid-column:1/-1;order:-1;min-height:76px", ".sidebar .nav-card{grid-column:auto;min-height:68px", ".sidebar .secondary-metric{grid-column:1/-1;min-height:68px"} {
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
	if !strings.Contains(js, `["list-row", s.Skipped && "skipped"`) || !strings.Contains(js, `<small class="skip-status">이번 달 결제 건너뜀</small>`) {
		t.Fatal("skipped subscriptions must carry an explicit class and readable status")
	}

	htmlSource, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlSource)
	for _, want := range []string{`primary-metric`, `secondary-metric`, `class="summary-card nav-card"`, `class="summary-card nav-card upcoming-nav-card"`, `구독 관리`, `결제 일정 보기`} {
		if !strings.Contains(html, want) {
			t.Fatalf("mobile summary markup is missing %q", want)
		}
	}
	if !strings.Contains(html, "<title>SubManager</title>") || strings.Contains(html, "나의 구독 관리") {
		t.Fatal("page title must contain only SubManager")
	}
	for _, want := range []string{`id="themeButton"`, `dataset.themePreference=preference`, `submanager-theme`, `prefers-color-scheme: dark`} {
		if !strings.Contains(html, want) && !strings.Contains(js, want) {
			t.Fatalf("theme controls must contain %q", want)
		}
	}
	for _, want := range []string{`class="icon-button github-link"`, `href="https://github.com/kmw0410/submanager"`, `title="GitHub 저장소"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("GitHub repository link is missing %q", want)
		}
	}
	if strings.Index(html, `id="settingsButton"`) > strings.Index(html, `class="icon-button github-link"`) ||
		strings.Index(html, `class="icon-button github-link"`) > strings.Index(html, `id="addSubscriptionButton"`) {
		t.Fatal("GitHub repository link must appear between settings and add-subscription actions")
	}
	if !strings.Contains(css, `:root[data-theme=light]`) || !strings.Contains(js, `themeMedia.addEventListener("change"`) {
		t.Fatal("light and system theme behavior must be defined")
	}
	if strings.Contains(html, `>×</button>`) || strings.Contains(html, `<span>+</span>`) {
		t.Fatal("header and modal action icons must use SVG")
	}
	if !strings.Contains(html, `href="/assets/app.css?v=20261001-subscriptions"`) || !strings.Contains(html, `src="/assets/app.js?v=20261001-subscriptions"`) {
		t.Fatal("dashboard assets must use the current cache version")
	}
	authSource, err := webFS.ReadFile("web/auth.html")
	if err != nil {
		t.Fatal(err)
	}
	auth := string(authSource)
	if !strings.Contains(auth, `href="/assets/app.css?v=20261001-subscriptions"`) {
		t.Fatal("authentication stylesheet must use the current cache version")
	}
	for _, want := range []string{`name="setupToken"`, `minlength="48" maxlength="48"`, `docker compose logs submanager`} {
		if !strings.Contains(auth, want) {
			t.Fatalf("setup token instructions are missing %q", want)
		}
	}
}

func TestIntegrationSettingsAndPWAControls(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsSource)
	for _, want := range []string{
		`integrationToggle("discordEnabled", "Discord", discordEnabled)`,
		`integrationToggle("telegramEnabled", "Telegram", telegramEnabled)`,
		`data-test="discord"`,
		`data-test="telegram"`,
		`integrationToggle("pwaEnabled", "PWA", pwaEnabled)`,
		`id="installPWA"`,
		`id="enablePWAPush"`,
		`data-test="pwa"`,
		`data-test="pwa">PWA 테스트</button>`,
		`const body = { channel: b.dataset.test };`,
		`/api/pwa/subscriptions`,
		`const subscriptionJSON = subscription.toJSON();`,
		`serviceWorker.register("/sw.js")`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("integration settings source is missing %q", want)
		}
	}
	if strings.Contains(js, `Discord Webhook</span>`) || strings.Contains(js, `Telegram Bot Token</span>`) {
		t.Fatal("integration labels must use the channel names")
	}
	for _, path := range []string{"web/manifest.webmanifest", "web/sw.js", "web/icon.svg"} {
		if _, err := webFS.ReadFile(path); err != nil {
			t.Fatalf("PWA asset %q is missing: %v", path, err)
		}
	}
	manifestSource, err := webFS.ReadFile("web/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifestSource), `"display": "standalone"`) {
		t.Fatal("PWA manifest must use standalone display mode")
	}
	swSource, err := webFS.ReadFile("web/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(swSource), "/api/") {
		t.Fatal("service worker must not cache authenticated pages or API responses")
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

func TestUpcomingICSExportSourceUsesExistingModal(t *testing.T) {
	jsSource, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsSource)
	for _, want := range []string{
		`data-export-ics`,
		`>결제 일정 내보내기</button>`,
		`openModal("ICS 내보내기", "결제 예정")`,
		`ICS 형식 · 미래 결제 예정만 포함`,
		`name="months" value="12" checked`,
		`/api/upcoming/export?format=ics&months=`,
		`link.download = "submanager-payments.ics"`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("ICS export UI is missing %q", want)
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
		"grid-template-columns:repeat(4,minmax(0,1fr))",
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

func TestCompactDashboardSurfacesAndMobilePaymentDates(t *testing.T) {
	cssSource, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := compactSource(string(cssSource))
	for _, rule := range []string{"--radius-sm:6px", "--radius-md:8px", "--radius-lg:10px", ".dashboard-overview{display:grid;grid-template-columns:minmax(0,2fr)minmax(0,1fr)", ".upcoming-preview-list{min-height:0;overflow-y:auto", ".list-row>span:nth-child(4){grid-column:1/-1", ".dashboard-empty{", ".list-row:not(.list-labels):focus-visible{"} {
		if !strings.Contains(css, rule) {
			t.Fatalf("missing dashboard behavior: %s", rule)
		}
	}
	for _, query := range []string{"@media(max-width:850px)", "@media(max-width:620px)"} {
		if strings.Count(css, query) != 1 {
			t.Fatalf("responsive rules must be consolidated for %s", query)
		}
	}
	htmlSource, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlSource)
	if strings.Contains(html, "<style>") || strings.Contains(html, "window.fetch =") || strings.Contains(html, "MutationObserver") {
		t.Fatal("dashboard HTML must not patch requests or modal rendering")
	}
	if !strings.Contains(html, `aria-label="GitHub 저장소 열기"`) {
		t.Fatal("icon repository link needs an accessible name")
	}
	swSource, err := webFS.ReadFile("web/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range []string{"app.css?v=20261001-subscriptions", "app.js?v=20261001-subscriptions"} {
		if !strings.Contains(string(swSource), asset) {
			t.Fatalf("service worker has stale asset: %s", asset)
		}
	}
}
