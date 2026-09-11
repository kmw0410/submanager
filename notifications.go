package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SherClockHolmes/webpush-go"
)

var (
	discordWebhookPathPattern = regexp.MustCompile(`^/api/webhooks/[0-9]+/[A-Za-z0-9._-]+$`)
	telegramBotTokenPattern   = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,200}$`)
)

type pwaPushSubscription struct {
	Endpoint string
	P256DH   string
	Auth     string
}

func validVAPIDKeys(privateKey, publicKey string) bool {
	private, privateErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(privateKey))
	public, publicErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(publicKey))
	return privateErr == nil && publicErr == nil && len(private) == 32 && len(public) == 65
}

func validPWASubscription(subscription pwaPushSubscription) bool {
	endpoint, err := url.Parse(strings.TrimSpace(subscription.Endpoint))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || len(subscription.Endpoint) > 2048 {
		return false
	}
	p256dh, p256dhErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(subscription.P256DH))
	auth, authErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(subscription.Auth))
	return p256dhErr == nil && authErr == nil && len(p256dh) == 65 && len(auth) == 16
}

func validPWASubscriptions(subscriptions []pwaPushSubscription) bool {
	for _, subscription := range subscriptions {
		if !validPWASubscription(subscription) {
			return false
		}
	}
	return true
}

func (a *application) vapidKeys() (privateKey, publicKey string, err error) {
	load := func() error {
		return a.db.QueryRow(`SELECT value FROM app_metadata WHERE key='pwa_vapid_private'`).Scan(&privateKey)
	}
	if err = load(); err == nil {
		err = a.db.QueryRow(`SELECT value FROM app_metadata WHERE key='pwa_vapid_public'`).Scan(&publicKey)
		if err == nil && validVAPIDKeys(privateKey, publicKey) {
			return privateKey, publicKey, nil
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	privateKey, publicKey, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO app_metadata(key,value) VALUES('pwa_vapid_private',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, privateKey); err == nil {
		_, err = tx.Exec(`INSERT INTO app_metadata(key,value) VALUES('pwa_vapid_public',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, publicKey)
	}
	if err != nil {
		return "", "", err
	}
	if err = tx.Commit(); err != nil {
		return "", "", err
	}
	return privateKey, publicKey, nil
}

func validateDiscordWebhook(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid Discord webhook URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "discord.com" && host != "discordapp.com" {
		return "", errors.New("invalid Discord webhook host")
	}
	if !discordWebhookPathPattern.MatchString(parsed.EscapedPath()) {
		return "", errors.New("invalid Discord webhook path")
	}
	return parsed.String(), nil
}

func validateTelegramCredentials(token, chat string) error {
	token = strings.TrimSpace(token)
	chat = strings.TrimSpace(chat)
	if token != "" && !telegramBotTokenPattern.MatchString(token) {
		return errors.New("invalid Telegram bot token")
	}
	if len(chat) > 128 {
		return errors.New("invalid Telegram chat ID")
	}
	return nil
}

