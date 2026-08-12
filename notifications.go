package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
	client := &http.Client{Timeout: 8 * time.Second}
	var req *http.Request
	var err error
	message := notificationTestMessage(time.Now().In(a.location))
	if v.Channel == "discord" {
		if discord == "" {
			bad(w, "Discord Webhook 주소를 입력해 주세요")
			return
		}
		body, _ := json.Marshal(discordWebhookPayload(message))
		req, err = http.NewRequest(http.MethodPost, discord, strings.NewReader(string(body)))
	} else if v.Channel == "telegram" {
		if token == "" || chat == "" {
			bad(w, "Telegram Bot Token과 Chat ID를 입력해 주세요")
			return
		}
		endpoint := "https://api.telegram.org/bot" + token + "/sendMessage"
		body, _ := json.Marshal(map[string]string{"chat_id": chat, "text": message})
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

func notificationTestMessage(now time.Time) string {
	return "SubManager 알림 테스트\n\n🔔 정기결제 알림 테스트\n1,000원\n" + now.Format("2006.01.02") + " 결제 예정이에요."
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
	var upcoming, monthly bool
	var days int
	if err := a.db.QueryRow(`SELECT notify_upcoming,notify_monthly,days_before FROM notification_rules WHERE id=1`).Scan(&upcoming, &monthly, &days); err != nil {
		log.Printf("notification rules: %v", err)
		return
	}
	now := time.Now().In(a.location)
	if upcoming {
		subs, err := a.loadSubscriptions()
		if err != nil {
			log.Printf("upcoming notifications: %v", err)
			return
		}
		for _, s := range subs {
			if s.Status != "active" || s.Skipped {
				continue
			}
			due := nextPayment(now, s.BillingDay, s.BillingCycle, s.BillingDate)
			d, _ := time.ParseInLocation("2006-01-02", due, a.location)
			remaining := int(time.Until(d).Hours() / 24)
			if d.Location() == a.location {
				remaining = int(d.Sub(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.location)).Hours() / 24)
			}
			if remaining == days {
				key := "upcoming:" + strconv.FormatInt(s.ID, 10) + ":" + due + ":" + strconv.Itoa(days)
				a.deliverOnce(key, "🔔 결제 예정\n\n"+s.ServiceName+"\n"+money(s.Amount, s.Currency)+"\n\n"+strconv.Itoa(days)+"일 뒤 결제 예정이에요.")
			}
		}
	}
	if monthly && now.Day() == 1 {
		totals, err := a.monthTotals(now.Format("2006-01"))
		if err != nil {
			log.Printf("monthly notification: %v", err)
			return
		}
		previous, err := a.monthTotals(now.AddDate(0, -1, 0).Format("2006-01"))
		if err != nil {
			return
		}
		currencies := make([]string, 0, len(totals))
		for currency := range totals {
			currencies = append(currencies, currency)
		}
		sort.Strings(currencies)
		lines := make([]string, 0, len(currencies))
		for _, currency := range currencies {
			line := money(totals[currency], currency)
			delta := totals[currency] - previous[currency]
			if delta > 0 {
				line += " · +" + money(delta, currency)
			} else if delta < 0 {
				line += " · -" + money(-delta, currency)
			}
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			lines = append(lines, money(0, "KRW"))
		}
		a.deliverOnce("monthly:"+now.Format("2006-01"), "📊 이번 달 구독비\n\n"+strings.Join(lines, "\n"))
	}
}

func (a *application) notifyChange(message string) {
	var enabled bool
	if err := a.db.QueryRow(`SELECT notify_changes FROM notification_rules WHERE id=1`).Scan(&enabled); err != nil || !enabled {
		return
	}
	if err := a.sendConfigured(message); err != nil {
		log.Printf("change notification: %v", err)
	}
}

func (a *application) deliverOnce(key, message string) {
	res, err := a.db.Exec(`INSERT OR IGNORE INTO notification_deliveries(delivery_key) VALUES(?)`, key)
	if err != nil {
		log.Printf("notification delivery: %v", err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return
	}
	if err := a.sendConfigured(message); err != nil {
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

func (a *application) sendConfigured(message string) error {
	var discord, token, chat string
	if err := a.db.QueryRow(`SELECT discord_webhook,telegram_bot_token,telegram_chat_id FROM notification_channels WHERE id=1`).Scan(&discord, &token, &chat); err != nil {
		return err
	}
	client := &http.Client{Timeout: 8 * time.Second}
	sent := 0
	var lastErr error
	if discord != "" {
		body, _ := json.Marshal(discordWebhookPayload(message))
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
		body, _ := json.Marshal(map[string]string{"chat_id": chat, "text": message})
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
