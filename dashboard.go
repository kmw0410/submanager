package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type paymentMethod struct {
	ID                  int64  `json:"id"`
	Name                string `json:"name"`
	IsBuiltin, Archived bool
}

type currencyOption struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Digits    int    `json:"digits"`
	IsBuiltin bool   `json:"isBuiltin"`
	Archived  bool   `json:"archived"`
}

type subscription struct {
	ID                                                                                          int64  `json:"id"`
	ServiceID                                                                                   *int64 `json:"serviceId,omitempty"`
	ServiceName, Icon, Color, Category, Currency, BillingCycle, PaymentMethodName, Status, Memo string
	Amount                                                                                      int64 `json:"amount"`
	BillingDay                                                                                  int   `json:"billingDay"`
	PaymentMethodID                                                                             int64 `json:"paymentMethodId"`
	NextPayment, CreatedAt, CancelledAt                                                         string
	BillingDate                                                                                 string `json:"billingDate"`
	TrialEndsAt                                                                                 string `json:"trialEndsAt"`
	IsTrial                                                                                     bool   `json:"isTrial"`
	Skipped                                                                                     bool   `json:"skipped"`
}

type currencyStat struct {
	Currency      string  `json:"currency"`
	MonthTotal    int64   `json:"monthTotal"`
	YearEstimate  int64   `json:"yearEstimate"`
	PreviousTotal int64   `json:"previousTotal"`
	Delta         int64   `json:"delta"`
	MonthlyTotals []int64 `json:"monthlyTotals"`
}

type userState struct {
	Name     string
	Email    string
	Currency string
}

type settingsState struct {
	NotifyDays       int
	DiscordWebhook   string
	DiscordEnabled   bool
	TelegramBotToken string
	TelegramChatID   string
	TelegramEnabled  bool
	PWAEnabled       bool
	NotifyUpcoming   bool
	NotifyChanges    bool
	NotifyMonthly    bool
}

