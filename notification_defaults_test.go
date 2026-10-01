package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

func notificationToggleValues(t *testing.T, a *application) [3]bool {
	t.Helper()
	var values [3]bool
	if err := a.db.QueryRow(`SELECT discord_enabled,telegram_enabled,pwa_enabled FROM notification_channels WHERE id=1`).Scan(&values[0], &values[1], &values[2]); err != nil {
		t.Fatal(err)
	}
	return values
}

func TestFreshNotificationChannelsDefaultOff(t *testing.T) {
	a := newTestApplication(t)
	if got := notificationToggleValues(t, a); got != [3]bool{} {
		t.Fatal("new notification channels must be disabled")
	}
	rows, err := a.db.Query(`PRAGMA table_info(notification_channels)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	defaults := map[string]string{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var value sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &value, &pk); err != nil {
			t.Fatal(err)
		}
		defaults[name] = value.String
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"discord_enabled", "telegram_enabled", "pwa_enabled"} {
		if defaults[name] != "0" {
			t.Fatalf("%s schema default must be zero", name)
		}
	}
}

func TestNotificationMigrationPreservesEveryExplicitChannelChoice(t *testing.T) {
	a := newTestApplication(t)
	for mask := 0; mask < 8; mask++ {
		want := [3]bool{mask&1 != 0, mask&2 != 0, mask&4 != 0}
		migrationExec(t, a, `UPDATE notification_channels SET discord_enabled=?,telegram_enabled=?,pwa_enabled=? WHERE id=1`, want[0], want[1], want[2])
		for i := 0; i < 2; i++ {
			if err := a.migrate(); err != nil {
				t.Fatal(err)
			}
		}
		if notificationToggleValues(t, a) != want {
			t.Fatal("migration overwrote an explicit channel preference")
		}
	}
}

func TestLegacyNotificationMigrationPreservesConfiguredProvidersOnly(t *testing.T) {
	for _, tt := range []struct {
		name, discord, token, chat string
		want                       [3]bool
	}{
		{name: "unconfigured"},
		{name: "configured", discord: "https://discord.com/api/webhooks/123/test", token: "123:ABCDEFGHIJKLMNOPQRSTUVWXYZ", chat: "123", want: [3]bool{true, true, false}},
		{name: "incomplete telegram", token: "123:ABCDEFGHIJKLMNOPQRSTUVWXYZ"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestApplication(t)
			migrationExec(t, a, `DROP TABLE notification_channels; CREATE TABLE notification_channels(id INTEGER PRIMARY KEY CHECK(id=1),discord_webhook TEXT NOT NULL DEFAULT '',telegram_bot_token TEXT NOT NULL DEFAULT '',telegram_chat_id TEXT NOT NULL DEFAULT '',updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP); INSERT INTO notification_channels(id,discord_webhook,telegram_bot_token,telegram_chat_id) VALUES(1,?,?,?)`, tt.discord, tt.token, tt.chat)
			for i := 0; i < 2; i++ {
				if err := a.migrate(); err != nil {
					t.Fatal(err)
				}
			}
			if notificationToggleValues(t, a) != tt.want {
				t.Fatal("legacy configured provider behavior was not preserved")
			}
			var discord, token, chat string
			if err := a.db.QueryRow(`SELECT discord_webhook,telegram_bot_token,telegram_chat_id FROM notification_channels WHERE id=1`).Scan(&discord, &token, &chat); err != nil {
				t.Fatal(err)
			}
			if discord != tt.discord || token != tt.token || chat != tt.chat {
				t.Fatal("migration changed integration credentials")
			}
		})
	}
}

func TestPartialNotificationMigrationPreservesExistingToggle(t *testing.T) {
	a := newTestApplication(t)
	migrationExec(t, a, `UPDATE notification_channels SET discord_webhook='configured',discord_enabled=0,telegram_bot_token='configured',telegram_chat_id='configured'; ALTER TABLE notification_channels DROP COLUMN telegram_enabled; ALTER TABLE notification_channels DROP COLUMN pwa_enabled`)
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	if notificationToggleValues(t, a) != [3]bool{false, true, false} {
		t.Fatal("partial migration rewrote existing choice or enabled new PWA")
	}
}

func TestMissingNotificationRowDefaultsOffEvenOnLegacySchema(t *testing.T) {
	a := newTestApplication(t)
	migrationExec(t, a, `DROP TABLE notification_channels; CREATE TABLE notification_channels(id INTEGER PRIMARY KEY CHECK(id=1),discord_webhook TEXT NOT NULL DEFAULT '',discord_enabled INTEGER NOT NULL DEFAULT 1,telegram_bot_token TEXT NOT NULL DEFAULT '',telegram_chat_id TEXT NOT NULL DEFAULT '',telegram_enabled INTEGER NOT NULL DEFAULT 1,pwa_enabled INTEGER NOT NULL DEFAULT 1,updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	if notificationToggleValues(t, a) != [3]bool{} {
		t.Fatal("missing settings row inherited legacy enabled defaults")
	}
}

func TestNotificationToggleUpgradeRollsBackAndRetries(t *testing.T) {
	a := newTestApplication(t)
	migrationExec(t, a, `UPDATE notification_channels SET discord_webhook='configured'; ALTER TABLE notification_channels DROP COLUMN discord_enabled; ALTER TABLE notification_channels DROP COLUMN telegram_enabled; ALTER TABLE notification_channels DROP COLUMN pwa_enabled; CREATE TRIGGER fail_toggle_upgrade BEFORE UPDATE ON notification_channels BEGIN SELECT RAISE(ABORT,'fixture failure'); END`)
	if err := a.migrate(); err == nil {
		t.Fatal("injected toggle upgrade failure ignored")
	}
	rows, err := a.db.Query(`PRAGMA table_info(notification_channels)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var value sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &value, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "discord_enabled" {
			t.Fatal("failed upgrade left a partially initialized toggle")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	migrationExec(t, a, `DROP TRIGGER fail_toggle_upgrade`)
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	if notificationToggleValues(t, a) != [3]bool{true, false, false} {
		t.Fatal("retry did not preserve legacy provider behavior")
	}
}

func TestNotificationChoicesPersistThroughSettingsMigrationAndCredentialBackup(t *testing.T) {
	a := newTestApplication(t)
	r, w := jsonRequest(t, http.MethodPut, "/api/settings", map[string]any{"Name": "사용자", "Currency": "KRW", "DiscordEnabled": true, "TelegramEnabled": false, "PWAEnabled": true, "NotifyUpcoming": true, "NotifyDays": 3})
	a.updateSettings(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("settings status=%d", w.Code)
	}
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	export := httptest.NewRecorder()
	a.exportData(export, httptest.NewRequest(http.MethodGet, "/api/data/export?includeNotificationCredentials=true", nil))
	if export.Code != http.StatusOK {
		t.Fatalf("export status=%d", export.Code)
	}
	migrationExec(t, a, `UPDATE notification_channels SET discord_enabled=0,telegram_enabled=1,pwa_enabled=0`)
	request := httptest.NewRequest(http.MethodPost, "/api/data/import", export.Body)
	response := httptest.NewRecorder()
	a.importData(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("restore status=%d", response.Code)
	}
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	if notificationToggleValues(t, a) != [3]bool{true, false, true} {
		t.Fatal("settings or backup roundtrip lost explicit choices")
	}
}
