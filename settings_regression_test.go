package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func settingsClient(t *testing.T, a *application) func(string, string, any) *httptest.ResponseRecorder {
	t.Helper()
	session := httptest.NewRecorder()
	if err := a.createSession(session, httptest.NewRequest(http.MethodPost, "/auth/login", nil), 1); err != nil {
		t.Fatal(err)
	}
	cookie := session.Result().Cookies()[0]
	return func(method, target string, body any) *httptest.ResponseRecorder {
		t.Helper()
		request, recorder := jsonRequest(t, method, target, body)
		request.AddCookie(cookie)
		a.routes().ServeHTTP(recorder, request)
		return recorder
	}
}

func TestSettingsCatalogValidationAndBuiltinBoundaries(t *testing.T) {
	a := newTestApplication(t)
	request := settingsClient(t, a)
	var builtinMethod, builtinCurrency int64
	if err := a.db.QueryRow(`SELECT id FROM payment_methods WHERE is_builtin=1 LIMIT 1`).Scan(&builtinMethod); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id FROM currencies WHERE code='KRW'`).Scan(&builtinCurrency); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, method, path string
		body               any
		status             int
	}{
		{"empty payment", "POST", "/api/payment-methods", map[string]any{"name": " "}, 400},
		{"unknown payment field", "POST", "/api/payment-methods", map[string]any{"name": "custom", "extra": true}, 400},
		{"invalid currency letters", "POST", "/api/currencies", map[string]any{"code": "한글A"}, 400},
		{"invalid currency length", "POST", "/api/currencies", map[string]any{"code": "US"}, 400},
		{"duplicate currency", "POST", "/api/currencies", map[string]any{"code": " krw "}, 400},
		{"builtin payment rename", "PUT", "/api/payment-methods/" + strconv.FormatInt(builtinMethod, 10), map[string]any{"name": "changed"}, 403},
		{"builtin payment delete", "DELETE", "/api/payment-methods/" + strconv.FormatInt(builtinMethod, 10), nil, 403},
		{"builtin currency delete", "DELETE", "/api/currencies/" + strconv.FormatInt(builtinCurrency, 10), nil, 403},
		{"missing payment rename", "PUT", "/api/payment-methods/99999", map[string]any{"name": "changed"}, 404},
		{"missing payment delete", "DELETE", "/api/payment-methods/99999", nil, 404},
		{"missing currency delete", "DELETE", "/api/currencies/99999", nil, 404},
		{"invalid payment id", "DELETE", "/api/payment-methods/0", nil, 400},
		{"invalid currency id", "DELETE", "/api/currencies/nope", nil, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := request(tc.method, tc.path, tc.body).Code; got != tc.status {
				t.Fatalf("status=%d want %d", got, tc.status)
			}
		})
	}
}

func TestCustomPaymentLifecyclePreservesCancelledSubscriptions(t *testing.T) {
	a := newTestApplication(t)
	request := settingsClient(t, a)
	if got := request("POST", "/api/payment-methods", map[string]any{"name": "  custom  "}).Code; got != 201 {
		t.Fatalf("create=%d", got)
	}
	var id int64
	if err := a.db.QueryRow(`SELECT id FROM payment_methods WHERE name='custom'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := "/api/payment-methods/" + strconv.FormatInt(id, 10)
	if got := request("PUT", path, map[string]any{"name": "renamed"}).Code; got != 200 {
		t.Fatalf("rename=%d", got)
	}
	if got := request("POST", "/api/payment-methods", map[string]any{"name": "renamed"}).Code; got != 400 {
		t.Fatalf("duplicate=%d", got)
	}
	if _, err := a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,status) VALUES('history',100,'KRW','monthly',1,'2026-10-01',?,'cancelled')`, id); err != nil {
		t.Fatal(err)
	}
	if got := request("DELETE", path, nil).Code; got != 200 {
		t.Fatalf("archive=%d", got)
	}
	var archived, count int
	if err := a.db.QueryRow(`SELECT archived FROM payment_methods WHERE id=?`, id).Scan(&archived); err != nil || archived != 1 {
		t.Fatalf("archive flag=%d err=%v", archived, err)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE payment_method_id=?`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("historical reference count=%d err=%v", count, err)
	}
	if got := request("PUT", path, map[string]any{"name": "again"}).Code; got != 404 {
		t.Fatalf("archived rename=%d", got)
	}
	if got := request("DELETE", path, nil).Code; got != 404 {
		t.Fatalf("archived delete=%d", got)
	}
	if got := request("POST", "/api/payment-methods", map[string]any{"name": "unused"}).Code; got != 201 {
		t.Fatalf("unused create=%d", got)
	}
	if err := a.db.QueryRow(`SELECT id FROM payment_methods WHERE name='unused'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if got := request("DELETE", "/api/payment-methods/"+strconv.FormatInt(id, 10), nil).Code; got != 200 {
		t.Fatalf("unused delete=%d", got)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM payment_methods WHERE id=?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unused count=%d err=%v", count, err)
	}
}

func TestCustomCurrencyRetainsEveryHistoricalReference(t *testing.T) {
	for _, reference := range []string{"subscription", "price history", "occurrence", "old activity", "new activity", "service template", "unused"} {
		t.Run(reference, func(t *testing.T) {
			a := newTestApplication(t)
			request := settingsClient(t, a)
			if got := request("POST", "/api/currencies", map[string]any{"code": " gbp "}).Code; got != 201 {
				t.Fatalf("create=%d", got)
			}
			var id, methodID int64
			if err := a.db.QueryRow(`SELECT id FROM currencies WHERE code='GBP'`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(`SELECT id FROM payment_methods LIMIT 1`).Scan(&methodID); err != nil {
				t.Fatal(err)
			}
			res, err := a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id) VALUES('history',100,'KRW','monthly',1,'2026-10-01',?)`, methodID)
			if err != nil {
				t.Fatal(err)
			}
			subscriptionID, err := res.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			switch reference {
			case "subscription":
				_, err = a.db.Exec(`UPDATE subscriptions SET currency='GBP' WHERE id=?`, subscriptionID)
			case "price history":
				_, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,100,'GBP','2026-09-01')`, subscriptionID)
			case "occurrence":
				_, err = a.db.Exec(`INSERT INTO subscription_occurrences(subscription_id,period,scheduled_date,amount,currency) VALUES(?,'2026-10','2026-10-01',100,'GBP')`, subscriptionID)
			case "old activity":
				_, err = a.db.Exec(`INSERT INTO activity_events(event_type,service_name,old_currency) VALUES('price_changed','history','GBP')`)
			case "new activity":
				_, err = a.db.Exec(`INSERT INTO activity_events(event_type,service_name,new_currency) VALUES('price_changed','history','GBP')`)
			case "service template":
				_, err = a.db.Exec(`UPDATE services SET default_currency='GBP' WHERE id=(SELECT id FROM services LIMIT 1)`)
			}
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/currencies/" + strconv.FormatInt(id, 10)
			if got := request("DELETE", path, nil).Code; got != 200 {
				t.Fatalf("delete=%d", got)
			}
			var count, archived int
			if reference == "unused" {
				if err := a.db.QueryRow(`SELECT COUNT(*) FROM currencies WHERE id=?`, id).Scan(&count); err != nil || count != 0 {
					t.Fatalf("unused count=%d err=%v", count, err)
				}
			} else {
				if err := a.db.QueryRow(`SELECT archived FROM currencies WHERE id=?`, id).Scan(&archived); err != nil || archived != 1 {
					t.Fatalf("historical currency archive=%d err=%v", archived, err)
				}
				if got := request("DELETE", path, nil).Code; got != 404 {
					t.Fatalf("archived delete=%d", got)
				}
			}
		})
	}
}

func validSettingsInput() map[string]any {
	return map[string]any{"name": "changed", "currency": "KRW", "notifyDays": 7, "notifyUpcoming": true}
}

func TestDefaultCurrencyMustBeChangedBeforeDeletion(t *testing.T) {
	a := newTestApplication(t)
	request := settingsClient(t, a)
	if got := request("POST", "/api/currencies", map[string]any{"code": "GBP"}).Code; got != 201 {
		t.Fatalf("create=%d", got)
	}
	body := validSettingsInput()
	body["currency"] = " gbp "
	if got := request("PUT", "/api/settings", body).Code; got != 200 {
		t.Fatalf("settings=%d", got)
	}
	var id int64
	if err := a.db.QueryRow(`SELECT id FROM currencies WHERE code='GBP'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := "/api/currencies/" + strconv.FormatInt(id, 10)
	if got := request("DELETE", path, nil).Code; got != 403 {
		t.Fatalf("delete active default=%d", got)
	}
	var archived int
	if err := a.db.QueryRow(`SELECT archived FROM currencies WHERE id=?`, id).Scan(&archived); err != nil || archived != 0 {
		t.Fatalf("default archived=%d err=%v", archived, err)
	}
	body["currency"] = "KRW"
	if got := request("PUT", "/api/settings", body).Code; got != 200 {
		t.Fatalf("change default=%d", got)
	}
	if got := request("DELETE", path, nil).Code; got != 200 {
		t.Fatalf("delete former default=%d", got)
	}
}

