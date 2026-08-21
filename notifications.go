package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
		Channel, DiscordWebhook, TelegramBotToken, TelegramChatID string
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
	client := &http.Client{Timeout: 8 * time.Second}
	var req *http.Request
	var err error
	if v.Channel == "discord" {
		if discord == "" {
			bad(w, "Discord Webhook 주소를 입력해 주세요")
			return
		}
		body, _ := json.Marshal(discordWebhookPayload(notification.plainText()))
		req, err = http.NewRequest(http.MethodPost, discord, strings.NewReader(string(body)))
	} else if v.Channel == "telegram" {
		if token == "" || chat == "" {
			bad(w, "Telegram Bot Token과 Chat ID를 입력해 주세요")
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
		bad(w, "알림을 보내지 못했어요: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		bad(w, "알림 서비스가 요청을 거절했어요: "+string(b))
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
// Discord and Telegram only receive grouped upcoming-payment notifications.
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

func (a *application) sendConfigured(notification upcomingNotification) error {
	var discord, token, chat string
	if err := a.db.QueryRow(`SELECT discord_webhook,telegram_bot_token,telegram_chat_id FROM notification_channels WHERE id=1`).Scan(&discord, &token, &chat); err != nil {
		return err
	}
	client := &http.Client{Timeout: 8 * time.Second}
	sent := 0
	var lastErr error
	if discord != "" {
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
				lastErr = e
			}
		} else {
			lastErr = err
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
				lastErr = e
			}
		} else {
			lastErr = err
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
