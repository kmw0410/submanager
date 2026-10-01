package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestBackupActivityCurrenciesAreValidatedAtomically(t *testing.T) {
	a := newTestApplication(t)
	if _, err := a.db.Exec(`INSERT INTO activity_events(event_type,service_name,new_amount,new_currency) VALUES('added','original',100,'KRW')`); err != nil {
		t.Fatal(err)
	}
	original := exportedBackup(t, a)
	for _, field := range []string{"old", "new"} {
		for _, code := range []string{"US", "한글A", "ZZZ"} {
			t.Run(field+" "+code, func(t *testing.T) {
				payload, err := json.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				var backup dataBackup
				if err := json.Unmarshal(payload, &backup); err != nil {
					t.Fatal(err)
				}
				invalid := code
				if field == "old" {
					backup.Activities[0].OldCurrency = &invalid
				} else {
					backup.Activities[0].NewCurrency = &invalid
				}
				backup.Settings.Name = "replacement"
				request, recorder := jsonRequest(t, http.MethodPost, "/api/data/import", backup)
				a.importData(recorder, request)
				if recorder.Code != http.StatusBadRequest {
					t.Fatalf("status=%d want 400", recorder.Code)
				}
				after := exportedBackup(t, a)
				after.ExportedAt = original.ExportedAt
				actual, err := json.Marshal(after)
				if err != nil {
					t.Fatal(err)
				}
				if string(actual) != string(payload) {
					t.Fatal("invalid activity currency changed persisted data")
				}
			})
		}
	}
}

func TestBackupActivityRetainsArchivedCurrencyAndNormalizesCase(t *testing.T) {
	a := newTestApplication(t)
	if _, err := a.db.Exec(`INSERT INTO currencies(code,name,is_builtin,archived) VALUES('GBP','GBP',0,1); INSERT INTO activity_events(event_type,service_name,old_amount,old_currency,new_amount,new_currency) VALUES('price_changed','history',100,'gbp',200,'krw')`); err != nil {
		t.Fatal(err)
	}
	backup := exportedBackup(t, a)
	request, recorder := jsonRequest(t, http.MethodPost, "/api/data/import", backup)
	a.importData(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", recorder.Code)
	}
	var oldCode, newCode string
	if err := a.db.QueryRow(`SELECT old_currency,new_currency FROM activity_events`).Scan(&oldCode, &newCode); err != nil {
		t.Fatal(err)
	}
	if oldCode != "GBP" || newCode != "KRW" {
		t.Fatal("activity currencies were not normalized")
	}
	var archived bool
	if err := a.db.QueryRow(`SELECT archived FROM currencies WHERE code='GBP'`).Scan(&archived); err != nil || !archived {
		t.Fatal("historical currency archive was lost")
	}
}
