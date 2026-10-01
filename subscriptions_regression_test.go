package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func subscriptionTestInput() subInput {
	return subInput{ServiceName: "테스트 구독", Amount: 1000, Currency: "KRW", BillingCycle: "monthly", BillingDate: "2026-10-31", PaymentMethodID: 1}
}

func callSubscriptionHandler(t *testing.T, handler http.HandlerFunc, id int64, value any) *httptest.ResponseRecorder {
	t.Helper()
	r, w := jsonRequest(t, http.MethodPost, "/api/subscriptions", value)
	if id != 0 {
		r.SetPathValue("id", strconv.FormatInt(id, 10))
	}
	handler(w, r)
	return w
}

func createTestSubscription(t *testing.T, a *application, v subInput) int64 {
	t.Helper()
	w := callSubscriptionHandler(t, a.createSubscription, 0, v)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d", w.Code)
	}
	var result struct{ ID int64 }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ID <= 0 {
		t.Fatal("create did not return a subscription ID")
	}
	return result.ID
}

func TestSubscriptionRejectsInvalidInputAndReferences(t *testing.T) {
	cases := []struct {
		name string
		edit func(*subInput)
	}{
		{"empty service", func(v *subInput) { v.ServiceName = " " }},
		{"negative amount", func(v *subInput) { v.Amount = -1 }},
		{"invalid date", func(v *subInput) { v.BillingDate = "2026-02-30" }},
		{"invalid cycle", func(v *subInput) { v.BillingCycle = "weekly" }},
		{"invalid trial", func(v *subInput) { v.TrialEndsAt = "invalid" }},
		{"billing before trial", func(v *subInput) { v.TrialEndsAt = "2026-11-01" }},
		{"unknown currency", func(v *subInput) { v.Currency = "ZZZ" }},
		{"missing method", func(v *subInput) { v.PaymentMethodID = 999999 }},
		{"missing service template", func(v *subInput) { id := int64(999999); v.ServiceID = &id }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApplication(t)
			v := subscriptionTestInput()
			tc.edit(&v)
			w := callSubscriptionHandler(t, a.createSubscription, 0, v)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			var count int
			if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&count); err != nil || count != 0 {
				t.Fatal("invalid create changed subscription data")
			}
		})
	}
}

func TestSubscriptionArchivedPaymentCanBeRetainedButNotAssigned(t *testing.T) {
	a := newTestApplication(t)
	result, err := a.db.Exec(`INSERT INTO payment_methods(name) VALUES('기존 결제수단')`)
	if err != nil {
		t.Fatal(err)
	}
	method, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	v := subscriptionTestInput()
	v.PaymentMethodID = method
	id := createTestSubscription(t, a, v)
	if _, err := a.db.Exec(`UPDATE payment_methods SET archived=1 WHERE id=?`, method); err != nil {
		t.Fatal(err)
	}
	v.Memo = "메모만 수정"
	if w := callSubscriptionHandler(t, a.updateSubscription, id, v); w.Code != http.StatusOK {
		t.Fatalf("retained payment method update status = %d", w.Code)
	}
	if w := callSubscriptionHandler(t, a.createSubscription, 0, v); w.Code != http.StatusBadRequest {
		t.Fatalf("new archived method assignment status = %d", w.Code)
	}
	other := createTestSubscription(t, a, subscriptionTestInput())
	if w := callSubscriptionHandler(t, a.updateSubscription, other, v); w.Code != http.StatusBadRequest {
		t.Fatalf("changed archived method assignment status = %d", w.Code)
	}
}

func TestSubscriptionWritesRollbackWhenActivityFails(t *testing.T) {
	for _, event := range []string{"added", "price_changed", "cancelled"} {
		t.Run(event, func(t *testing.T) {
			a := newTestApplication(t)
			v := subscriptionTestInput()
			var id int64
			if event != "added" {
				id = createTestSubscription(t, a, v)
			}
			// The constant event comes from this test's fixed table, not a request.
			if _, err := a.db.Exec(`CREATE TRIGGER fail_activity BEFORE INSERT ON activity_events WHEN NEW.event_type='` + event + `' BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
				t.Fatal(err)
			}
			var w *httptest.ResponseRecorder
			switch event {
			case "added":
				w = callSubscriptionHandler(t, a.createSubscription, 0, v)
			case "price_changed":
				v.Amount = 2000
				w = callSubscriptionHandler(t, a.updateSubscription, id, v)
			case "cancelled":
				w = callSubscriptionHandler(t, a.cancelSubscription, id, nil)
			}
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", w.Code)
			}
			var subscriptions, history int
			if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&subscriptions); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscription_price_history`).Scan(&history); err != nil {
				t.Fatal(err)
			}
			if event == "added" {
				if subscriptions != 0 || history != 0 {
					t.Fatal("failed create left partial data")
				}
				return
			}
			var amount int64
			var status string
			var cancelledAt any
			if err := a.db.QueryRow(`SELECT amount,status,cancelled_at FROM subscriptions WHERE id=?`, id).Scan(&amount, &status, &cancelledAt); err != nil {
				t.Fatal(err)
			}
			if subscriptions != 1 || history != 1 || amount != 1000 || status != "active" || cancelledAt != nil {
				t.Fatal("failed mutation left partial data")
			}
		})
	}
}

