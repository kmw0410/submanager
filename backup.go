package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/mattn/go-sqlite3"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type backupPaymentMethod struct {
	ID       int64
	Name     string
	Archived bool
}

type backupCurrency struct {
	ID         int64
	Code, Name string
	Archived   bool
}

type backupSubscription struct {
	ID                                                                                                                           int64
	ServiceID                                                                                                                    *int64
	ServiceName, Icon, Color, Currency, BillingCycle, BillingAnchor, Category, Memo, Status, StartedAt, CancelledAt, TrialEndsAt string
	Amount                                                                                                                       int64
	BillingDay                                                                                                                   int
	PaymentMethodID                                                                                                              int64
}

type backupOccurrence struct {
	ID, SubscriptionID              int64
	Period, ScheduledDate, Currency string
	Amount                          int64
	Skipped, Paid                   bool
}

type backupPricePoint struct {
	ID, SubscriptionID      int64
	Amount                  int64
	Currency, EffectiveFrom string
}

type backupActivity struct {
	ID                       int64
	SubscriptionID           *int64
	EventType, ServiceName   string
	OldAmount, NewAmount     *int64
	OldCurrency, NewCurrency *string
	OccurredAt               string
}

type dataBackup struct {
	Version                         int    `json:"version"`
	ExportedAt                      string `json:"exportedAt"`
	NotificationCredentialsIncluded bool   `json:"notificationCredentialsIncluded"`
	Settings                        struct {
		Name             string
		Currency         string
		Timezone         string `json:"timezone,omitempty"`
		DiscordWebhook   string
		DiscordEnabled   bool
		TelegramBotToken string
		TelegramChatID   string
		TelegramEnabled  bool
		PWAEnabled       bool
		VAPIDPublicKey   string
		VAPIDPrivateKey  string
		NotifyDays       int
		NotifyUpcoming   bool
		NotifyChanges    bool
		NotifyMonthly    bool
	} `json:"settings"`
	PWASubscriptions []pwaPushSubscription `json:"pwaSubscriptions"`
	PaymentMethods   []backupPaymentMethod `json:"paymentMethods"`
	Currencies       []backupCurrency      `json:"currencies"`
	Subscriptions    []backupSubscription  `json:"subscriptions"`
	Occurrences      []backupOccurrence    `json:"occurrences"`
	PriceHistory     []backupPricePoint    `json:"priceHistory"`
	Activities       []backupActivity      `json:"activities"`
}

