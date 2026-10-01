package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeTimezoneIsExcludedFromStateAndBackup(t *testing.T) {
	a := newTestApplication(t)

	state, err := a.loadState()
	if err != nil {
		t.Fatal(err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(stateJSON)), "timezone") {
		t.Fatalf("runtime timezone leaked into application state: %s", stateJSON)
	}

	recorder := httptest.NewRecorder()
	a.exportData(recorder, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(strings.ToLower(recorder.Body.String()), "timezone") {
		t.Fatalf("runtime timezone leaked into backup: %s", recorder.Body.String())
	}

	var legacyBackup map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &legacyBackup); err != nil {
		t.Fatal(err)
	}
	legacyBackup["settings"].(map[string]any)["timezone"] = "UTC"
	legacyJSON, err := json.Marshal(legacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	importRecorder := httptest.NewRecorder()
	a.importData(importRecorder, httptest.NewRequest(http.MethodPost, "/api/data/import", bytes.NewReader(legacyJSON)))
	if importRecorder.Code != http.StatusOK {
		t.Fatalf("legacy timezone backup import status=%d body=%s", importRecorder.Code, importRecorder.Body.String())
	}
	if a.location.String() != "Asia/Seoul" {
		t.Fatalf("backup timezone changed runtime location to %q", a.location)
	}
}

func TestJSONExportImportRoundTrip(t *testing.T) {
	a := newTestApplication(t)
	if _, err := a.db.Exec(`INSERT INTO currencies(code,name,is_builtin) VALUES('GBP','GBP',0)`); err != nil {
		t.Fatal(err)
	}
	var paymentID int64
	if err := a.db.QueryRow(`SELECT id FROM payment_methods WHERE is_builtin=1 LIMIT 1`).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	result, err := a.db.Exec(`INSERT INTO subscriptions(service_name,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,started_at) VALUES('백업 구독',1599,'GBP','monthly',10,'2026-08-10',?,'2026-08-01')`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if _, err = a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,1599,'GBP','2026-08-01')`, id); err != nil {
		t.Fatal(err)
	}
	exportRecorder := httptest.NewRecorder()
	a.exportData(exportRecorder, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if exportRecorder.Code != http.StatusOK {
		t.Fatalf("export=%d %s", exportRecorder.Code, exportRecorder.Body.String())
	}
	if bytes.Contains(exportRecorder.Body.Bytes(), []byte("password_hash")) {
		t.Fatal("backup exposed password hash")
	}
	var backup dataBackup
	if err := json.Unmarshal(exportRecorder.Body.Bytes(), &backup); err != nil {
		t.Fatal(err)
	}
	if backup.Version != 5 || len(backup.Subscriptions) != 1 || backup.Subscriptions[0].Amount != 1599 {
		t.Fatalf("unexpected backup amount encoding: version=%d subscriptions=%+v", backup.Version, backup.Subscriptions)
	}
	importRecorder := httptest.NewRecorder()
	a.importData(importRecorder, httptest.NewRequest(http.MethodPost, "/api/data/import", bytes.NewReader(exportRecorder.Body.Bytes())))
	if importRecorder.Code != http.StatusOK {
		t.Fatalf("import=%d %s", importRecorder.Code, importRecorder.Body.String())
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE service_name='백업 구독' AND currency='GBP' AND amount=1599`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("subscription was not restored")
	}
}

