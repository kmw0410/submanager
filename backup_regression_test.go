package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func exportedBackup(t *testing.T, a *application) dataBackup {
	t.Helper()
	w := httptest.NewRecorder()
	a.exportData(w, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("export status=%d", w.Code)
	}
	var b dataBackup
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal("backup decode failed")
	}
	return b
}

func TestInvalidBackupPreservesApplicationData(t *testing.T) {
	a := newTestApplication(t)
	if _, err := a.db.Exec(`UPDATE users SET name='original',password_hash='preserve-hash';INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES(100,'original',100,'KRW','monthly',1,'2026-01-01',1,'2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	original := exportedBackup(t, a)
	for _, tt := range []struct {
		name   string
		mutate func(*dataBackup)
	}{
		{"short anchor", func(b *dataBackup) { b.Subscriptions[0].BillingAnchor = "x" }},
		{"bad cancellation", func(b *dataBackup) { b.Subscriptions[0].CancelledAt = "x" }},
		{"negative amount", func(b *dataBackup) { b.Subscriptions[0].Amount = -1 }},
		{"missing payment", func(b *dataBackup) { b.Subscriptions[0].PaymentMethodID = 99999 }},
		{"duplicate subscription", func(b *dataBackup) { b.Subscriptions = append(b.Subscriptions, b.Subscriptions[0]) }},
		{"missing settings", func(b *dataBackup) { b.Settings.Name = "" }},
		{"unsupported version", func(b *dataBackup) { b.Version = 999 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload, _ := json.Marshal(original)
			var b dataBackup
			_ = json.Unmarshal(payload, &b)
			tt.mutate(&b)
			r, w := jsonRequest(t, http.MethodPost, "/api/data/import", b)
			a.importData(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d", w.Code)
			}
			var name, hash, service string
			if err := a.db.QueryRow(`SELECT name,password_hash,(SELECT service_name FROM subscriptions WHERE id=100) FROM users WHERE id=1`).Scan(&name, &hash, &service); err != nil {
				t.Fatal(err)
			}
			if name != "original" || hash != "preserve-hash" || service != "original" {
				t.Fatal("invalid import changed stored data")
			}
		})
	}
	payload, _ := json.Marshal(original)
	for _, body := range []string{string(payload) + `{}`, string(payload[:len(payload)/2])} {
		w := httptest.NewRecorder()
		a.importData(w, httptest.NewRequest(http.MethodPost, "/api/data/import", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid JSON status=%d", w.Code)
		}
	}
}

func TestBackupFailureRollsBackReplacement(t *testing.T) {
	a := newTestApplication(t)
	b := exportedBackup(t, a)
	b.Settings.Name = "replacement"
	if _, err := a.db.Exec(`CREATE TRIGGER reject_restore BEFORE UPDATE ON users BEGIN SELECT RAISE(ABORT,'restore blocked'); END`); err != nil {
		t.Fatal(err)
	}
	r, w := jsonRequest(t, http.MethodPost, "/api/data/import", b)
	a.importData(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
	after := exportedBackup(t, a)
	if after.Settings.Name != "사용자" {
		t.Fatal("failed restore changed profile")
	}
}

func TestBackupRejectsBrokenRelationsAndInactiveDefaultCurrency(t *testing.T) {
	a := newTestApplication(t)
	if _, err := a.db.Exec(`INSERT INTO currencies(id,code,name,is_builtin,archived) VALUES(100,'GBP','Pound',0,1); INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,payment_method_id,started_at) VALUES(100,'Original',100,'KRW','monthly',1,1,'2026-01-01'); INSERT INTO subscription_occurrences(id,subscription_id,period,scheduled_date,amount,currency) VALUES(100,100,'2026-01','2026-01-01',100,'KRW'); INSERT INTO subscription_price_history(id,subscription_id,amount,currency,effective_from) VALUES(100,100,100,'KRW','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	original := exportedBackup(t, a)
	for _, tt := range []struct {
		name   string
		mutate func(*dataBackup)
	}{
		{"archived default", func(b *dataBackup) { b.Settings.Currency = "GBP" }},
		{"unknown currency", func(b *dataBackup) { b.Subscriptions[0].Currency = "ZZZ" }},
		{"orphan occurrence", func(b *dataBackup) { b.Occurrences[0].SubscriptionID = 999 }},
		{"orphan history", func(b *dataBackup) { b.PriceHistory[0].SubscriptionID = 999 }},
		{"duplicate period", func(b *dataBackup) { v := b.Occurrences[0]; v.ID++; b.Occurrences = append(b.Occurrences, v) }},
		{"date period mismatch", func(b *dataBackup) { b.Occurrences[0].ScheduledDate = "2026-02-01" }},
		{"missing service", func(b *dataBackup) { id := int64(999); b.Subscriptions[0].ServiceID = &id }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(original)
			var b dataBackup
			if err := json.Unmarshal(data, &b); err != nil {
				t.Fatal(err)
			}
			tt.mutate(&b)
			r, w := jsonRequest(t, http.MethodPost, "/api/data/import", b)
			a.importData(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d", w.Code)
			}
			after := exportedBackup(t, a)
			after.ExportedAt = original.ExportedAt
			afterJSON, _ := json.Marshal(after)
			if string(afterJSON) != string(data) {
				t.Fatal("rejected backup changed persisted application data")
			}
		})
	}
}

