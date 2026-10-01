package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPWAPushSubscriptionValidationAndKeys(t *testing.T) {
	a := newTestApplication(t)
	privateKey, publicKey, err := a.vapidKeys()
	if err != nil || !validVAPIDKeys(privateKey, publicKey) {
		t.Fatalf("invalid VAPID keys: %v", err)
	}
	publicRecorder := httptest.NewRecorder()
	a.pwaPublicKey(publicRecorder, httptest.NewRequest(http.MethodGet, "/api/pwa/vapid-public", nil))
	if publicRecorder.Code != http.StatusOK || strings.Contains(publicRecorder.Body.String(), privateKey) || !strings.Contains(publicRecorder.Body.String(), publicKey) {
		t.Fatalf("VAPID public-key response is invalid: status=%d body=%s", publicRecorder.Code, publicRecorder.Body.String())
	}
	validKey := base64.RawURLEncoding.EncodeToString(make([]byte, 65))
	validAuth := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	request, recorder := jsonRequest(t, http.MethodPost, "/api/pwa/subscriptions", map[string]any{
		"endpoint":       "https://push.example.test/subscription",
		"expirationTime": nil,
		"keys":           map[string]string{"p256dh": validKey, "auth": validAuth},
	})
	a.savePWASubscription(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("PWA subscription status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	invalidRequest, invalidRecorder := jsonRequest(t, http.MethodPost, "/api/pwa/subscriptions", map[string]any{"endpoint": "http://unsafe.example.test", "keys": map[string]string{}})
	a.savePWASubscription(invalidRecorder, invalidRequest)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unsafe PWA subscription status=%d body=%s", invalidRecorder.Code, invalidRecorder.Body.String())
	}
}

func TestPWATestNotificationIsDelivered(t *testing.T) {
	a := newTestApplication(t)
	if _, err := a.db.Exec(`UPDATE notification_channels SET pwa_enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE users SET email='admin@example.com' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	privateKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err = rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`INSERT INTO pwa_push_subscriptions(endpoint,p256dh,auth) VALUES(?,?,?)`, "https://push.example.test/subscription", base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(auth)); err != nil {
		t.Fatal(err)
	}
	a.notificationHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "push.example.test" {
			t.Fatalf("unexpected PWA push destination: %q", request.URL.Host)
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	request, recorder := jsonRequest(t, http.MethodPost, "/api/notifications/test", map[string]string{"channel": "pwa"})
	a.testNotification(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PWA test notification status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUpcomingNotificationItemsIncludePrices(t *testing.T) {
	items := []string{
		upcomingNotificationItem("Discord Nitro", 1000, "USD"),
		upcomingNotificationItem("네이버플러스 멤버십", 4900, "KRW"),
	}
	if items[0] != "Discord Nitro ($10.00)" {
		t.Fatalf("unexpected USD notification item: %q", items[0])
	}
	if items[1] != "네이버플러스 멤버십 (₩4,900)" {
		t.Fatalf("unexpected KRW notification item: %q", items[1])
	}

	notification := upcomingNotification{Days: 3, Items: items}
	for _, want := range items {
		if !strings.Contains(notification.plainText(), "- "+want) {
			t.Fatalf("plain notification is missing %q: %s", want, notification.plainText())
		}
	}
	if !strings.Contains(notification.telegramMarkdown(), `Discord Nitro \($10\.00\)`) {
		t.Fatalf("Telegram notification did not escape the priced item: %s", notification.telegramMarkdown())
	}

	payload := discordWebhookPayload(notification.plainText())
	embeds, ok := payload["embeds"].([]map[string]any)
	if !ok || len(embeds) != 1 || embeds[0]["title"] != "🔔 결제 예정" {
		t.Fatalf("Discord upcoming notification must be an embed: %#v", payload)
	}
	pwaPayload := pwaPushPayload(notification)
	if pwaPayload["title"] != "🔔 결제 예정" || strings.Contains(pwaPayload["body"], "결제 예정") {
		t.Fatalf("PWA notification must keep its heading only in the title: %#v", pwaPayload)
	}
	for _, want := range items {
		if !strings.Contains(pwaPayload["body"], "- "+want) {
			t.Fatalf("PWA notification body is missing %q: %s", want, pwaPayload["body"])
		}
	}
}

func TestNotificationDestinationsAreRestricted(t *testing.T) {
	valid := "https://discord.com/api/webhooks/123456789012345678/secret_webhook_token"
	if got, err := validateDiscordWebhook(valid); err != nil || got != valid {
		t.Fatalf("valid Discord webhook rejected: got=%q err=%v", got, err)
	}
	for _, value := range []string{
		"http://discord.com/api/webhooks/123/token",
		"https://127.0.0.1/api/webhooks/123/token",
		"https://discord.com.evil.example/api/webhooks/123/token",
		"https://discord.com/api/webhooks/123/token?redirect=http://127.0.0.1",
	} {
		if _, err := validateDiscordWebhook(value); err == nil {
			t.Fatalf("unsafe Discord webhook accepted: %q", value)
		}
	}
	if err := validateTelegramCredentials("not-a-token", "1234"); err == nil {
		t.Fatal("invalid Telegram token was accepted")
	}
}

func TestDisabledNotificationChannelsAreNotDelivered(t *testing.T) {
	a := newTestApplication(t)
	const webhook = "https://discord.com/api/webhooks/123456789012345678/secret_webhook_token"
	if _, err := a.db.Exec(`UPDATE notification_channels SET discord_webhook=?,discord_enabled=0,telegram_enabled=0 WHERE id=1`, webhook); err != nil {
		t.Fatal(err)
	}
	if err := a.sendConfigured(upcomingNotification{Days: 3, Items: []string{"테스트 (₩1,000)"}}); !errors.Is(err, errNoChannels) {
		t.Fatalf("disabled notification channels must not send, got %v", err)
	}
}

func TestNotificationErrorsDoNotExposeTelegramToken(t *testing.T) {
	a := newTestApplication(t)
	const token = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
	a.notificationHTTPClient = &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed for " + request.URL.String())
		}),
	}
	request, recorder := jsonRequest(t, http.MethodPost, "/api/notifications/test", map[string]string{
		"channel": "telegram", "telegramBotToken": token, "telegramChatID": "123456789",
	})
	a.testNotification(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("notification test status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), token) {
		t.Fatalf("Telegram token leaked in error response: %s", recorder.Body.String())
	}
}