func TestSubscriptionHistoryAndMissingResources(t *testing.T) {
	a := newTestApplication(t)
	v := subscriptionTestInput()
	id := createTestSubscription(t, a, v)
	v.Memo = "가격은 유지"
	if w := callSubscriptionHandler(t, a.updateSubscription, id, v); w.Code != http.StatusOK {
		t.Fatalf("update status = %d", w.Code)
	}
	var history int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscription_price_history WHERE subscription_id=?`, id).Scan(&history); err != nil || history != 1 {
		t.Fatal("non-price edit changed price history")
	}
	v.Amount, v.Currency = 1599, "USD"
	if w := callSubscriptionHandler(t, a.updateSubscription, id, v); w.Code != http.StatusOK {
		t.Fatalf("price update status = %d", w.Code)
	}
	var oldAmount, newAmount int64
	var oldCurrency, newCurrency string
	if err := a.db.QueryRow(`SELECT old_amount,old_currency,new_amount,new_currency FROM activity_events WHERE subscription_id=? AND event_type='price_changed'`, id).Scan(&oldAmount, &oldCurrency, &newAmount, &newCurrency); err != nil {
		t.Fatal(err)
	}
	if oldAmount != 1000 || oldCurrency != "KRW" || newAmount != 1599 || newCurrency != "USD" {
		t.Fatal("price activity lost the previous amount or currency")
	}
	if w := callSubscriptionHandler(t, a.cancelSubscription, id, nil); w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d", w.Code)
	}
	for _, missing := range []int64{id, 999999} {
		for _, request := range []struct {
			handler http.HandlerFunc
			body    any
		}{
			{a.updateSubscription, v},
			{a.cancelSubscription, nil},
			{a.skipSubscription, map[string]bool{"skipped": true}},
		} {
			if w := callSubscriptionHandler(t, request.handler, missing, request.body); w.Code != http.StatusNotFound {
				t.Fatalf("missing/inactive mutation status = %d", w.Code)
			}
		}
	}

}

func TestSubscriptionSkipUpsertTracksEditedBillingDate(t *testing.T) {
	a := newTestApplication(t)
	v := subscriptionTestInput()
	id := createTestSubscription(t, a, v)
	for _, skipped := range []bool{true, false} {
		if !skipped {
			v.BillingDate = "2026-10-15"
			if w := callSubscriptionHandler(t, a.updateSubscription, id, v); w.Code != http.StatusOK {
				t.Fatalf("billing date update status = %d", w.Code)
			}
		}
		if w := callSubscriptionHandler(t, a.skipSubscription, id, map[string]bool{"skipped": skipped}); w.Code != http.StatusOK {
			t.Fatalf("skip status = %d", w.Code)
		}
	}
	var count, skipped int
	var date string
	if err := a.db.QueryRow(`SELECT COUNT(*),skipped,scheduled_date FROM subscription_occurrences WHERE subscription_id=?`, id).Scan(&count, &skipped, &date); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(a.location)
	if count != 1 || skipped != 0 || date != now.Format("2006-01")+"-15" {
		t.Fatal("restoring occurrence kept a stale billing date or duplicate row")
	}
}

func TestConcurrentSubscriptionPricesPreserveHistoryChain(t *testing.T) {
	a := newTestApplication(t)
	id := createTestSubscription(t, a, subscriptionTestInput())
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, amount := range []int64{2000, 3000} {
		wg.Add(1)
		go func(amount int64) {
			defer wg.Done()
			v := subscriptionTestInput()
			v.Amount = amount
			statuses <- callSubscriptionHandler(t, a.updateSubscription, id, v).Code
		}(amount)
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("concurrent update status = %d", status)
		}
	}
	rows, err := a.db.Query(`SELECT old_amount,new_amount FROM activity_events WHERE subscription_id=? AND event_type='price_changed' ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	previous, count := int64(1000), 0
	for rows.Next() {
		var oldAmount, newAmount int64
		if err := rows.Scan(&oldAmount, &newAmount); err != nil {
			t.Fatal(err)
		}
		if oldAmount != previous {
			t.Fatal("concurrent update recorded a stale previous price")
		}
		previous = newAmount
		count++
	}
	if err := rows.Err(); err != nil || count != 2 {
		t.Fatal("incomplete concurrent price history")
	}
}

func TestSkipRequiresExplicitStatus(t *testing.T) {
	a := newTestApplication(t)
	id := createTestSubscription(t, a, subscriptionTestInput())
	if w := callSubscriptionHandler(t, a.skipSubscription, id, map[string]any{"skipped": true}); w.Code != http.StatusOK {
		t.Fatalf("skip status=%d", w.Code)
	}
	for _, body := range []any{map[string]any{}, map[string]any{"skipped": nil}} {
		w := callSubscriptionHandler(t, a.skipSubscription, id, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("missing skip state status=%d", w.Code)
		}
		var skipped bool
		var count int
		if err := a.db.QueryRow(`SELECT skipped,(SELECT COUNT(*) FROM subscription_occurrences) FROM subscription_occurrences WHERE subscription_id=?`, id).Scan(&skipped, &count); err != nil {
			t.Fatal(err)
		}
		if !skipped || count != 1 {
			t.Fatal("invalid request silently changed skipped occurrence")
		}
	}
	// An explicit false is still a valid unskip request.
	if w := callSubscriptionHandler(t, a.skipSubscription, id, map[string]any{"skipped": false}); w.Code != http.StatusOK {
		t.Fatalf("explicit false status=%d", w.Code)
	}
	var skipped bool
	if err := a.db.QueryRow(`SELECT skipped FROM subscription_occurrences WHERE subscription_id=?`, id).Scan(&skipped); err != nil || skipped {
		t.Fatal("explicit false did not unskip")
	}
}
