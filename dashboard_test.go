package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

func TestUpcomingICSExportsActualFutureOccurrences(t *testing.T) {
	a := newTestApplication(t)
	var paymentID int64
	var paymentName string
	if err := a.db.QueryRow(`SELECT id,name FROM payment_methods WHERE is_builtin=1 ORDER BY id LIMIT 1`).Scan(&paymentID, &paymentName); err != nil {
		t.Fatal(err)
	}
	result, err := a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('쉼표, 세미콜론; 역슬래시\',1099,'USD','monthly',24,'2026-08-24',?,'2026-08-01')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	monthlyID, _ := result.LastInsertId()
	if _, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,999,'USD','2026-08-01'),(?,1099,'USD','2026-09-01')`, monthlyID, monthlyID); err != nil {
		t.Fatal(err)
	}
	result, err = a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('연간 서비스',1280,'JPY','yearly',31,'2026-09-30',?,'2026-01-01')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	yearlyID, _ := result.LastInsertId()
	if _, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,1280,'JPY','2026-01-01')`, yearlyID); err != nil {
		t.Fatal(err)
	}
	result, err = a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('건너뜀',5000,'KRW','monthly',25,'2026-08-25',?,'2026-08-01')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	skippedID, _ := result.LastInsertId()
	if _, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,5000,'KRW','2026-08-01'); INSERT INTO subscription_occurrences(subscription_id,period,scheduled_date,amount,currency,skipped) VALUES(?,'2026-09','2026-09-25',5000,'KRW',1)`, skippedID, skippedID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at,trial_ends_at) VALUES('체험 중',7000,'KRW','monthly',15,'2026-10-15',?,'2026-08-01','2026-10-14'); INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at,status,cancelled_at) VALUES('해지됨',8000,'KRW','monthly',20,'2026-08-20',?,'2026-01-01','cancelled','2026-08-31')`, paymentID, paymentID); err != nil {
		t.Fatal(err)
	}

	ics, err := a.upcomingICS(
		time.Date(2026, time.August, 23, 0, 0, 0, 0, a.location),
		time.Date(2026, time.October, 31, 0, 0, 0, 0, a.location),
	)
	if err != nil {
		t.Fatal(err)
	}
	unfolded := strings.ReplaceAll(ics, "\r\n ", "")
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//SubManager//Payment Calendar//KO\r\nCALSCALE:GREGORIAN",
		fmt.Sprintf("UID:%d-20260824@submanager", monthlyID),
		fmt.Sprintf("UID:%d-20260924@submanager", monthlyID),
		fmt.Sprintf("UID:%d-20261024@submanager", monthlyID),
		"DTSTART;VALUE=DATE:20260930",
		"DTEND;VALUE=DATE:20261001",
		"SUMMARY:쉼표\\, 세미콜론\\; 역슬래시\\\\ 결제",
		"금액: USD 9.99\\n결제수단: " + paymentName + "\\n결제주기: 월간",
		"금액: USD 10.99",
		"금액: JPY 1\\,280",
		"END:VCALENDAR\r\n",
	} {
		if !strings.Contains(unfolded, want) {
			t.Fatalf("ICS is missing %q:\n%s", want, unfolded)
		}
	}
	if strings.Count(unfolded, "BEGIN:VEVENT") != 7 {
		t.Fatalf("monthly occurrences must be individual events:\n%s", unfolded)
	}
	for _, excluded := range []string{"DTSTART;VALUE=DATE:20260925", "해지됨 결제", "DTSTART;VALUE=DATE:20260815", "DTSTART;VALUE=DATE:20260915", "RRULE"} {
		if strings.Contains(unfolded, excluded) {
			t.Fatalf("ICS unexpectedly contains %q:\n%s", excluded, unfolded)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(ics, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("ICS line exceeds 75 octets: %q", line)
		}
	}
}

func TestUpcomingICSEmptyCalendarAndStableUID(t *testing.T) {
	a := newTestApplication(t)
	start := time.Date(2035, time.January, 1, 0, 0, 0, 0, a.location)
	ics, err := a.upcomingICS(start, start.AddDate(0, 0, 30))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ics, "BEGIN:VEVENT") || !strings.HasSuffix(ics, "END:VCALENDAR\r\n") {
		t.Fatalf("invalid empty calendar: %q", ics)
	}
	second, err := a.upcomingICS(start, start.AddDate(0, 0, 30))
	if err != nil || second != ics {
		t.Fatal("exporting the same range must remain stable")
	}
}

func TestUpcomingICSExportRejectsInvalidOptions(t *testing.T) {
	a := newTestApplication(t)
	for _, target := range []string{"/api/upcoming/export?format=json&months=12", "/api/upcoming/export?format=ics&months=2"} {
		recorder := httptest.NewRecorder()
		a.exportUpcoming(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"error"`) {
			t.Fatalf("target=%s status=%d body=%s", target, recorder.Code, recorder.Body.String())
		}
	}

	unauthenticated := httptest.NewRecorder()
	a.requireAuth(a.exportUpcoming)(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/upcoming/export?format=ics&months=12", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated export status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	empty := httptest.NewRecorder()
	a.exportUpcoming(empty, httptest.NewRequest(http.MethodGet, "/api/upcoming/export?format=ics&months=1", nil))
	if empty.Code != http.StatusOK || empty.Header().Get("Content-Type") != "text/calendar; charset=utf-8" || empty.Header().Get("Content-Disposition") != `attachment; filename="submanager-payments.ics"` {
		t.Fatalf("export headers status=%d content-type=%q disposition=%q", empty.Code, empty.Header().Get("Content-Type"), empty.Header().Get("Content-Disposition"))
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
