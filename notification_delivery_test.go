package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func configureTestDiscord(t *testing.T, a *application) {
	t.Helper()
	if _, err := a.db.Exec(`UPDATE notification_channels SET discord_webhook='https://discord.com/api/webhooks/123456789012345678/test_fixture_token',discord_enabled=1,telegram_enabled=0,pwa_enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationDeliveryRetriesFailureThenDeduplicates(t *testing.T) {
	a := newTestApplication(t)
	configureTestDiscord(t, a)
	calls := 0
	a.notificationHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		status := http.StatusNoContent
		if calls == 1 {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{StatusCode: status, Status: "provider private text", Body: io.NopCloser(strings.NewReader("ignored")), Header: make(http.Header)}, nil
	})}
	notification := upcomingNotification{Days: 3, Items: []string{"테스트 (₩1,000)"}}
	a.deliverOnce("test-retry", notification)
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM notification_deliveries`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed delivery was permanently deduplicated")
	}
	a.deliverOnce("test-retry", notification)
	a.deliverOnce("test-retry", notification)
	if calls != 2 {
		t.Fatalf("delivery requests = %d, want 2", calls)
	}
}

func TestScheduledNotificationUsesDueMonthOccurrence(t *testing.T) {
	for _, skipDue := range []bool{false, true} {
		t.Run(map[bool]string{false: "current month skip does not suppress next month", true: "due month skip suppresses reminder"}[skipDue], func(t *testing.T) {
			a := newTestApplication(t)
			configureTestDiscord(t, a)
			if _, err := a.db.Exec(`UPDATE notification_rules SET notify_upcoming=1,days_before=3 WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			v := subscriptionTestInput()
			v.BillingDate = "2026-10-02"
			id := createTestSubscription(t, a, v)
			if _, err := a.db.Exec(`INSERT INTO subscription_occurrences(subscription_id,period,scheduled_date,amount,currency,skipped) VALUES(?,'2026-10','2026-10-02',1000,'KRW',1),(?,'2026-11','2026-11-02',1599,'USD',?)`, id, id, skipDue); err != nil {
				t.Fatal(err)
			}
			calls := 0
			a.notificationHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body), "$15.99") {
					t.Fatal("reminder did not use due occurrence price")
				}
				return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
			})}
			now := time.Date(2026, 10, 30, 12, 0, 0, 0, a.location)
			state, err := a.loadStateAt(now)
			if err != nil {
				t.Fatal(err)
			}
			expectedUpcoming := 1
			if skipDue {
				expectedUpcoming = 0
			}
			if state.Stats.UpcomingCount != expectedUpcoming {
				t.Fatal("upcoming count used the wrong month skip")
			}
			a.runScheduledNotificationsAt(now)
			a.runScheduledNotificationsAt(now)
			want := 1
			if skipDue {
				want = 0
			}
			if calls != want {
				t.Fatalf("requests = %d, want %d", calls, want)
			}
		})
	}
}
