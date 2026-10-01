package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func benchmarkDashboardApplication(b *testing.B) *application {
	b.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(b.TempDir(), "bench.db")+"?_foreign_keys=on")
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	loc, _ := time.LoadLocation("Asia/Seoul")
	a := &application{db: db, location: loc}
	if err := a.migrate(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { a.db.Close() })
	tx, err := a.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 500 {
		res, err := tx.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES(?,1000,'KRW','monthly',15,'2020-01-15',1,'2020-01-01')`, fmt.Sprintf("service %d", i))
		if err != nil {
			b.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err = tx.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,1000,'KRW','2020-01-01')`, id); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return a
}

func BenchmarkDashboard500Subscriptions(b *testing.B) {
	a := benchmarkDashboardApplication(b)
	b.ResetTimer()
	for b.Loop() {
		if _, err := a.loadState(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkICS500Subscriptions(b *testing.B) {
	a := benchmarkDashboardApplication(b)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, a.location)
	end := start.AddDate(1, 0, -1)
	b.ResetTimer()
	for b.Loop() {
		if _, err := a.upcomingICS(start, end); err != nil {
			b.Fatal(err)
		}
	}
}
