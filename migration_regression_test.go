package main

import (
	"testing"
)

func migrationExec(t *testing.T, a *application, query string, args ...any) {
	t.Helper()
	if _, err := a.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestMinorUnitMigrationNormalizesCurrencyBeforeDeduplication(t *testing.T) {
	a := newTestApplication(t)
	migrationExec(t, a, `INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,payment_method_id,started_at) VALUES(100,'Upper',12,'USD','monthly',10,1,'2026-01-01'),(101,'Lower',34,'usd','monthly',10,1,'2026-01-01'); INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(100,12,'Usd','2026-01-01'); INSERT INTO subscription_occurrences(subscription_id,period,scheduled_date,amount,currency) VALUES(101,'2026-01','2026-01-10',34,'uSD'); INSERT INTO activity_events(subscription_id,event_type,service_name,old_amount,old_currency,new_amount,new_currency) VALUES(100,'price_changed','Upper',11,'USd',12,'usD'); DELETE FROM app_metadata WHERE key='amounts_minor_units_v1'`)
	for i := 0; i < 2; i++ {
		if err := a.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range []struct {
		query string
		want  int64
	}{
		{`SELECT amount FROM subscriptions WHERE id=100`, 1200},
		{`SELECT amount FROM subscriptions WHERE id=101`, 3400},
		{`SELECT amount FROM subscription_price_history WHERE subscription_id=100`, 1200},
		{`SELECT amount FROM subscription_occurrences WHERE subscription_id=101`, 3400},
		{`SELECT old_amount FROM activity_events WHERE subscription_id=100`, 1100},
		{`SELECT new_amount FROM activity_events WHERE subscription_id=100`, 1200},
	} {
		var got int64
		if err := a.db.QueryRow(check.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Fatalf("%s: got %d want %d", check.query, got, check.want)
		}
	}
}

func TestMinorUnitMigrationRollsBackAndCanRetry(t *testing.T) {
	a := newTestApplication(t)
	migrationExec(t, a, `INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,payment_method_id,started_at) VALUES(100,'Legacy',12,'USD','monthly',10,1,'2026-01-01'); INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(100,12,'USD','2026-01-01'); DELETE FROM app_metadata WHERE key='amounts_minor_units_v1'; CREATE TRIGGER reject_price_upgrade BEFORE UPDATE ON subscription_price_history BEGIN SELECT RAISE(ABORT,'injected migration failure'); END`)
	if err := a.migrate(); err == nil {
		t.Fatal("injected migration failure must be returned")
	}
	var amount, marker int64
	if err := a.db.QueryRow(`SELECT amount FROM subscriptions WHERE id=100`).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM app_metadata WHERE key='amounts_minor_units_v1'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if amount != 12 || marker != 0 {
		t.Fatalf("failed migration changed amount or completion marker: amount=%d marker=%d", amount, marker)
	}
	migrationExec(t, a, `DROP TRIGGER reject_price_upgrade`)
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT amount FROM subscriptions WHERE id=100`).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != 1200 {
		t.Fatalf("retry amount=%d want 1200", amount)
	}
}

func TestMigrateRepairsPartialAdditiveSchemaWithoutChangingData(t *testing.T) {
	a := newTestApplication(t)
	migrationExec(t, a, `UPDATE users SET name='기존 사용자',email='old@example.com',password_hash='preserved-hash',is_admin=1; INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,payment_method_id,started_at) VALUES(100,'Existing',5000,'KRW','monthly',31,1,'2026-01-01'); ALTER TABLE subscriptions DROP COLUMN billing_anchor; ALTER TABLE subscriptions DROP COLUMN trial_ends_at; ALTER TABLE services DROP COLUMN supports_trial; ALTER TABLE sessions DROP COLUMN user_agent; ALTER TABLE notification_channels DROP COLUMN telegram_enabled; DELETE FROM notification_rules`)
	for i := 0; i < 2; i++ {
		if err := a.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	var name, email, hash, anchor, trial string
	var amount int64
	if err := a.db.QueryRow(`SELECT name,email,password_hash FROM users WHERE id=1`).Scan(&name, &email, &hash); err != nil {
		t.Fatal(err)
	}
	if name != "기존 사용자" || email != "old@example.com" || hash != "preserved-hash" {
		t.Fatal("migration changed existing administrator")
	}
	if err := a.db.QueryRow(`SELECT amount,billing_anchor,trial_ends_at FROM subscriptions WHERE id=100`).Scan(&amount, &anchor, &trial); err != nil {
		t.Fatal(err)
	}
	if amount != 5000 || anchor != "" || trial != "" {
		t.Fatal("migration changed subscription data")
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscription_price_history WHERE subscription_id=100`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("price history rows=%d want 1", count)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM notification_rules WHERE id=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("missing notification settings were not repaired")
	}
	rows, err := a.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left invalid foreign keys")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMinorUnitMigrationRejectsOverflowWithoutLosingIntegerMoney(t *testing.T) {
	a := newTestApplication(t)
	const amount int64 = 92233720368547759
	migrationExec(t, a, `INSERT INTO subscriptions(id,service_name,amount,currency,billing_cycle,billing_day,payment_method_id,started_at) VALUES(100,'Legacy',?,'USD','monthly',10,1,'2026-01-01'); DELETE FROM app_metadata WHERE key='amounts_minor_units_v1'`, amount)
	if err := a.migrate(); err == nil {
		t.Fatal("overflowing minor unit conversion must fail")
	}
	var got int64
	var storage string
	var marker int
	if err := a.db.QueryRow(`SELECT amount,typeof(amount) FROM subscriptions WHERE id=100`).Scan(&got, &storage); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM app_metadata WHERE key='amounts_minor_units_v1'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if got != amount || storage != "integer" || marker != 0 {
		t.Fatal("failed overflow conversion changed amount or completion state")
	}
}