func TestSettingsValidationAndTransactionRollback(t *testing.T) {
	a := newTestApplication(t)
	request := settingsClient(t, a)
	for _, field := range []string{"name", "currency", "notifyDays"} {
		t.Run(field, func(t *testing.T) {
			body := validSettingsInput()
			switch field {
			case "name":
				body[field] = " "
			case "currency":
				body[field] = "ZZZ"
			case "notifyDays":
				body[field] = 31
			}
			if got := request("PUT", "/api/settings", body).Code; got != 400 {
				t.Fatalf("invalid settings=%d", got)
			}
		})
	}
	var originalName string
	if err := a.db.QueryRow(`SELECT name FROM users WHERE id=1`).Scan(&originalName); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`CREATE TRIGGER reject_settings BEFORE UPDATE ON notification_rules BEGIN SELECT RAISE(ABORT,'forced failure'); END`); err != nil {
		t.Fatal(err)
	}
	if got := request("PUT", "/api/settings", validSettingsInput()).Code; got != 500 {
		t.Fatalf("failed transaction=%d", got)
	}
	var currentName string
	if err := a.db.QueryRow(`SELECT name FROM users WHERE id=1`).Scan(&currentName); err != nil {
		t.Fatal(err)
	}
	if currentName != originalName {
		t.Fatal("settings failure partially updated the administrator")
	}
}
