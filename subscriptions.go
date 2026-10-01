package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

type subInput struct {
	ServiceID                                                        *int64 `json:"serviceId"`
	ServiceName, Icon, Color, Category, Currency, BillingCycle, Memo string
	TrialEndsAt                                                      string `json:"trialEndsAt"`
	BillingDate                                                      string `json:"billingDate"`
	Amount                                                           int64
	BillingDay                                                       int
	PaymentMethodID                                                  int64
}

func validateSub(v subInput) error {
	if strings.TrimSpace(v.ServiceName) == "" {
		return errors.New("서비스명을 입력해 주세요")
	}
	if v.Amount < 0 {
		return errors.New("금액을 확인해 주세요")
	}
	if _, err := time.Parse("2006-01-02", v.BillingDate); err != nil {
		return errors.New("결제 날짜를 선택해 주세요")
	}
	if v.TrialEndsAt != "" {
		trialEnd, err := time.Parse("2006-01-02", v.TrialEndsAt)
		if err != nil {
			return errors.New("무료 체험 종료일을 확인해 주세요")
		}
		billingDate, _ := time.Parse("2006-01-02", v.BillingDate)
		if billingDate.Before(trialEnd) {
			return errors.New("첫 결제일은 무료 체험 종료일 이후여야 해요")
		}
	}
	if v.BillingCycle != "monthly" && v.BillingCycle != "yearly" {
		return errors.New("결제 주기를 확인해 주세요")
	}
	if !currencyCodePattern.MatchString(strings.ToUpper(v.Currency)) {
		return errors.New("통화를 확인해 주세요")
	}
	if v.PaymentMethodID < 1 {
		return errors.New("결제수단을 선택해 주세요")
	}
	return nil
}
func (a *application) createSubscription(w http.ResponseWriter, r *http.Request) {
	var v subInput
	if !decode(w, r, &v) {
		return
	}
	v.Currency = strings.ToUpper(strings.TrimSpace(v.Currency))
	if v.Currency == "" {
		v.Currency = "KRW"
	}
	if !validOrError(w, validateSub(v)) {
		return
	}
	if v.Icon == "" {
		v.Icon = initial(v.ServiceName)
	}
	if v.Color == "" {
		v.Color = "#9AB8A8"
	}
	billingDate, _ := time.Parse("2006-01-02", v.BillingDate)
	v.BillingDay = billingDate.Day()
	startedAt := time.Now().In(a.location).Format("2006-01-02")
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	if !a.validSubscriptionReferences(w, r.Context(), tx, v, 0) {
		return
	}

	res, err := tx.ExecContext(r.Context(),
		`INSERT INTO subscriptions(
			service_id,
			service_name,
			icon,
			color,
			amount,
			currency,
			billing_cycle,
			billing_day,
			billing_anchor,
			payment_method_id,
			category,
			memo,
			started_at,
			trial_ends_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ServiceID,
		strings.TrimSpace(v.ServiceName),
		v.Icon,
		v.Color,
		v.Amount,
		v.Currency,
		v.BillingCycle,
		v.BillingDay,
		v.BillingDate,
		v.PaymentMethodID,
		strings.TrimSpace(v.Category),
		strings.TrimSpace(v.Memo),
		startedAt,
		v.TrialEndsAt,
	)
	if err != nil {
		a.fail(w, err)
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		a.fail(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,?,?,?)`, id, v.Amount, v.Currency, startedAt); err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO activity_events(subscription_id,event_type,service_name,new_amount,new_currency) VALUES(?,'added',?,?,?)`, id, strings.TrimSpace(v.ServiceName), v.Amount, v.Currency)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (a *application) updateSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var v subInput
	if !decode(w, r, &v) {
		return
	}
	v.Currency = strings.ToUpper(strings.TrimSpace(v.Currency))
	if !validOrError(w, validateSub(v)) {
		return
	}
	billingDate, _ := time.Parse("2006-01-02", v.BillingDate)
	v.BillingDay = billingDate.Day()
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	var oldCurrency string
	var oldAmount, oldMethod int64
	if err := tx.QueryRowContext(r.Context(), `SELECT amount,currency,payment_method_id FROM subscriptions WHERE id=? AND status='active'`, id).Scan(&oldAmount, &oldCurrency, &oldMethod); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	if !a.validSubscriptionReferences(w, r.Context(), tx, v, oldMethod) {
		return
	}
	res, err := tx.ExecContext(r.Context(),
		`UPDATE subscriptions SET
			service_id=?,
			service_name=?,
			icon=?,
			color=?,
			amount=?,
			currency=?,
			billing_cycle=?,
			billing_day=?,
			billing_anchor=?,
			payment_method_id=?,
			category=?,
			memo=?,
			trial_ends_at=?,
			updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status='active'`,
		v.ServiceID,
		strings.TrimSpace(v.ServiceName),
		v.Icon,
		v.Color,
		v.Amount,
		v.Currency,
		v.BillingCycle,
		v.BillingDay,
		v.BillingDate,
		v.PaymentMethodID,
		strings.TrimSpace(v.Category),
		strings.TrimSpace(v.Memo),
		v.TrialEndsAt,
		id,
	)
	if err != nil {
		a.fail(w, err)
		return
	}
	if oldAmount != v.Amount || oldCurrency != v.Currency {
		effective := time.Now().In(a.location).Format("2006-01-02")
		_, err = tx.ExecContext(r.Context(), `INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) VALUES(?,?,?,?)`, id, v.Amount, v.Currency, effective)
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `INSERT INTO activity_events(subscription_id,event_type,service_name,old_amount,old_currency,new_amount,new_currency) VALUES(?,'price_changed',?,?,?,?,?)`, id, strings.TrimSpace(v.ServiceName), oldAmount, oldCurrency, v.Amount, v.Currency)
		}
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	changed(w, res)
}

func (a *application) skipSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var v struct {
		Skipped *bool `json:"skipped"`
	}
	if !decode(w, r, &v) {
		return
	}
	if v.Skipped == nil {
		bad(w, "결제 건너뛰기 상태를 입력해 주세요")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	now := time.Now().In(a.location)
	var amount int64
	var day int
	var currency string
	if err := tx.QueryRowContext(r.Context(), `SELECT amount,billing_day,currency FROM subscriptions WHERE id=? AND status='active'`, id).Scan(&amount, &day, &currency); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	lastDay := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, a.location).Day()
	date := time.Date(
		now.Year(),
		now.Month(),
		min(day, lastDay),
		0,
		0,
		0,
		0,
		a.location,
	).Format("2006-01-02")
	baseAmount, baseCurrency := amount, currency
	var storedDate string
	err = tx.QueryRowContext(r.Context(), `SELECT scheduled_date,amount,currency FROM subscription_occurrences WHERE subscription_id=? AND period=?`, id, now.Format("2006-01")).Scan(&storedDate, &amount, &currency)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.fail(w, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) || storedDate != date {
		amount, currency = baseAmount, baseCurrency
		// A skip changes inclusion, not the price of an already billed period.
		// Preserve an existing snapshot when its scheduled date stays the same;
		// otherwise select the price effective on the new billing date.
		err = tx.QueryRowContext(r.Context(), `SELECT amount,currency FROM subscription_price_history WHERE subscription_id=? AND effective_from<=? ORDER BY effective_from DESC,id DESC LIMIT 1`, id, date).Scan(&amount, &currency)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			a.fail(w, err)
			return
		}
	}
	_, err = tx.ExecContext(r.Context(),
		`INSERT INTO subscription_occurrences(
			subscription_id,
			period,
			scheduled_date,
			amount,
			currency,
			skipped
		) VALUES(?,?,?,?,?,?)
		ON CONFLICT(subscription_id,period) DO UPDATE SET
			skipped=excluded.skipped,
			scheduled_date=excluded.scheduled_date,
			amount=excluded.amount,
			currency=excluded.currency,
			updated_at=CURRENT_TIMESTAMP`,
		id,
		now.Format("2006-01"),
		date,
		amount,
		currency,
		*v.Skipped,
	)
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

func (a *application) cancelSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	var name string
	if err := tx.QueryRowContext(r.Context(), `SELECT service_name FROM subscriptions WHERE id=? AND status='active'`, id).Scan(&name); err != nil {
		notFoundOrFail(a, w, err)
		return
	}
	res, err := tx.ExecContext(r.Context(), `UPDATE subscriptions SET status='cancelled',cancelled_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='active'`, time.Now().In(a.location).Format("2006-01-02"), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO activity_events(subscription_id,event_type,service_name) VALUES(?,'cancelled',?)`, id, name); err != nil {
		a.fail(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	changed(w, res)
}

func (a *application) validSubscriptionReferences(w http.ResponseWriter, ctx context.Context, tx *sql.Tx, v subInput, retainedMethod int64) bool {
	var currencyOK, methodOK, serviceOK bool
	err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM currencies WHERE code=? AND archived=0),
		EXISTS(SELECT 1 FROM payment_methods WHERE id=? AND (archived=0 OR id=?)),
		(? IS NULL OR EXISTS(SELECT 1 FROM services WHERE id=?))`,
		v.Currency, v.PaymentMethodID, retainedMethod, v.ServiceID, v.ServiceID).Scan(&currencyOK, &methodOK, &serviceOK)
	if err != nil {
		a.fail(w, err)
		return false
	}
	switch {
	case !currencyOK:
		bad(w, "사용할 수 없는 통화예요")
	case !methodOK:
		bad(w, "사용할 수 없는 결제수단이에요")
	case !serviceOK:
		bad(w, "사용할 수 없는 서비스예요")
	default:
		return true
	}
	return false
}