func TestBackupNotificationCredentialsAreOptional(t *testing.T) {
	a := newTestApplication(t)
	const (
		discordWebhook = "https://discord.com/api/webhooks/123456789012345678/secret_webhook_token"
		telegramToken  = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
		telegramChatID = "-1001234567890"
	)
	if _, err := a.db.Exec(
		`UPDATE notification_channels SET discord_webhook=?,telegram_bot_token=?,telegram_chat_id=? WHERE id=1`,
		discordWebhook,
		telegramToken,
		telegramChatID,
	); err != nil {
		t.Fatal(err)
	}

	excludedRecorder := httptest.NewRecorder()
	a.exportData(excludedRecorder, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if excludedRecorder.Code != http.StatusOK {
		t.Fatalf("excluded export=%d %s", excludedRecorder.Code, excludedRecorder.Body.String())
	}
	for _, secret := range []string{discordWebhook, telegramToken, telegramChatID} {
		if bytes.Contains(excludedRecorder.Body.Bytes(), []byte(secret)) {
			t.Fatalf("default backup exposed notification credential %q", secret)
		}
	}
	var excludedBackup dataBackup
	if err := json.Unmarshal(excludedRecorder.Body.Bytes(), &excludedBackup); err != nil {
		t.Fatal(err)
	}
	if excludedBackup.Version != 5 || excludedBackup.NotificationCredentialsIncluded {
		t.Fatalf("unexpected excluded backup metadata: %+v", excludedBackup)
	}

	const (
		preservedWebhook = "https://discord.com/api/webhooks/987654321098765432/preserved_webhook_token"
		preservedToken   = "987654321:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
		preservedChatID  = "123456789"
	)
	if _, err := a.db.Exec(
		`UPDATE notification_channels SET discord_webhook=?,telegram_bot_token=?,telegram_chat_id=? WHERE id=1`,
		preservedWebhook,
		preservedToken,
		preservedChatID,
	); err != nil {
		t.Fatal(err)
	}
	excludedImport := httptest.NewRecorder()
	a.importData(excludedImport, httptest.NewRequest(http.MethodPost, "/api/data/import", bytes.NewReader(excludedRecorder.Body.Bytes())))
	if excludedImport.Code != http.StatusOK {
		t.Fatalf("excluded import=%d %s", excludedImport.Code, excludedImport.Body.String())
	}
	var currentWebhook, currentToken, currentChatID string
	if err := a.db.QueryRow(`SELECT discord_webhook,telegram_bot_token,telegram_chat_id FROM notification_channels WHERE id=1`).Scan(
		&currentWebhook,
		&currentToken,
		&currentChatID,
	); err != nil {
		t.Fatal(err)
	}
	if currentWebhook != preservedWebhook || currentToken != preservedToken || currentChatID != preservedChatID {
		t.Fatalf("excluded import replaced notification credentials: %q %q %q", currentWebhook, currentToken, currentChatID)
	}

	includedRecorder := httptest.NewRecorder()
	a.exportData(includedRecorder, httptest.NewRequest(http.MethodGet, "/api/data/export?includeNotificationCredentials=true", nil))
	if includedRecorder.Code != http.StatusOK {
		t.Fatalf("included export=%d %s", includedRecorder.Code, includedRecorder.Body.String())
	}
	var includedBackup dataBackup
	if err := json.Unmarshal(includedRecorder.Body.Bytes(), &includedBackup); err != nil {
		t.Fatal(err)
	}
	if !includedBackup.NotificationCredentialsIncluded ||
		includedBackup.Settings.DiscordWebhook != preservedWebhook ||
		includedBackup.Settings.TelegramBotToken != preservedToken ||
		includedBackup.Settings.TelegramChatID != preservedChatID {
		t.Fatalf("notification credentials were not included: %+v", includedBackup.Settings)
	}
	if _, err := a.db.Exec(`UPDATE notification_channels SET discord_webhook='',telegram_bot_token='',telegram_chat_id='' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	includedImport := httptest.NewRecorder()
	a.importData(includedImport, httptest.NewRequest(http.MethodPost, "/api/data/import", bytes.NewReader(includedRecorder.Body.Bytes())))
	if includedImport.Code != http.StatusOK {
		t.Fatalf("included import=%d %s", includedImport.Code, includedImport.Body.String())
	}
	var restoredWebhook, restoredToken, restoredChatID string
	if err := a.db.QueryRow(`SELECT discord_webhook,telegram_bot_token,telegram_chat_id FROM notification_channels WHERE id=1`).Scan(
		&restoredWebhook,
		&restoredToken,
		&restoredChatID,
	); err != nil {
		t.Fatal(err)
	}
	if restoredWebhook != preservedWebhook || restoredToken != preservedToken || restoredChatID != preservedChatID {
		t.Fatalf("included import did not restore notification credentials: %q %q %q", restoredWebhook, restoredToken, restoredChatID)
	}
}

func TestLegacyBackupAmountsAreUpgraded(t *testing.T) {
	oldAmount, newAmount := int64(10), int64(20)
	oldCurrency, newCurrency := "USD", "TRY"
	var backup dataBackup
	backup.Version = 1
	backup.Subscriptions = append(backup.Subscriptions, struct {
		ID                                                                                                                           int64
		ServiceID                                                                                                                    *int64
		ServiceName, Icon, Color, Currency, BillingCycle, BillingAnchor, Category, Memo, Status, StartedAt, CancelledAt, TrialEndsAt string
		Amount                                                                                                                       int64
		BillingDay                                                                                                                   int
		PaymentMethodID                                                                                                              int64
	}{Amount: 25, Currency: "TRY"})
	backup.Activities = append(backup.Activities, struct {
		ID                       int64
		SubscriptionID           *int64
		EventType, ServiceName   string
		OldAmount, NewAmount     *int64
		OldCurrency, NewCurrency *string
		OccurredAt               string
	}{OldAmount: &oldAmount, NewAmount: &newAmount, OldCurrency: &oldCurrency, NewCurrency: &newCurrency})
	upgradeLegacyBackupAmounts(&backup)
	if backup.Subscriptions[0].Amount != 2500 || oldAmount != 1000 || newAmount != 2000 {
		t.Fatalf("legacy backup amounts were not upgraded: subscription=%d old=%d new=%d", backup.Subscriptions[0].Amount, oldAmount, newAmount)
	}
}
