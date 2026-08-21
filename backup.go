package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type dataBackup struct {
	Version                         int    `json:"version"`
	ExportedAt                      string `json:"exportedAt"`
	NotificationCredentialsIncluded bool   `json:"notificationCredentialsIncluded"`
	Settings                        struct {
		Name             string
		Currency         string
		Timezone         string `json:"timezone,omitempty"`
		DiscordWebhook   string
		TelegramBotToken string
		TelegramChatID   string
		NotifyDays       int
		NotifyUpcoming   bool
		NotifyChanges    bool
		NotifyMonthly    bool
	} `json:"settings"`
	PaymentMethods []struct {
		ID       int64
		Name     string
		Archived bool
	} `json:"paymentMethods"`
	Currencies []struct {
		ID         int64
		Code, Name string
		Archived   bool
	} `json:"currencies"`
	Subscriptions []struct {
		ID                                                                                                                           int64
		ServiceID                                                                                                                    *int64
		ServiceName, Icon, Color, Currency, BillingCycle, BillingAnchor, Category, Memo, Status, StartedAt, CancelledAt, TrialEndsAt string
		Amount                                                                                                                       int64
		BillingDay                                                                                                                   int
		PaymentMethodID                                                                                                              int64
	} `json:"subscriptions"`
	Occurrences []struct {
		ID, SubscriptionID              int64
		Period, ScheduledDate, Currency string
		Amount                          int64
		Skipped, Paid                   bool
	} `json:"occurrences"`
	PriceHistory []struct {
		ID, SubscriptionID      int64
		Amount                  int64
		Currency, EffectiveFrom string
	} `json:"priceHistory"`
	Activities []struct {
		ID                       int64
		SubscriptionID           *int64
		EventType, ServiceName   string
		OldAmount, NewAmount     *int64
		OldCurrency, NewCurrency *string
		OccurredAt               string
	} `json:"activities"`
}

