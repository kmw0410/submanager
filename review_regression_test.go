package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestScheduledRemindersExcludeCancelledOnBillingDay(t *testing.T) {
	a := newTestApplication(t)
	configureTestDiscord(t, a)
	if _, err := a.db.Exec(`UPDATE notification_rules SET days_before=0,notify_upcoming=1; INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at,status,cancelled_at) VALUES(100,'Cancelled',1000,'KRW','monthly',15,'2026-01-15',1,'2026-01-01','cancelled','2026-10-15'),(101,'Active',2000,'KRW','monthly',15,'2026-01-15',1,'2026-01-01','active',NULL)`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.notificationHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "Cancelled") || !strings.Contains(string(body), "Active") {
			t.Fatal("reminder included cancelled subscription or omitted active subscription")
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	a.runScheduledNotificationsAt(time.Date(2026, 10, 15, 12, 0, 0, 0, a.location))
	if calls != 1 {
		t.Fatal("active same-day reminder missing")
	}
	month, err := a.loadPaymentOccurrences("2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if month.Totals["KRW"] != 3000 {
		t.Fatal("filtering reminders removed cancelled historical spending")
	}
}

func TestSkipTogglesPreserveEffectiveBilledPriceAndExistingOverrides(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical price", true: "existing paid snapshot"}[existing], func(t *testing.T) {
			a := newTestApplication(t)
			now := time.Now().In(a.location)
			period := now.Format("2006-01")
			billing := period + "-01"
			later := period + "-02"
			if _, err := a.db.Exec(`INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES(100,'Price changed',2000,'USD','monthly',1,?,1,?); INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(100,1000,'KRW',?),(100,2000,'USD',?)`, billing, billing, billing, later); err != nil {
				t.Fatal(err)
			}
			wantAmount := int64(1000)
			wantCurrency := "KRW"
			paid := false
			if existing {
				wantAmount = 1500
				wantCurrency = "EUR"
				paid = true
				if _, err := a.db.Exec(`INSERT INTO subscription_occurrences(subscription_id,period,scheduled_date,amount,currency,paid) VALUES(100,?,?,1500,'EUR',1)`, period, billing); err != nil {
					t.Fatal(err)
				}
			}
			for _, skipped := range []bool{true, false} {
				w := callSubscriptionHandler(t, a.skipSubscription, 100, map[string]bool{"skipped": skipped})
				if w.Code != http.StatusOK {
					t.Fatalf("skip status=%d", w.Code)
				}
				var amount int64
				var currency string
				var gotPaid, gotSkipped bool
				if err := a.db.QueryRow(`SELECT amount,currency,paid,skipped FROM subscription_occurrences WHERE subscription_id=100 AND period=?`, period).Scan(&amount, &currency, &gotPaid, &gotSkipped); err != nil {
					t.Fatal(err)
				}
				if amount != wantAmount || currency != wantCurrency || gotPaid != paid || gotSkipped != skipped {
					t.Fatal("skip toggle changed billed money or paid state")
				}
			}
			month, err := a.loadPaymentOccurrences(period)
			if err != nil {
				t.Fatal(err)
			}
			if month.Totals[wantCurrency] != wantAmount || month.Totals["USD"] != 0 {
				t.Fatal("unskip rewrote historical monthly total")
			}
			var amount int64
			var currency string
			if err := a.db.QueryRow(`SELECT amount,currency FROM subscriptions WHERE id=100`).Scan(&amount, &currency); err != nil {
				t.Fatal(err)
			}
			if amount != 2000 || currency != "USD" {
				t.Fatal("skip changed base subscription")
			}
		})
	}
}

func TestSkipDateChangeRecomputesPriceAndKeepsPaidState(t *testing.T) {
	a := newTestApplication(t)
	period := time.Now().In(a.location).Format("2006-01")
	if _, err := a.db.Exec(`INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES(100,'Date changed',2000,'USD','monthly',3,?,1,?); INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(100,1000,'KRW',?),(100,2000,'USD',?); INSERT INTO subscription_occurrences(subscription_id,period,scheduled_date,amount,currency,paid) VALUES(100,?,?,1500,'EUR',1)`, period+"-01", period+"-01", period+"-01", period+"-02", period, period+"-01"); err != nil {
		t.Fatal(err)
	}
	w := callSubscriptionHandler(t, a.skipSubscription, 100, map[string]bool{"skipped": false})
	if w.Code != http.StatusOK {
		t.Fatalf("skip status=%d", w.Code)
	}
	var date, currency string
	var amount int64
	var paid bool
	if err := a.db.QueryRow(`SELECT scheduled_date,amount,currency,paid FROM subscription_occurrences WHERE subscription_id=100 AND period=?`, period).Scan(&date, &amount, &currency, &paid); err != nil {
		t.Fatal(err)
	}
	if date != period+"-03" || amount != 2000 || currency != "USD" || !paid {
		t.Fatal("changed billing date did not use its effective price or lost paid status")
	}
}
