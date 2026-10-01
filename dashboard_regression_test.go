package main

import (
	"reflect"
	"testing"
	"time"
)

func TestDashboardMonthEndUsesSixDistinctMonths(t *testing.T) {
	a := newTestApplication(t)
	now := time.Date(2026, time.March, 31, 12, 0, 0, 0, a.location)
	state, err := a.loadStateAt(now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Stats.Months, []string{"10월", "11월", "12월", "1월", "2월", "3월"}) {
		t.Fatalf("months = %v", state.Stats.Months)
	}
}

func TestDashboardPreservesCancelledHistoricalCurrencies(t *testing.T) {
	a := newTestApplication(t)
	result, err := a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at,status,cancelled_at) VALUES('해지된 구독',2000,'USD','monthly',15,'2026-01-15',1,'2026-01-01','cancelled','2026-02-28')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if _, err := a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,1000,'EUR','2026-01-01'),(?,2000,'USD','2026-02-01')`, id, id); err != nil {
		t.Fatal(err)
	}
	state, err := a.loadStateAt(time.Date(2026, 3, 31, 12, 0, 0, 0, a.location))
	if err != nil {
		t.Fatal(err)
	}
	stats := map[string]currencyStat{}
	for _, stat := range state.Stats.Currencies {
		stats[stat.Currency] = stat
	}
	if state.Stats.ActiveCount != 0 || stats["EUR"].MonthlyTotals[3] != 1000 || stats["USD"].MonthlyTotals[4] != 2000 || stats["USD"].MonthTotal != 0 || stats["USD"].YearEstimate != 0 {
		t.Fatal("cancelled historical spending or currency was lost")
	}
}

func TestPaymentProjectionHandlesMalformedStoredDates(t *testing.T) {
	for _, column := range []string{"billing_anchor", "cancelled_at"} {
		t.Run(column, func(t *testing.T) {
			a := newTestApplication(t)
			id := createTestSubscription(t, a, subscriptionTestInput())
			// Both identifiers are fixed schema columns owned by this test.
			if _, err := a.db.Exec(`UPDATE subscriptions SET `+column+`='bad' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			month, err := a.loadPaymentOccurrences("2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if len(month.Items) != 0 {
				t.Fatal("malformed date produced a billing occurrence")
			}
		})
	}
}