func (a *application) exportData(w http.ResponseWriter, r *http.Request) {
	var b dataBackup
	b.Version = 3
	b.ExportedAt = time.Now().In(a.location).Format(time.RFC3339)
	b.NotificationCredentialsIncluded = r.URL.Query().Get("includeNotificationCredentials") == "true"
	err := a.db.QueryRow(`SELECT u.name,u.currency,n.days_before,n.notify_upcoming,n.notify_changes,n.notify_monthly FROM users u,notification_rules n WHERE u.id=1 AND n.id=1`).Scan(
		&b.Settings.Name,
		&b.Settings.Currency,
		&b.Settings.NotifyDays,
		&b.Settings.NotifyUpcoming,
		&b.Settings.NotifyChanges,
		&b.Settings.NotifyMonthly,
	)
	if err != nil {
		a.fail(w, err)
		return
	}
	if b.NotificationCredentialsIncluded {
		err = a.db.QueryRow(`SELECT discord_webhook,telegram_bot_token,telegram_chat_id FROM notification_channels WHERE id=1`).Scan(
			&b.Settings.DiscordWebhook,
			&b.Settings.TelegramBotToken,
			&b.Settings.TelegramChatID,
		)
		if err != nil {
			a.fail(w, err)
			return
		}
	}
	pm, err := a.db.Query(`SELECT id,name,archived FROM payment_methods WHERE is_builtin=0 ORDER BY id`)
	if err != nil {
		a.fail(w, err)
		return
	}
	for pm.Next() {
		var v struct {
			ID       int64
			Name     string
			Archived bool
		}
		if err = pm.Scan(&v.ID, &v.Name, &v.Archived); err != nil {
			pm.Close()
			a.fail(w, err)
			return
		}
		b.PaymentMethods = append(b.PaymentMethods, v)
	}
	pm.Close()
	currencyRows, err := a.db.Query(`SELECT id,code,name,archived FROM currencies WHERE is_builtin=0 ORDER BY id`)
	if err != nil {
		a.fail(w, err)
		return
	}
	for currencyRows.Next() {
		var v struct {
			ID         int64
			Code, Name string
			Archived   bool
		}
		if err = currencyRows.Scan(&v.ID, &v.Code, &v.Name, &v.Archived); err != nil {
			currencyRows.Close()
			a.fail(w, err)
			return
		}
		b.Currencies = append(b.Currencies, v)
	}
	currencyRows.Close()
	subs, err := a.db.Query(`SELECT id,service_id,service_name,icon,color,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,category,memo,status,started_at,COALESCE(cancelled_at,''),trial_ends_at FROM subscriptions ORDER BY id`)
	if err != nil {
		a.fail(w, err)
		return
	}
	for subs.Next() {
		var v struct {
			ID                                                                                                                           int64
			ServiceID                                                                                                                    *int64
			ServiceName, Icon, Color, Currency, BillingCycle, BillingAnchor, Category, Memo, Status, StartedAt, CancelledAt, TrialEndsAt string
			Amount                                                                                                                       int64
			BillingDay                                                                                                                   int
			PaymentMethodID                                                                                                              int64
		}
		if err = subs.Scan(&v.ID, &v.ServiceID, &v.ServiceName, &v.Icon, &v.Color, &v.Amount, &v.Currency, &v.BillingCycle, &v.BillingDay, &v.BillingAnchor, &v.PaymentMethodID, &v.Category, &v.Memo, &v.Status, &v.StartedAt, &v.CancelledAt, &v.TrialEndsAt); err != nil {
			subs.Close()
			a.fail(w, err)
			return
		}
		b.Subscriptions = append(b.Subscriptions, v)
	}
	subs.Close()
	occ, err := a.db.Query(`SELECT id,subscription_id,period,scheduled_date,amount,currency,skipped,paid FROM subscription_occurrences ORDER BY id`)
	if err != nil {
		a.fail(w, err)
		return
	}
	for occ.Next() {
		var v struct {
			ID, SubscriptionID              int64
			Period, ScheduledDate, Currency string
			Amount                          int64
			Skipped, Paid                   bool
		}
		if err = occ.Scan(&v.ID, &v.SubscriptionID, &v.Period, &v.ScheduledDate, &v.Amount, &v.Currency, &v.Skipped, &v.Paid); err != nil {
			occ.Close()
			a.fail(w, err)
			return
		}
		b.Occurrences = append(b.Occurrences, v)
	}
	occ.Close()
	ph, err := a.db.Query(`SELECT id,subscription_id,amount,currency,effective_from FROM subscription_price_history ORDER BY id`)
	if err != nil {
		a.fail(w, err)
		return
	}
	for ph.Next() {
		var v struct {
			ID, SubscriptionID      int64
			Amount                  int64
			Currency, EffectiveFrom string
		}
		if err = ph.Scan(&v.ID, &v.SubscriptionID, &v.Amount, &v.Currency, &v.EffectiveFrom); err != nil {
			ph.Close()
			a.fail(w, err)
			return
		}
		b.PriceHistory = append(b.PriceHistory, v)
	}
	ph.Close()
	activities, err := a.db.Query(`SELECT id,subscription_id,event_type,service_name,old_amount,old_currency,new_amount,new_currency,occurred_at FROM activity_events ORDER BY id`)
	if err != nil {
		a.fail(w, err)
		return
	}
	for activities.Next() {
		var v struct {
			ID                       int64
			SubscriptionID           *int64
			EventType, ServiceName   string
			OldAmount, NewAmount     *int64
			OldCurrency, NewCurrency *string
			OccurredAt               string
		}
		if err = activities.Scan(&v.ID, &v.SubscriptionID, &v.EventType, &v.ServiceName, &v.OldAmount, &v.OldCurrency, &v.NewAmount, &v.NewCurrency, &v.OccurredAt); err != nil {
			activities.Close()
			a.fail(w, err)
			return
		}
		b.Activities = append(b.Activities, v)
	}
	activities.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="submanager-backup.json"`)
	_ = json.NewEncoder(w).Encode(b)
}

func (a *application) importData(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	var b dataBackup
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		bad(w, "백업 JSON을 읽을 수 없어요")
		return
	}
	if b.Version != 1 && b.Version != 2 && b.Version != 3 {
		bad(w, "지원하지 않는 백업 버전이에요")
		return
	}
	if b.Version == 1 {
		upgradeLegacyBackupAmounts(&b)
	}
	backupIncludesNotificationCredentials := b.Version < 3 || b.NotificationCredentialsIncluded
	if backupIncludesNotificationCredentials {
		var err error
		b.Settings.DiscordWebhook, err = validateDiscordWebhook(b.Settings.DiscordWebhook)
		if err != nil {
			bad(w, "백업의 Discord Webhook 주소가 올바르지 않아요")
			return
		}
		b.Settings.TelegramBotToken = strings.TrimSpace(b.Settings.TelegramBotToken)
		b.Settings.TelegramChatID = strings.TrimSpace(b.Settings.TelegramChatID)
		if err := validateTelegramCredentials(b.Settings.TelegramBotToken, b.Settings.TelegramChatID); err != nil {
			bad(w, "백업의 Telegram 연동 정보가 올바르지 않아요")
			return
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	deleteQueries := []string{
		`DELETE FROM activity_events`,
		`DELETE FROM subscription_occurrences`,
		`DELETE FROM subscription_price_history`,
		`DELETE FROM subscriptions`,
		`DELETE FROM payment_methods WHERE is_builtin=0`,
		`DELETE FROM currencies WHERE is_builtin=0`,
	}
	for _, query := range deleteQueries {
		if _, err = tx.Exec(query); err != nil {
			a.fail(w, err)
			return
		}
	}
	for _, v := range b.PaymentMethods {
		if strings.TrimSpace(v.Name) == "" {
			bad(w, "결제수단 데이터가 올바르지 않아요")
			return
		}
		if _, err = tx.Exec(`INSERT INTO payment_methods(id,name,type,is_builtin,archived) VALUES(?,?,'custom',0,?)`, v.ID, v.Name, v.Archived); err != nil {
			a.fail(w, err)
			return
		}
	}
	for _, v := range b.Currencies {
		v.Code = strings.ToUpper(strings.TrimSpace(v.Code))
		if !currencyCodePattern.MatchString(v.Code) {
			bad(w, "백업의 통화 데이터가 올바르지 않아요")
			return
		}
		if _, err = tx.Exec(`INSERT INTO currencies(id,code,name,is_builtin,archived) VALUES(?,?,?,0,?)`, v.ID, v.Code, v.Name, v.Archived); err != nil {
			a.fail(w, err)
			return
		}
	}
	for _, v := range b.Subscriptions {
		if _, err = tx.Exec(`INSERT INTO subscriptions(id,service_id,service_name,icon,color,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,category,memo,status,started_at,cancelled_at,trial_ends_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.ServiceID, v.ServiceName, v.Icon, v.Color, v.Amount, v.Currency, v.BillingCycle, v.BillingDay, v.BillingAnchor, v.PaymentMethodID, v.Category, v.Memo, v.Status, v.StartedAt, nullIfEmpty(v.CancelledAt), v.TrialEndsAt); err != nil {
			a.fail(w, err)
			return
		}
	}
	for _, v := range b.Occurrences {
		if _, err = tx.Exec(`INSERT INTO subscription_occurrences(id,subscription_id,period,scheduled_date,amount,currency,skipped,paid) VALUES(?,?,?,?,?,?,?,?)`, v.ID, v.SubscriptionID, v.Period, v.ScheduledDate, v.Amount, v.Currency, v.Skipped, v.Paid); err != nil {
			a.fail(w, err)
			return
		}
	}
	for _, v := range b.PriceHistory {
		if _, err = tx.Exec(`INSERT INTO subscription_price_history(id,subscription_id,amount,currency,effective_from) VALUES(?,?,?,?,?)`, v.ID, v.SubscriptionID, v.Amount, v.Currency, v.EffectiveFrom); err != nil {
			a.fail(w, err)
			return
		}
	}
	for _, v := range b.Activities {
		if _, err = tx.Exec(`INSERT INTO activity_events(id,subscription_id,event_type,service_name,old_amount,old_currency,new_amount,new_currency,occurred_at) VALUES(?,?,?,?,?,?,?,?,?)`, v.ID, v.SubscriptionID, v.EventType, v.ServiceName, v.OldAmount, v.OldCurrency, v.NewAmount, v.NewCurrency, v.OccurredAt); err != nil {
			a.fail(w, err)
			return
		}
	}
	_, err = tx.Exec(
		`UPDATE users SET name=?,currency=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`,
		b.Settings.Name,
		b.Settings.Currency,
	)
	if err == nil && backupIncludesNotificationCredentials {
		_, err = tx.Exec(`UPDATE notification_channels SET discord_webhook=?,telegram_bot_token=?,telegram_chat_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, b.Settings.DiscordWebhook, b.Settings.TelegramBotToken, b.Settings.TelegramChatID)
	}
	if err == nil {
		_, err = tx.Exec(`UPDATE notification_rules SET days_before=?,notify_upcoming=?,notify_changes=?,notify_monthly=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, b.Settings.NotifyDays, b.Settings.NotifyUpcoming, b.Settings.NotifyChanges, b.Settings.NotifyMonthly)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func upgradeLegacyBackupAmounts(b *dataBackup) {
	for i := range b.Subscriptions {
		b.Subscriptions[i].Amount *= minorUnitFactor(b.Subscriptions[i].Currency)
	}
	for i := range b.Occurrences {
		b.Occurrences[i].Amount *= minorUnitFactor(b.Occurrences[i].Currency)
	}
	for i := range b.PriceHistory {
		b.PriceHistory[i].Amount *= minorUnitFactor(b.PriceHistory[i].Currency)
	}
	for i := range b.Activities {
		v := &b.Activities[i]
		if v.OldAmount != nil && v.OldCurrency != nil {
			*v.OldAmount *= minorUnitFactor(*v.OldCurrency)
		}
		if v.NewAmount != nil && v.NewCurrency != nil {
			*v.NewAmount *= minorUnitFactor(*v.NewCurrency)
		}
	}
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