func (a *application) notificationClient() *http.Client {
	if a.notificationHTTPClient != nil {
		return a.notificationHTTPClient
	}
	return &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type upcomingNotification struct {
	Days  int
	Items []string
}

func (n upcomingNotification) plainText() string {
	var b strings.Builder
	b.WriteString("🔔 결제 예정\n\n")
	fmt.Fprintf(&b, "%d일 뒤에 %d개의 항목이 결제 예정이에요:\n", n.Days, len(n.Items))
	for _, item := range n.Items {
		b.WriteString("- ")
		b.WriteString(item)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func (n upcomingNotification) telegramMarkdown() string {
	var b strings.Builder
	b.WriteString("*🔔 결제 예정*\n\n")
	fmt.Fprintf(&b, "%d일 뒤에 %d개의 항목이 결제 예정이에요:\n", n.Days, len(n.Items))
	for _, item := range n.Items {
		b.WriteString("\\- ")
		b.WriteString(escapeTelegramMarkdownV2(item))
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func escapeTelegramMarkdownV2(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(value)
}

func telegramPayload(chat string, notification upcomingNotification) map[string]string {
	return map[string]string{
		"chat_id":    chat,
		"text":       notification.telegramMarkdown(),
		"parse_mode": "MarkdownV2",
	}
}

func (a *application) testNotification(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Channel          string `json:"channel"`
		DiscordWebhook   string `json:"discordWebhook"`
		TelegramBotToken string `json:"telegramBotToken"`
		TelegramChatID   string `json:"telegramChatId"`
	}
	if !decode(w, r, &v) {
		return
	}
	var discord, token, chat string
	if err := a.db.QueryRow(`SELECT discord_webhook,telegram_bot_token,telegram_chat_id FROM notification_channels WHERE id=1`).Scan(&discord, &token, &chat); err != nil {
		a.fail(w, err)
		return
	}
	if strings.TrimSpace(v.DiscordWebhook) != "" {
		discord = strings.TrimSpace(v.DiscordWebhook)
	}
	if strings.TrimSpace(v.TelegramBotToken) != "" {
		token = strings.TrimSpace(v.TelegramBotToken)
	}
	if strings.TrimSpace(v.TelegramChatID) != "" {
		chat = strings.TrimSpace(v.TelegramChatID)
	}
	var days int
	if err := a.db.QueryRow(`SELECT days_before FROM notification_rules WHERE id=1`).Scan(&days); err != nil {
		a.fail(w, err)
		return
	}
	notification := upcomingNotification{
		Days: days,
		Items: []string{
			upcomingNotificationItem("테스트 결제항목 1", 1000, "KRW"),
			upcomingNotificationItem("테스트 결제항목 2", 990, "USD"),
		},
	}
	if v.Channel == "pwa" {
		var enabled bool
		if err := a.db.QueryRow(`SELECT pwa_enabled FROM notification_channels WHERE id=1`).Scan(&enabled); err != nil {
			a.fail(w, err)
			return
		}
		if !enabled {
			bad(w, "PWA 알림을 먼저 켜 주세요")
			return
		}
		if _, err := a.sendPWANotification(notification); err != nil {
			bad(w, "PWA 알림을 보내지 못했어요. 이 기기의 푸시 알림을 켜 주세요")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	client := a.notificationClient()
	var req *http.Request
	var err error
	if v.Channel == "discord" {
		if discord == "" {
			bad(w, "Discord Webhook 주소를 입력해 주세요")
			return
		}
		discord, err = validateDiscordWebhook(discord)
		if err != nil {
			bad(w, "Discord Webhook 주소를 확인해 주세요")
			return
		}
		body, _ := json.Marshal(discordWebhookPayload(notification.plainText()))
		req, err = http.NewRequest(http.MethodPost, discord, strings.NewReader(string(body)))
	} else if v.Channel == "telegram" {
		if token == "" || chat == "" {
			bad(w, "Telegram Bot Token과 Chat ID를 입력해 주세요")
			return
		}
		if err = validateTelegramCredentials(token, chat); err != nil {
			bad(w, "Telegram Bot Token과 Chat ID를 확인해 주세요")
			return
		}
		endpoint := "https://api.telegram.org/bot" + token + "/sendMessage"
		body, _ := json.Marshal(telegramPayload(chat, notification))
		req, err = http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	} else {
		bad(w, "알림 채널을 확인해 주세요")
		return
	}
	if err != nil {
		bad(w, "요청을 만들지 못했어요")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		bad(w, "알림을 보내지 못했어요")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 500))
		bad(w, "알림 서비스가 요청을 거절했어요")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *application) pwaPublicKey(w http.ResponseWriter, _ *http.Request) {
	_, publicKey, err := a.vapidKeys()
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": publicKey})
}

func (a *application) savePWASubscription(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Endpoint       string `json:"endpoint"`
		ExpirationTime any    `json:"expirationTime"`
		Keys           struct {
			P256DH string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if !decode(w, r, &v) {
		return
	}
	subscription := pwaPushSubscription{Endpoint: strings.TrimSpace(v.Endpoint), P256DH: strings.TrimSpace(v.Keys.P256DH), Auth: strings.TrimSpace(v.Keys.Auth)}
	if !validPWASubscription(subscription) {
		bad(w, "PWA 푸시 구독 정보를 확인해 주세요")
		return
	}
	if _, err := a.db.Exec(`INSERT INTO pwa_push_subscriptions(endpoint,p256dh,auth,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh,auth=excluded.auth,updated_at=CURRENT_TIMESTAMP`, subscription.Endpoint, subscription.P256DH, subscription.Auth); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (a *application) deletePWASubscription(w http.ResponseWriter, r *http.Request) {
	var v struct{ Endpoint string }
	if !decode(w, r, &v) {
		return
	}
	endpoint, err := url.Parse(strings.TrimSpace(v.Endpoint))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || len(v.Endpoint) > 2048 {
		bad(w, "PWA 푸시 구독 정보를 확인해 주세요")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM pwa_push_subscriptions WHERE endpoint=?`, strings.TrimSpace(v.Endpoint)); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *application) notificationLoop(ctx context.Context) {
	a.runScheduledNotifications()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runScheduledNotifications()
		}
	}
}

func (a *application) runScheduledNotifications() {
	var upcoming bool
	var days int
	if err := a.db.QueryRow(`SELECT notify_upcoming,days_before FROM notification_rules WHERE id=1`).Scan(&upcoming, &days); err != nil {
		log.Printf("notification rules: %v", err)
		return
	}
	if !upcoming {
		return
	}

	now := time.Now().In(a.location)
	subs, err := a.loadSubscriptions()
	if err != nil {
		log.Printf("upcoming notifications: %v", err)
		return
	}

	items := make([]string, 0)
	dueDate := ""
	for _, s := range subs {
		if s.Status != "active" || s.Skipped {
			continue
		}
		due := nextPayment(now, s.BillingDay, s.BillingCycle, s.BillingDate)
		d, err := time.ParseInLocation("2006-01-02", due, a.location)
		if err != nil {
			continue
		}
		remaining := int(d.Sub(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.location)).Hours() / 24)
		if remaining != days {
			continue
		}
		items = append(items, upcomingNotificationItem(s.ServiceName, s.Amount, s.Currency))
		dueDate = due
	}
	if len(items) == 0 {
		return
	}

	sort.Strings(items)
	notification := upcomingNotification{Days: days, Items: items}
	key := "upcoming:" + dueDate + ":" + strconv.Itoa(days)
	a.deliverOnce(key, notification)
}

func upcomingNotificationItem(serviceName string, amount int64, currency string) string {
	return serviceName + " (" + money(amount, currency) + ")"
}

// Change and monthly-summary notifications are intentionally disabled.
// Discord, Telegram, and PWA push only receive grouped upcoming-payment notifications.
func (a *application) notifyChange(_ string) {}

func (a *application) deliverOnce(key string, notification upcomingNotification) {
	res, err := a.db.Exec(`INSERT OR IGNORE INTO notification_deliveries(delivery_key) VALUES(?)`, key)
	if err != nil {
		log.Printf("notification delivery: %v", err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return
	}
	if err := a.sendConfigured(notification); err != nil {
		_, _ = a.db.Exec(`DELETE FROM notification_deliveries WHERE delivery_key=?`, key)
		if !errors.Is(err, errNoChannels) {
			log.Printf("notification delivery: %v", err)
		}
	}
}

var errNoChannels = errors.New("no notification channels configured")

func discordWebhookPayload(message string) map[string]any {
	parts := strings.SplitN(strings.TrimSpace(message), "\n", 2)
	title := strings.TrimSpace(parts[0])
	description := ""
	if len(parts) == 2 {
		description = strings.TrimSpace(parts[1])
	}
	return map[string]any{
		"embeds": []map[string]any{{
			"title":       title,
			"description": description,
			"color":       10139816,
		}},
	}
}

func pwaPushPayload(notification upcomingNotification) map[string]string {
	return map[string]string{
		"title": "🔔 결제 예정",
		// The notification title already states that this is an upcoming payment.
		// Keep the body to the actionable details so mobile notifications do not
		// repeat the same heading twice.
		"body": notificationBody(notification),
		"tag":  "submanager-upcoming",
		"url":  "/",
	}
}

func notificationBody(notification upcomingNotification) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d일 뒤 결제할 %d개 항목이에요:\n", notification.Days, len(notification.Items))
	for _, item := range notification.Items {
		b.WriteString("- ")
		b.WriteString(item)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func (a *application) sendPWANotification(notification upcomingNotification) (int, error) {
	privateKey, publicKey, err := a.vapidKeys()
	if err != nil {
		return 0, err
	}
	var email string
	if err = a.db.QueryRow(`SELECT email FROM users WHERE id=1`).Scan(&email); err != nil {
		return 0, err
	}
	rows, err := a.db.Query(`SELECT endpoint,p256dh,auth FROM pwa_push_subscriptions`)
	if err != nil {
		return 0, err
	}
	var subscriptions []pwaPushSubscription
	for rows.Next() {
		var subscription pwaPushSubscription
		if err = rows.Scan(&subscription.Endpoint, &subscription.P256DH, &subscription.Auth); err != nil {
			rows.Close()
			return 0, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(pwaPushPayload(notification))
	sent := 0
	for _, subscription := range subscriptions {
		if !validPWASubscription(subscription) {
			_, _ = a.db.Exec(`DELETE FROM pwa_push_subscriptions WHERE endpoint=?`, subscription.Endpoint)
			continue
		}
		response, sendErr := webpush.SendNotification(payload, &webpush.Subscription{
			Endpoint: subscription.Endpoint,
			Keys:     webpush.Keys{P256dh: subscription.P256DH, Auth: subscription.Auth},
		}, &webpush.Options{
			HTTPClient:      a.notificationClient(),
			Subscriber:      "mailto:" + email,
			TTL:             3600,
			Urgency:         webpush.UrgencyHigh,
			VAPIDPublicKey:  publicKey,
			VAPIDPrivateKey: privateKey,
		})
		if response != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
			response.Body.Close()
			if response.StatusCode == http.StatusGone || response.StatusCode == http.StatusNotFound {
				_, _ = a.db.Exec(`DELETE FROM pwa_push_subscriptions WHERE endpoint=?`, subscription.Endpoint)
			}
		}
		if sendErr == nil && response != nil && response.StatusCode < 300 {
			sent++
		}
	}
	if sent == 0 {
		return 0, errNoChannels
	}
	return sent, nil
}

func (a *application) sendConfigured(notification upcomingNotification) error {
	var discord, token, chat string
	var discordEnabled, telegramEnabled, pwaEnabled bool
	if err := a.db.QueryRow(`SELECT discord_webhook,discord_enabled,telegram_bot_token,telegram_chat_id,telegram_enabled,pwa_enabled FROM notification_channels WHERE id=1`).Scan(&discord, &discordEnabled, &token, &chat, &telegramEnabled, &pwaEnabled); err != nil {
		return err
	}
	client := a.notificationClient()
	sent := 0
	var lastErr error
	if !discordEnabled {
		discord = ""
	}
	if discord != "" {
		validatedDiscord, err := validateDiscordWebhook(discord)
		if err != nil {
			lastErr = errors.New("invalid Discord webhook configuration")
		} else {
			discord = validatedDiscord
		}
	}
	if discord != "" && lastErr == nil {
		body, _ := json.Marshal(discordWebhookPayload(notification.plainText()))
		req, err := http.NewRequest(http.MethodPost, discord, strings.NewReader(string(body)))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if resp, e := client.Do(req); e == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
				resp.Body.Close()
				if resp.StatusCode < 300 {
					sent++
				} else {
					lastErr = errors.New("discord returned " + resp.Status)
				}
			} else {
				lastErr = errors.New("Discord notification request failed")
			}
		} else {
			lastErr = errors.New("invalid Discord notification request")
		}
	}
	if !telegramEnabled {
		token, chat = "", ""
	}
	if token != "" && chat != "" {
		if err := validateTelegramCredentials(token, chat); err != nil {
			lastErr = errors.New("invalid Telegram notification configuration")
			token = ""
		}
	}
	if token != "" && chat != "" {
		body, _ := json.Marshal(telegramPayload(chat, notification))
		req, err := http.NewRequest(http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", strings.NewReader(string(body)))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if resp, e := client.Do(req); e == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
				resp.Body.Close()
				if resp.StatusCode < 300 {
					sent++
				} else {
					lastErr = errors.New("telegram returned " + resp.Status)
				}
			} else {
				lastErr = errors.New("Telegram notification request failed")
			}
		} else {
			lastErr = errors.New("invalid Telegram notification request")
		}
	}
	if pwaEnabled {
		if pwaSent, err := a.sendPWANotification(notification); err == nil {
			sent += pwaSent
		} else if !errors.Is(err, errNoChannels) {
			lastErr = errors.New("PWA notification request failed")
		}
	}
	if sent > 0 {
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return errNoChannels
}