type sessionState struct {
	ID        int64  `json:"id"`
	Device    string `json:"device"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
}

type dashboardStats struct {
	ActiveCount   int
	UpcomingCount int
	Months        []string       `json:"months"`
	Currencies    []currencyStat `json:"currencies"`
	Greeting      string
	Summary       string
}

type appState struct {
	User           userState        `json:"user"`
	Settings       settingsState    `json:"settings"`
	Services       []service        `json:"services"`
	PaymentMethods []paymentMethod  `json:"paymentMethods"`
	Currencies     []currencyOption `json:"currencies"`
	Subscriptions  []subscription   `json:"subscriptions"`
	Stats          dashboardStats   `json:"stats"`
}

type paymentOccurrence struct {
	SubscriptionID    int64  `json:"subscriptionId"`
	ServiceName       string `json:"serviceName"`
	Color             string `json:"color"`
	Amount            int64  `json:"amount"`
	Currency          string `json:"currency"`
	ScheduledDate     string `json:"scheduledDate"`
	PaymentMethodName string `json:"paymentMethodName"`
	BillingCycle      string `json:"billingCycle"`
	Skipped           bool   `json:"skipped"`
	FirstPayment      bool   `json:"firstPayment"`
}

type upcomingMonth struct {
	Period string              `json:"period"`
	Items  []paymentOccurrence `json:"items"`
	Totals map[string]int64    `json:"totals"`
}

type subscriptionPricePoint struct {
	Amount                  int64
	Currency, EffectiveFrom string
}

func (a *application) getState(w http.ResponseWriter, _ *http.Request) {
	s, e := a.loadState()
	if e != nil {
		a.fail(w, e)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *application) loadState() (appState, error) {
	var s appState
	s.Services = []service{}
	s.PaymentMethods = []paymentMethod{}
	s.Currencies = []currencyOption{}
	s.Subscriptions = []subscription{}
	s.Stats.Months = []string{}
	s.Stats.Currencies = []currencyStat{}
	stateQuery := `
		SELECT
			u.name,
			u.email,
			u.currency,
			n.days_before,
			c.discord_webhook,
			c.discord_enabled,
			c.telegram_bot_token,
			c.telegram_chat_id,
			c.telegram_enabled,
			c.pwa_enabled,
			n.notify_upcoming,
			n.notify_changes,
			n.notify_monthly
		FROM users u, notification_rules n, notification_channels c
		WHERE u.id=1 AND n.id=1 AND c.id=1`
	err := a.db.QueryRow(stateQuery).Scan(
		&s.User.Name,
		&s.User.Email,
		&s.User.Currency,
		&s.Settings.NotifyDays,
		&s.Settings.DiscordWebhook,
		&s.Settings.DiscordEnabled,
		&s.Settings.TelegramBotToken,
		&s.Settings.TelegramChatID,
		&s.Settings.TelegramEnabled,
		&s.Settings.PWAEnabled,
		&s.Settings.NotifyUpcoming,
		&s.Settings.NotifyChanges,
		&s.Settings.NotifyMonthly,
	)
	if err != nil {
		return s, err
	}
	rows, err := a.db.Query(`
		SELECT
			id,
			name,
			icon,
			default_category,
			default_billing_cycle,
			default_currency,
			color,
			supports_trial
		FROM services
		ORDER BY id`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var v service
		if err := rows.Scan(
			&v.ID,
			&v.Name,
			&v.Icon,
			&v.Category,
			&v.BillingCycle,
			&v.Currency,
			&v.Color,
			&v.SupportsTrial,
		); err != nil {
			return s, err
		}
		s.Services = append(s.Services, v)
	}
	pm, err := a.db.Query(`SELECT id,name,is_builtin,archived FROM payment_methods ORDER BY is_builtin DESC,id`)
	if err != nil {
		return s, err
	}
	defer pm.Close()
	for pm.Next() {
		var v paymentMethod
		if err := pm.Scan(&v.ID, &v.Name, &v.IsBuiltin, &v.Archived); err != nil {
			return s, err
		}
		s.PaymentMethods = append(s.PaymentMethods, v)
	}
	currencyRows, err := a.db.Query(`SELECT id,code,name,is_builtin,archived FROM currencies ORDER BY is_builtin DESC,id`)
	if err != nil {
		return s, err
	}
	for currencyRows.Next() {
		var v currencyOption
		if err := currencyRows.Scan(&v.ID, &v.Code, &v.Name, &v.IsBuiltin, &v.Archived); err != nil {
			currencyRows.Close()
			return s, err
		}
		v.Digits = currencyFractionDigits(v.Code)
		s.Currencies = append(s.Currencies, v)
	}
	if err := currencyRows.Close(); err != nil {
		return s, err
	}
	s.Subscriptions, err = a.loadSubscriptions()
	if err != nil {
		return s, err
	}
	if s.Subscriptions == nil {
		s.Subscriptions = []subscription{}
	}
	now := time.Now().In(a.location)
	currencyStats := map[string]*currencyStat{}
	currencyOrder := []string{}
	addCurrency := func(currency string) {
		currency = strings.ToUpper(currency)
		if _, ok := currencyStats[currency]; !ok {
			currencyStats[currency] = &currencyStat{Currency: currency, MonthlyTotals: []int64{}}
			currencyOrder = append(currencyOrder, currency)
		}
	}
	activeSubscriptionIDs := make(map[int64]struct{})
	for i := range s.Subscriptions {
		v := &s.Subscriptions[i]
		if v.TrialEndsAt != "" {
			if end, err := time.ParseInLocation("2006-01-02", v.TrialEndsAt, a.location); err == nil {
				v.IsTrial = now.Before(end.AddDate(0, 0, 1))
			}
		}
		v.NextPayment = nextPayment(now, v.BillingDay, v.BillingCycle, v.BillingDate)
		if v.Status == "active" {
			s.Stats.ActiveCount++
			activeSubscriptionIDs[v.ID] = struct{}{}
			currency := strings.ToUpper(v.Currency)
			addCurrency(currency)
			due, _ := time.ParseInLocation("2006-01-02", v.NextPayment, a.location)
			if due.Sub(now) <= 7*24*time.Hour {
				s.Stats.UpcomingCount++
			}
			if v.BillingCycle == "yearly" {
				currencyStats[currency].YearEstimate += v.Amount
			} else {
				currencyStats[currency].YearEstimate += v.Amount * 12
			}
		}
	}
	priceHistory, err := a.loadSubscriptionPriceHistory()
	if err != nil {
		return s, err
	}
	for subscriptionID, points := range priceHistory {
		if _, active := activeSubscriptionIDs[subscriptionID]; !active {
			continue
		}
		for _, point := range points {
			addCurrency(point.Currency)
		}
	}
	sort.SliceStable(currencyOrder, func(i, j int) bool {
		if currencyOrder[i] == currencyOrder[j] {
			return false
		}
		if currencyOrder[i] == s.User.Currency {
			return true
		}
		if currencyOrder[j] == s.User.Currency {
			return false
		}
		return currencyOrder[i] < currencyOrder[j]
	})
	for i := -5; i <= 0; i++ {
		d := now.AddDate(0, i, 0)
		p := d.Format("2006-01")
		s.Stats.Months = append(s.Stats.Months, d.Format("1월"))
		month, err := a.loadPaymentOccurrencesWithPriceHistory(p, priceHistory)
		if err != nil {
			return s, err
		}
		totals := month.Totals
		for _, currency := range currencyOrder {
			stat := currencyStats[currency]
			value := totals[currency]
			stat.MonthlyTotals = append(stat.MonthlyTotals, value)
			if i == -1 {
				stat.PreviousTotal = value
			}
			if i == 0 {
				stat.MonthTotal = value
			}
		}
	}
	for _, currency := range currencyOrder {
		stat := currencyStats[currency]
		stat.Delta = stat.MonthTotal - stat.PreviousTotal
		s.Stats.Currencies = append(s.Stats.Currencies, *stat)
	}
	s.Stats.Greeting = "안녕하세요, " + s.User.Name + "님."
	if summary, ok, err := a.latestActivitySummary(now); err != nil {
		return s, err
	} else if ok {
		s.Stats.Summary = summary
	} else if s.Stats.ActiveCount == 0 {
		s.Stats.Summary = "첫 구독을 추가하면 한눈에 정리해 드릴게요."
	} else if len(s.Stats.Currencies) > 1 {
		s.Stats.Summary = "이번 달 구독비를 통화별로 정리했어요."
	} else if abs(s.Stats.Currencies[0].Delta) < 1 {
		s.Stats.Summary = "이번 달은 저번 달과 비슷해요."
	} else if s.Stats.Currencies[0].Delta > 0 {
		s.Stats.Summary = "이번 달은 저번 달보다 " + money(s.Stats.Currencies[0].Delta, s.Stats.Currencies[0].Currency) + " 더 나가요."
	} else {
		s.Stats.Summary = "이번 달은 저번 달보다 " + money(-s.Stats.Currencies[0].Delta, s.Stats.Currencies[0].Currency) + " 적게 나가요."
	}
	return s, nil
}

func (a *application) latestActivitySummary(now time.Time) (string, bool, error) {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, a.location).UTC().Format("2006-01-02 15:04:05")
	var eventType, name string
	var oldAmount, newAmount sql.NullInt64
	var oldCurrency, newCurrency sql.NullString
	err := a.db.QueryRow(`SELECT event_type,service_name,old_amount,old_currency,new_amount,new_currency FROM activity_events WHERE occurred_at>=? ORDER BY occurred_at DESC,id DESC LIMIT 1`, monthStart).Scan(&eventType, &name, &oldAmount, &oldCurrency, &newAmount, &newCurrency)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	switch eventType {
	case "added":
		return "이번 달은 " + name + " (" + money(newAmount.Int64, newCurrency.String) + ")이 추가되었어요.", true, nil
	case "cancelled":
		return "이번 달은 " + name + " 구독이 삭제되었어요.", true, nil
	case "price_changed":
		return name + "의 금액이 " + money(oldAmount.Int64, oldCurrency.String) + "에서 " + money(newAmount.Int64, newCurrency.String) + "으로 변경됐어요.", true, nil
	default:
		return "", false, nil
	}
}

func (a *application) loadSubscriptions() ([]subscription, error) {
	query := `
		SELECT
			s.id,
			s.service_id,
			s.service_name,
			s.icon,
			s.color,
			s.amount,
			s.currency,
			s.billing_cycle,
			s.billing_day,
			s.payment_method_id,
			p.name,
			s.category,
			s.memo,
			s.status,
			s.started_at,
			COALESCE(NULLIF(s.billing_anchor,''),s.started_at),
			COALESCE(s.cancelled_at,''),
			COALESCE(s.trial_ends_at,''),
			COALESCE(o.skipped,0)
		FROM subscriptions s
		JOIN payment_methods p ON p.id=s.payment_method_id
		LEFT JOIN subscription_occurrences o
			ON o.subscription_id=s.id AND o.period=?
		ORDER BY s.status,s.billing_day,s.id`
	period := time.Now().In(a.location).Format("2006-01")
	rows, err := a.db.Query(query, period)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subscription
	for rows.Next() {
		var v subscription
		if err := rows.Scan(
			&v.ID,
			&v.ServiceID,
			&v.ServiceName,
			&v.Icon,
			&v.Color,
			&v.Amount,
			&v.Currency,
			&v.BillingCycle,
			&v.BillingDay,
			&v.PaymentMethodID,
			&v.PaymentMethodName,
			&v.Category,
			&v.Memo,
			&v.Status,
			&v.CreatedAt,
			&v.BillingDate,
			&v.CancelledAt,
			&v.TrialEndsAt,
			&v.Skipped,
		); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (a *application) monthTotals(period string) (map[string]int64, error) {
	month, err := a.loadPaymentOccurrences(period)
	if err != nil {
		return nil, err
	}
	return month.Totals, nil
}

func (a *application) loadPaymentOccurrences(period string) (upcomingMonth, error) {
	history, err := a.loadSubscriptionPriceHistory()
	if err != nil {
		return upcomingMonth{}, err
	}
	return a.loadPaymentOccurrencesWithPriceHistory(period, history)
}

func (a *application) loadSubscriptionPriceHistory() (map[int64][]subscriptionPricePoint, error) {
	history := map[int64][]subscriptionPricePoint{}
	rows, err := a.db.Query(`SELECT subscription_id,amount,currency,effective_from FROM subscription_price_history ORDER BY subscription_id,effective_from,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var subscriptionID int64
		var point subscriptionPricePoint
		if err := rows.Scan(&subscriptionID, &point.Amount, &point.Currency, &point.EffectiveFrom); err != nil {
			return nil, err
		}
		history[subscriptionID] = append(history[subscriptionID], point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return history, nil
}

func (a *application) loadPaymentOccurrencesWithPriceHistory(period string, history map[int64][]subscriptionPricePoint) (upcomingMonth, error) {
	y, m, err := parsePeriod(period)
	if err != nil {
		return upcomingMonth{}, err
	}
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, a.location).Day()
	rows, err := a.db.Query(`SELECT s.id,s.service_name,s.color,s.amount,s.currency,s.billing_cycle,s.billing_day,COALESCE(NULLIF(s.billing_anchor,''),s.started_at),COALESCE(s.cancelled_at,''),COALESCE(s.trial_ends_at,''),p.name,COALESCE(o.skipped,0),o.amount,o.currency FROM subscriptions s JOIN payment_methods p ON p.id=s.payment_method_id LEFT JOIN subscription_occurrences o ON o.subscription_id=s.id AND o.period=?`, period)
	if err != nil {
		return upcomingMonth{}, err
	}
	defer rows.Close()
	month := upcomingMonth{Period: period, Items: []paymentOccurrence{}, Totals: map[string]int64{}}
	for rows.Next() {
		var id, day int
		var amount int64
		var serviceName, color, currency, cycle, anchor, cancelled, trialEndsAt, paymentMethodName string
		var skipped bool
		var occurrenceAmount sql.NullInt64
		var occurrenceCurrency sql.NullString
		if err := rows.Scan(&id, &serviceName, &color, &amount, &currency, &cycle, &day, &anchor, &cancelled, &trialEndsAt, &paymentMethodName, &skipped, &occurrenceAmount, &occurrenceCurrency); err != nil {
			return upcomingMonth{}, err
		}
		billDate := time.Date(y, m, min(day, last), 0, 0, 0, 0, a.location)
		billingAnchor, parseErr := time.ParseInLocation("2006-01-02", anchor[:10], a.location)
		if parseErr != nil {
			continue
		}
		if billDate.Before(billingAnchor) {
			continue
		}
		if cancelled != "" {
			c, _ := time.ParseInLocation("2006-01-02", cancelled[:10], a.location)
			if billDate.After(c) {
				continue
			}
		}
		if cycle == "yearly" && int(m) != int(billingAnchor.Month()) {
			continue
		}
		if occurrenceAmount.Valid {
			amount = occurrenceAmount.Int64
			if occurrenceCurrency.Valid && occurrenceCurrency.String != "" {
				currency = occurrenceCurrency.String
			}
		} else {
			billDateText := billDate.Format("2006-01-02")
			for _, point := range history[int64(id)] {
				if point.EffectiveFrom <= billDateText {
					amount = point.Amount
					currency = point.Currency
				} else {
					break
				}
			}
		}
		currency = strings.ToUpper(currency)
		month.Items = append(month.Items, paymentOccurrence{
			SubscriptionID:    int64(id),
			ServiceName:       serviceName,
			Color:             color,
			Amount:            amount,
			Currency:          currency,
			ScheduledDate:     billDate.Format("2006-01-02"),
			PaymentMethodName: paymentMethodName,
			BillingCycle:      cycle,
			Skipped:           skipped,
			FirstPayment:      trialEndsAt != "" && billDate.Equal(billingAnchor),
		})
		if !skipped {
			month.Totals[currency] += amount
		}
	}
	if err := rows.Err(); err != nil {
		return upcomingMonth{}, err
	}
	sort.SliceStable(month.Items, func(i, j int) bool {
		if month.Items[i].ScheduledDate == month.Items[j].ScheduledDate {
			return month.Items[i].ServiceName < month.Items[j].ServiceName
		}
		return month.Items[i].ScheduledDate < month.Items[j].ScheduledDate
	})
	return month, nil
}