func TestBackupRoundTripPreservesUnicodeNamesAndAuthentication(t *testing.T) {
	a := newTestApplication(t)
	if _, err := a.db.Exec(`INSERT INTO payment_methods(name,type) VALUES('Ä payment','custom'),('ä payment','custom'); UPDATE users SET password_hash='private-hash',email='admin@example.com',is_admin=1; INSERT INTO sessions(user_id,token_hash,expires_at) VALUES(1,'private-session-hash','2099-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	original := exportedBackup(t, a)
	for i := 0; i < 2; i++ {
		r, w := jsonRequest(t, http.MethodPost, "/api/data/import", original)
		a.importData(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("roundtrip status=%d", w.Code)
		}
		if err := a.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	after := exportedBackup(t, a)
	after.ExportedAt = original.ExportedAt
	first, _ := json.Marshal(original)
	second, _ := json.Marshal(after)
	if string(first) != string(second) {
		t.Fatal("repeated restore/migration changed backup data")
	}
	for _, secret := range []string{"private-hash", "private-session-hash", "admin@example.com"} {
		if strings.Contains(string(second), secret) {
			t.Fatal("backup contains administrator authentication data")
		}
	}
	var hash, email, token string
	if err := a.db.QueryRow(`SELECT password_hash,email,(SELECT token_hash FROM sessions LIMIT 1) FROM users WHERE id=1`).Scan(&hash, &email, &token); err != nil {
		t.Fatal(err)
	}
	if hash != "private-hash" || email != "admin@example.com" || token != "private-session-hash" {
		t.Fatal("restore replaced authentication data")
	}
}

func TestBackupDatabaseFailureDoesNotLogPrivateStoredValues(t *testing.T) {
	a := newTestApplication(t)
	const secret = "private-backup-value"

	// This legacy schema permits malformed money; the resulting Scan error contains its value.
	if _, err := a.db.Exec(`INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,payment_method_id,started_at) VALUES(100,'Original',100,'KRW','monthly',1,1,'2026-01-01'); INSERT INTO subscription_occurrences(subscription_id,period,scheduled_date,amount,currency) VALUES(100,'2026-01','2026-01-01',?,'KRW')`, secret); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldWriter) })
	w := httptest.NewRecorder()
	a.exportData(w, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(w.Body.String(), secret) {
		t.Fatal("backup failure exposed a private stored value")
	}
}

func TestSupportedBackupVersionsKeepMoneyAndLegacyDateCompatibility(t *testing.T) {
	for version := 1; version <= 5; version++ {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			a := newTestApplication(t)
			if _, err := a.db.Exec(`INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,payment_method_id,started_at) VALUES(100,'Legacy',1000,'USD','monthly',1,1,'2026-01-01 12:00:00'); INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(100,1000,'USD','2026-01-01')`); err != nil {
				t.Fatal(err)
			}
			b := exportedBackup(t, a)
			b.Version = version
			if version == 1 {
				b.Subscriptions[0].Amount = 10
				b.PriceHistory[0].Amount = 10
			}
			r, w := jsonRequest(t, http.MethodPost, "/api/data/import", b)
			a.importData(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("version %d import status=%d", version, w.Code)
			}
			if err := a.migrate(); err != nil {
				t.Fatal(err)
			}
			after := exportedBackup(t, a)
			if after.Subscriptions[0].Amount != 1000 || after.PriceHistory[0].Amount != 1000 {
				t.Fatal("legacy migration altered restored minor-unit money")
			}
			if after.Subscriptions[0].BillingAnchor != "" || after.Subscriptions[0].StartedAt != "2026-01-01 12:00:00" {
				t.Fatal("legacy timestamp or optional billing anchor changed")
			}
		})
	}
}
