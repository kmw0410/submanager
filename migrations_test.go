package main

import (
	"testing"
)

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