func (a *application) getUpcomingMonth(w http.ResponseWriter, r *http.Request) {
	period := strings.TrimSpace(r.URL.Query().Get("month"))
	if len(period) != 7 {
		bad(w, "조회할 연월을 YYYY-MM 형식으로 입력해 주세요")
		return
	}
	if _, _, err := parsePeriod(period); err != nil {
		bad(w, "조회할 연월을 YYYY-MM 형식으로 입력해 주세요")
		return
	}
	month, err := a.loadPaymentOccurrences(period)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, month)
}

func (a *application) exportUpcoming(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("format") != "ics" {
		bad(w, "지원하는 내보내기 형식은 ICS예요")
		return
	}
	months, err := strconv.Atoi(r.URL.Query().Get("months"))
	if err != nil || (months != 1 && months != 3 && months != 12) {
		bad(w, "내보낼 기간은 이번 달, 3개월 또는 1년 중에서 선택해 주세요")
		return
	}
	now := time.Now().In(a.location)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.location)
	end := time.Date(now.Year(), now.Month()+time.Month(months), 0, 0, 0, 0, 0, a.location)
	calendar, err := a.upcomingICS(start, end)
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="submanager-payments.ics"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(calendar))
}

func (a *application) upcomingICS(start, end time.Time) (string, error) {
	items := make([]paymentOccurrence, 0)
	dtstamp := time.Now().UTC().Format("20060102T150405Z")
	for month := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, a.location); !month.After(end); month = month.AddDate(0, 1, 0) {
		occurrences, err := a.loadPaymentOccurrences(month.Format("2006-01"))
		if err != nil {
			return "", err
		}
		for _, item := range occurrences.Items {
			date, err := time.ParseInLocation("2006-01-02", item.ScheduledDate, a.location)
			if err == nil && !item.Skipped && !date.Before(start) && !date.After(end) {
				items = append(items, item)
			}
		}
	}

	var calendar strings.Builder
	for _, line := range []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//SubManager//Payment Calendar//KO", "CALSCALE:GREGORIAN"} {
		writeICSLine(&calendar, line)
	}
	for _, item := range items {
		date, _ := time.ParseInLocation("2006-01-02", item.ScheduledDate, a.location)
		description := strings.Join([]string{
			"서비스: " + item.ServiceName,
			"금액: " + item.Currency + " " + formatMinorUnits(item.Amount, item.Currency),
			"결제수단: " + item.PaymentMethodName,
			"결제주기: " + map[bool]string{true: "연간", false: "월간"}[item.BillingCycle == "yearly"],
		}, "\n")
		for _, line := range []string{
			"BEGIN:VEVENT",
			fmt.Sprintf("UID:%d-%s@submanager", item.SubscriptionID, date.Format("20060102")),
			"DTSTAMP:" + dtstamp,
			"DTSTART;VALUE=DATE:" + date.Format("20060102"),
			"DTEND;VALUE=DATE:" + date.AddDate(0, 0, 1).Format("20060102"),
			"SUMMARY:" + escapeICSText(item.ServiceName+" 결제"),
			"DESCRIPTION:" + escapeICSText(description),
			"END:VEVENT",
		} {
			writeICSLine(&calendar, line)
		}
	}
	writeICSLine(&calendar, "END:VCALENDAR")
	return calendar.String(), nil
}

func escapeICSText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, ";", "\\;")
	return strings.ReplaceAll(value, ",", "\\,")
}

func writeICSLine(calendar *strings.Builder, line string) {
	const limit = 75
	first := true
	for len(line) > 0 {
		available := limit
		if !first {
			calendar.WriteByte(' ')
			available--
		}
		cut := min(len(line), available)
		for cut > 0 && cut < len(line) && line[cut]&0xc0 == 0x80 {
			cut--
		}
		calendar.WriteString(line[:cut])
		calendar.WriteString("\r\n")
		line = line[cut:]
		first = false
	}
}