func (a *application) exportData(w http.ResponseWriter, r *http.Request) {
	var b dataBackup
	b.Version = 5
	b.ExportedAt = time.Now().In(a.location).Format(time.RFC3339)
	b.NotificationCredentialsIncluded = r.URL.Query().Get("includeNotificationCredentials") == "true"
	if b.NotificationCredentialsIncluded {
		// Ensure keys exist before taking the snapshot; read their actual values
		// inside it together with the push subscriptions they protect.
		_, _, err := a.vapidKeys()
		if err != nil {
			backupFailure(a, w, err)
			return
		}
	}
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	defer tx.Rollback()
	err = tx.QueryRow(`SELECT u.name,u.currency,n.days_before,n.notify_upcoming,n.notify_changes,n.notify_monthly FROM users u,notification_rules n WHERE u.id=1 AND n.id=1`).Scan(
		&b.Settings.Name,
		&b.Settings.Currency,
		&b.Settings.NotifyDays,
		&b.Settings.NotifyUpcoming,
		&b.Settings.NotifyChanges,
		&b.Settings.NotifyMonthly,
	)
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	if b.NotificationCredentialsIncluded {
		if err = tx.QueryRow(`SELECT value FROM app_metadata WHERE key='pwa_vapid_private'`).Scan(&b.Settings.VAPIDPrivateKey); err == nil {
			err = tx.QueryRow(`SELECT value FROM app_metadata WHERE key='pwa_vapid_public'`).Scan(&b.Settings.VAPIDPublicKey)
		}
		if err != nil {
			backupFailure(a, w, err)
			return
		}
		err = tx.QueryRow(`SELECT discord_webhook,discord_enabled,telegram_bot_token,telegram_chat_id,telegram_enabled,pwa_enabled FROM notification_channels WHERE id=1`).Scan(
			&b.Settings.DiscordWebhook,
			&b.Settings.DiscordEnabled,
			&b.Settings.TelegramBotToken,
			&b.Settings.TelegramChatID,
			&b.Settings.TelegramEnabled,
			&b.Settings.PWAEnabled,
		)
		if err != nil {
			backupFailure(a, w, err)
			return
		}
		rows, err := tx.Query(`SELECT endpoint,p256dh,auth FROM pwa_push_subscriptions ORDER BY id`)
		if err != nil {
			backupFailure(a, w, err)
			return
		}
		for rows.Next() {
			var subscription pwaPushSubscription
			if err = rows.Scan(&subscription.Endpoint, &subscription.P256DH, &subscription.Auth); err != nil {
				rows.Close()
				backupFailure(a, w, err)
				return
			}
			b.PWASubscriptions = append(b.PWASubscriptions, subscription)
		}
		if err = finishRows(rows); err != nil {
			backupFailure(a, w, err)
			return
		}
	}
	pm, err := tx.Query(`SELECT id,name,archived FROM payment_methods WHERE is_builtin=0 ORDER BY id`)
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	for pm.Next() {
		var v backupPaymentMethod
		if err = pm.Scan(&v.ID, &v.Name, &v.Archived); err != nil {
			pm.Close()
			backupFailure(a, w, err)
			return
		}
		b.PaymentMethods = append(b.PaymentMethods, v)
	}
	if err = finishRows(pm); err != nil {
		backupFailure(a, w, err)
		return
	}
	currencyRows, err := tx.Query(`SELECT id,code,name,archived FROM currencies WHERE is_builtin=0 ORDER BY id`)
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	for currencyRows.Next() {
		var v backupCurrency
		if err = currencyRows.Scan(&v.ID, &v.Code, &v.Name, &v.Archived); err != nil {
			currencyRows.Close()
			backupFailure(a, w, err)
			return
		}
		b.Currencies = append(b.Currencies, v)
	}
	if err = finishRows(currencyRows); err != nil {
		backupFailure(a, w, err)
		return
	}
	subs, err := tx.Query(`SELECT id,service_id,service_name,icon,color,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,category,memo,status,started_at,COALESCE(cancelled_at,''),trial_ends_at FROM subscriptions ORDER BY id`)
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	for subs.Next() {
		var v backupSubscription
		if err = subs.Scan(&v.ID, &v.ServiceID, &v.ServiceName, &v.Icon, &v.Color, &v.Amount, &v.Currency, &v.BillingCycle, &v.BillingDay, &v.BillingAnchor, &v.PaymentMethodID, &v.Category, &v.Memo, &v.Status, &v.StartedAt, &v.CancelledAt, &v.TrialEndsAt); err != nil {
			subs.Close()
			backupFailure(a, w, err)
			return
		}
		b.Subscriptions = append(b.Subscriptions, v)
	}
	if err = finishRows(subs); err != nil {
		backupFailure(a, w, err)
		return
	}
	occ, err := tx.Query(`SELECT id,subscription_id,period,scheduled_date,amount,currency,skipped,paid FROM subscription_occurrences ORDER BY id`)
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	for occ.Next() {
		var v backupOccurrence
		if err = occ.Scan(&v.ID, &v.SubscriptionID, &v.Period, &v.ScheduledDate, &v.Amount, &v.Currency, &v.Skipped, &v.Paid); err != nil {
			occ.Close()
			backupFailure(a, w, err)
			return
		}
		b.Occurrences = append(b.Occurrences, v)
	}
	if err = finishRows(occ); err != nil {
		backupFailure(a, w, err)
		return
	}
	ph, err := tx.Query(`SELECT id,subscription_id,amount,currency,effective_from FROM subscription_price_history ORDER BY id`)
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	for ph.Next() {
		var v backupPricePoint
		if err = ph.Scan(&v.ID, &v.SubscriptionID, &v.Amount, &v.Currency, &v.EffectiveFrom); err != nil {
			ph.Close()
			backupFailure(a, w, err)
			return
		}
		b.PriceHistory = append(b.PriceHistory, v)
	}
	if err = finishRows(ph); err != nil {
		backupFailure(a, w, err)
		return
	}
	activities, err := tx.Query(`SELECT id,subscription_id,event_type,service_name,old_amount,old_currency,new_amount,new_currency,occurred_at FROM activity_events ORDER BY id`)
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	for activities.Next() {
		var v backupActivity
		if err = activities.Scan(&v.ID, &v.SubscriptionID, &v.EventType, &v.ServiceName, &v.OldAmount, &v.OldCurrency, &v.NewAmount, &v.NewCurrency, &v.OccurredAt); err != nil {
			activities.Close()
			backupFailure(a, w, err)
			return
		}
		b.Activities = append(b.Activities, v)
	}
	if err = finishRows(activities); err != nil {
		backupFailure(a, w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		backupFailure(a, w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="submanager-backup.json"`)
	_ = json.NewEncoder(w).Encode(b)
}

func (a *application) importData(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	var b dataBackup
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := decodeJSONDocument(d, &b); err != nil {
		bad(w, "백업 JSON을 읽을 수 없어요")
		return
	}
	if b.Version != 1 && b.Version != 2 && b.Version != 3 && b.Version != 4 && b.Version != 5 {
		bad(w, "지원하지 않는 백업 버전이에요")
		return
	}
	if err := validateBackup(b); err != nil {
		bad(w, err.Error())
		return
	}
	if b.Version == 1 {
		if err := upgradeLegacyBackupAmounts(&b); err != nil {
			bad(w, "백업 금액이 지원 범위를 초과해요")
			return
		}
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
		if b.Version < 4 {
			b.Settings.DiscordEnabled = b.Settings.DiscordWebhook != ""
			b.Settings.TelegramEnabled = b.Settings.TelegramBotToken != "" && b.Settings.TelegramChatID != ""
		}
		if b.Version >= 5 && (!validVAPIDKeys(b.Settings.VAPIDPrivateKey, b.Settings.VAPIDPublicKey) || !validPWASubscriptions(b.PWASubscriptions)) {
			bad(w, "백업의 PWA 알림 정보가 올바르지 않아요")
			return
		}
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		backupFailure(a, w, err)
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
			backupFailure(a, w, err)
			return
		}
	}
	for _, v := range b.PaymentMethods {
		if strings.TrimSpace(v.Name) == "" {
			bad(w, "결제수단 데이터가 올바르지 않아요")
			return
		}
		if _, err = tx.Exec(`INSERT INTO payment_methods(id,name,type,is_builtin,archived) VALUES(?,?,'custom',0,?)`, v.ID, v.Name, v.Archived); err != nil {
			backupFailure(a, w, err)
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
			backupFailure(a, w, err)
			return
		}
	}
	// Currency codes are not foreign keys in legacy schemas, so validate them explicitly.
	currencies, err := tx.Query(`SELECT code,archived FROM currencies`)
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	available := map[string]bool{}
	active := map[string]bool{}
	for currencies.Next() {
		var code string
		var archived bool
		if err = currencies.Scan(&code, &archived); err != nil {
			currencies.Close()
			backupFailure(a, w, err)
			return
		}
		available[code] = true
		active[code] = !archived
	}
	if err = finishRows(currencies); err != nil {
		backupFailure(a, w, err)
		return
	}
	b.Settings.Currency = strings.ToUpper(b.Settings.Currency)
	if !active[b.Settings.Currency] {
		bad(w, "백업의 기본 통화를 사용할 수 없어요")
		return
	}
	validCurrencies := true
	for i := range b.Subscriptions {
		v := &b.Subscriptions[i]
		v.Currency = strings.ToUpper(v.Currency)
		validCurrencies = validCurrencies && available[v.Currency]
	}
	for i := range b.Occurrences {
		v := &b.Occurrences[i]
		v.Currency = strings.ToUpper(v.Currency)
		validCurrencies = validCurrencies && available[v.Currency]
	}
	for i := range b.PriceHistory {
		v := &b.PriceHistory[i]
		v.Currency = strings.ToUpper(v.Currency)
		validCurrencies = validCurrencies && available[v.Currency]
	}
	for i := range b.Activities {
		v := &b.Activities[i]
		for _, code := range []*string{v.OldCurrency, v.NewCurrency} {
			if code != nil {
				*code = strings.ToUpper(*code)
				validCurrencies = validCurrencies && available[*code]
			}
		}
	}
	if !validCurrencies {
		bad(w, "백업의 통화를 찾을 수 없어요")
		return
	}
	for _, v := range b.Subscriptions {
		if _, err = tx.Exec(`INSERT INTO subscriptions(id,service_id,service_name,icon,color,amount,currency,billing_cycle,billing_day,billing_anchor,payment_method_id,category,memo,status,started_at,cancelled_at,trial_ends_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.ServiceID, v.ServiceName, v.Icon, v.Color, v.Amount, v.Currency, v.BillingCycle, v.BillingDay, v.BillingAnchor, v.PaymentMethodID, v.Category, v.Memo, v.Status, v.StartedAt, nullIfEmpty(v.CancelledAt), v.TrialEndsAt); err != nil {
			backupFailure(a, w, err)
			return
		}
	}
	for _, v := range b.Occurrences {
		if _, err = tx.Exec(`INSERT INTO subscription_occurrences(id,subscription_id,period,scheduled_date,amount,currency,skipped,paid) VALUES(?,?,?,?,?,?,?,?)`, v.ID, v.SubscriptionID, v.Period, v.ScheduledDate, v.Amount, v.Currency, v.Skipped, v.Paid); err != nil {
			backupFailure(a, w, err)
			return
		}
	}
	for _, v := range b.PriceHistory {
		if _, err = tx.Exec(`INSERT INTO subscription_price_history(id,subscription_id,amount,currency,effective_from) VALUES(?,?,?,?,?)`, v.ID, v.SubscriptionID, v.Amount, v.Currency, v.EffectiveFrom); err != nil {
			backupFailure(a, w, err)
			return
		}
	}
	for _, v := range b.Activities {
		if _, err = tx.Exec(`INSERT INTO activity_events(id,subscription_id,event_type,service_name,old_amount,old_currency,new_amount,new_currency,occurred_at) VALUES(?,?,?,?,?,?,?,?,?)`, v.ID, v.SubscriptionID, v.EventType, v.ServiceName, v.OldAmount, v.OldCurrency, v.NewAmount, v.NewCurrency, v.OccurredAt); err != nil {
			backupFailure(a, w, err)
			return
		}
	}
	_, err = tx.Exec(
		`UPDATE users SET name=?,currency=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`,
		b.Settings.Name,
		b.Settings.Currency,
	)
	if err == nil && backupIncludesNotificationCredentials {
		_, err = tx.Exec(`UPDATE notification_channels SET discord_webhook=?,discord_enabled=?,telegram_bot_token=?,telegram_chat_id=?,telegram_enabled=?,pwa_enabled=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, b.Settings.DiscordWebhook, b.Settings.DiscordEnabled, b.Settings.TelegramBotToken, b.Settings.TelegramChatID, b.Settings.TelegramEnabled, b.Settings.PWAEnabled)
	}
	if err == nil && b.Version >= 5 && backupIncludesNotificationCredentials {
		if _, err = tx.Exec(`DELETE FROM pwa_push_subscriptions`); err == nil {
			for _, subscription := range b.PWASubscriptions {
				if _, err = tx.Exec(`INSERT INTO pwa_push_subscriptions(endpoint,p256dh,auth) VALUES(?,?,?)`, subscription.Endpoint, subscription.P256DH, subscription.Auth); err != nil {
					break
				}
			}
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO app_metadata(key,value) VALUES('pwa_vapid_private',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, b.Settings.VAPIDPrivateKey)
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO app_metadata(key,value) VALUES('pwa_vapid_public',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, b.Settings.VAPIDPublicKey)
		}
	}
	if err == nil {
		_, err = tx.Exec(`UPDATE notification_rules SET days_before=?,notify_upcoming=?,notify_changes=?,notify_monthly=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, b.Settings.NotifyDays, b.Settings.NotifyUpcoming, b.Settings.NotifyChanges, b.Settings.NotifyMonthly)
	}
	if err != nil {
		backupFailure(a, w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		backupFailure(a, w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func upgradeLegacyBackupAmounts(b *dataBackup) error {
	fits := func(value int64, currency string) bool {
		factor := minorUnitFactor(currency)
		return value >= 0 && value <= math.MaxInt64/factor
	}
	for _, v := range b.Subscriptions {
		if !fits(v.Amount, v.Currency) {
			return errors.New("legacy amount overflow")
		}
	}
	for _, v := range b.Occurrences {
		if !fits(v.Amount, v.Currency) {
			return errors.New("legacy amount overflow")
		}
	}
	for _, v := range b.PriceHistory {
		if !fits(v.Amount, v.Currency) {
			return errors.New("legacy amount overflow")
		}
	}
	for _, v := range b.Activities {
		if v.OldAmount != nil && v.OldCurrency != nil && !fits(*v.OldAmount, *v.OldCurrency) {
			return errors.New("legacy amount overflow")
		}
		if v.NewAmount != nil && v.NewCurrency != nil && !fits(*v.NewAmount, *v.NewCurrency) {
			return errors.New("legacy amount overflow")
		}
	}

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
	return nil
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func finishRows(rows *sql.Rows) error {
	err := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func backupFailure(a *application, w http.ResponseWriter, err error) {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
		bad(w, "백업의 항목 또는 연결 관계가 올바르지 않아요")
		return
	}
	// Scan errors can include stored values; backups contain private data and
	// credentials, so never send the underlying database error to the logger.
	a.fail(w, errors.New("backup database operation failed"))
}

func validateBackup(b dataBackup) error {
	invalid := errors.New("백업 데이터가 올바르지 않아요")
	validCode := func(code string) bool { return currencyCodePattern.MatchString(strings.ToUpper(code)) }
	date := func(value string, optional bool) bool {
		if optional && value == "" {
			return true
		}
		_, err := storedDate(value, time.UTC)
		return err == nil
	}
	if strings.TrimSpace(b.Settings.Name) == "" || !validCode(b.Settings.Currency) || b.Settings.NotifyDays < 0 || b.Settings.NotifyDays > 30 {
		return invalid
	}
	unique := func() func(int64) bool {
		seen := map[int64]bool{}
		return func(id int64) bool {
			if id < 1 || seen[id] {
				return false
			}
			seen[id] = true
			return true
		}
	}
	ids := unique()
	names := map[string]bool{}
	for _, v := range b.PaymentMethods {
		// Match SQLite NOCASE, which folds ASCII only. Unicode case variants
		// can legitimately coexist in existing exported payment methods.
		name := strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' {
				return r + ('a' - 'A')
			}
			return r
		}, v.Name)
		if !ids(v.ID) || strings.TrimSpace(name) == "" || names[name] {
			return invalid
		}
		names[name] = true
	}
	ids = unique()
	codes := map[string]bool{}
	for _, v := range b.Currencies {
		code := strings.ToUpper(strings.TrimSpace(v.Code))
		if !ids(v.ID) || !validCode(code) || codes[code] {
			return invalid
		}
		codes[code] = true
	}
	ids = unique()
	subs := map[int64]bool{}
	for _, v := range b.Subscriptions {
		if !ids(v.ID) || strings.TrimSpace(v.ServiceName) == "" || v.Amount < 0 || !validCode(v.Currency) || v.PaymentMethodID < 1 || v.BillingDay < 1 || v.BillingDay > 31 || (v.BillingCycle != "monthly" && v.BillingCycle != "yearly") || (v.Status != "active" && v.Status != "cancelled") || !date(v.StartedAt, false) || !date(v.BillingAnchor, true) || !date(v.CancelledAt, true) || !date(v.TrialEndsAt, true) {
			return invalid
		}
		if v.ServiceID != nil && *v.ServiceID < 1 {
			return invalid
		}
		if v.Status == "cancelled" && v.CancelledAt == "" {
			return invalid
		}
		anchor := v.BillingAnchor
		if anchor == "" {
			anchor = v.StartedAt
		}
		if v.TrialEndsAt != "" && anchor[:10] < v.TrialEndsAt[:10] {
			return invalid
		}
		subs[v.ID] = true
	}
	ids = unique()
	periods := map[string]bool{}
	for _, v := range b.Occurrences {
		key := strconv.FormatInt(v.SubscriptionID, 10) + ":" + v.Period
		_, _, err := parsePeriod(v.Period)
		if !ids(v.ID) || !subs[v.SubscriptionID] || len(v.Period) != 7 || err != nil || !date(v.ScheduledDate, false) || v.ScheduledDate[:7] != v.Period || v.Amount < 0 || !validCode(v.Currency) || periods[key] {
			return invalid
		}
		periods[key] = true
	}
	ids = unique()
	for _, v := range b.PriceHistory {
		if !ids(v.ID) || !subs[v.SubscriptionID] || v.Amount < 0 || !validCode(v.Currency) || !date(v.EffectiveFrom, false) {
			return invalid
		}
	}
	ids = unique()
	for _, v := range b.Activities {
		if !ids(v.ID) || (v.SubscriptionID != nil && !subs[*v.SubscriptionID]) || !date(v.OccurredAt, false) || (v.OldAmount != nil && *v.OldAmount < 0) || (v.NewAmount != nil && *v.NewAmount < 0) || (v.OldCurrency != nil && !validCode(*v.OldCurrency)) || (v.NewCurrency != nil && !validCode(*v.NewCurrency)) {
			return invalid
		}
	}
	return nil
}
