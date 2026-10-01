package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
)

func TestSubscriptionEditorRejectedUpdatesPreserveCompleteSavedRecord(t *testing.T) {
	a := newTestApplication(t)
	original := subscriptionTestInput()
	original.Icon = "T"
	original.Color = "#D4D4D8"
	original.Category = "AI"
	original.Memo = "existing memo"
	original.TrialEndsAt = "2026-10-30"
	id := createTestSubscription(t, a, original)
	before := exportedBackup(t, a)
	cookieResponse := httptest.NewRecorder()
	if err := a.createSession(cookieResponse, httptest.NewRequest(http.MethodPost, "/auth/login", nil), 1); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		edit func(*subInput)
	}{
		{"invalid billing date", func(v *subInput) { v.BillingDate = "2026-02-30" }},
		{"trial after billing", func(v *subInput) { v.TrialEndsAt = "2026-11-01" }},
		{"negative money", func(v *subInput) { v.Amount = -1 }},
		{"missing payment", func(v *subInput) { v.PaymentMethodID = 999999 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			draft := original
			draft.ServiceName = "unsaved rename"
			draft.Category = "unsaved category"
			draft.Memo = "unsaved memo"
			tt.edit(&draft)
			request, response := jsonRequest(t, http.MethodPut, "/api/subscriptions/"+strconv.FormatInt(id, 10), draft)
			request.AddCookie(cookieResponse.Result().Cookies()[0])
			a.routes().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("rejected edit status=%d", response.Code)
			}
			var errorBody map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &errorBody); err != nil {
				t.Fatal("editor error must remain JSON")
			}
			if len(errorBody) != 1 || errorBody["error"] == "" {
				t.Fatal("editor validation error contract changed")
			}
			after := exportedBackup(t, a)
			after.ExportedAt = before.ExportedAt
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed editor update modified saved fields or history")
			}
		})
	}
}

func TestSubscriptionEditorArchivedCurrencyCannotBeImplicitlyReassigned(t *testing.T) {
	a := newTestApplication(t)
	migrationExec(t, a, `INSERT INTO currencies(code,name,is_builtin) VALUES('GBP','Pound',0)`)
	original := subscriptionTestInput()
	original.Currency = "GBP"
	id := createTestSubscription(t, a, original)
	migrationExec(t, a, `UPDATE currencies SET archived=1 WHERE code='GBP'`)
	draft := original
	draft.Memo = "only a memo edit"
	response := callSubscriptionHandler(t, a.updateSubscription, id, draft)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("inactive currency status=%d", response.Code)
	}
	var amount int64
	var currency, memo string
	if err := a.db.QueryRow(`SELECT amount,currency,memo FROM subscriptions WHERE id=?`, id).Scan(&amount, &currency, &memo); err != nil {
		t.Fatal(err)
	}
	if amount != original.Amount || currency != "GBP" || memo != "" {
		t.Fatal("rejected inactive-currency edit changed saved data")
	}
	// Choosing an active replacement is an explicit currency/price change.
	draft.Currency = "USD"
	response = callSubscriptionHandler(t, a.updateSubscription, id, draft)
	if response.Code != http.StatusOK {
		t.Fatalf("explicit active replacement status=%d", response.Code)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscription_price_history WHERE subscription_id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("currency replacement did not preserve price history")
	}
}
