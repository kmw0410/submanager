package main

import (
	"database/sql"
	"strings"
)

type service struct {
	ID                                                  int64
	Name, Icon, Category, BillingCycle, Currency, Color string
	SupportsTrial                                       bool
}

func (a *application) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY CHECK(id=1),
    name TEXT NOT NULL DEFAULT '사용자',
    currency TEXT NOT NULL DEFAULT 'KRW',
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS services (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    icon TEXT NOT NULL,
    default_category TEXT NOT NULL DEFAULT '',
    default_billing_cycle TEXT NOT NULL DEFAULT 'monthly',
    default_currency TEXT NOT NULL DEFAULT 'KRW',
    color TEXT NOT NULL DEFAULT '#9AB8A8',
    is_builtin INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS payment_methods (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    type TEXT NOT NULL DEFAULT 'custom',
    is_builtin INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS currencies (
    id INTEGER PRIMARY KEY,
    code TEXT NOT NULL COLLATE NOCASE UNIQUE,
    name TEXT NOT NULL DEFAULT '',
    is_builtin INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS categories (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    color TEXT NOT NULL DEFAULT '#9AB8A8'
);
CREATE TABLE IF NOT EXISTS subscriptions (
    id INTEGER PRIMARY KEY,
    service_id INTEGER REFERENCES services(id),
    service_name TEXT NOT NULL,
    icon TEXT NOT NULL DEFAULT 'S',
    color TEXT NOT NULL DEFAULT '#9AB8A8',
    amount INTEGER NOT NULL CHECK(amount >= 0),
    currency TEXT NOT NULL DEFAULT 'KRW',
    billing_cycle TEXT NOT NULL CHECK(billing_cycle IN ('monthly','yearly')),
    billing_day INTEGER NOT NULL CHECK(billing_day BETWEEN 1 AND 31),
    billing_anchor TEXT NOT NULL DEFAULT '',
    payment_method_id INTEGER NOT NULL REFERENCES payment_methods(id),
    category TEXT NOT NULL DEFAULT '',
    memo TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','cancelled')),
    started_at TEXT NOT NULL DEFAULT CURRENT_DATE,
    cancelled_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS subscription_occurrences (
    id INTEGER PRIMARY KEY,
    subscription_id INTEGER NOT NULL REFERENCES subscriptions(id),
    period TEXT NOT NULL,
    scheduled_date TEXT NOT NULL,
    amount INTEGER NOT NULL,
    skipped INTEGER NOT NULL DEFAULT 0,
    paid INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(subscription_id, period)
);
CREATE TABLE IF NOT EXISTS subscription_price_history (
    id INTEGER PRIMARY KEY,
    subscription_id INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    amount INTEGER NOT NULL CHECK(amount >= 0),
    currency TEXT NOT NULL,
    effective_from TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_price_history_subscription_date
    ON subscription_price_history(subscription_id,effective_from,id);
CREATE TABLE IF NOT EXISTS activity_events (
    id INTEGER PRIMARY KEY,
    subscription_id INTEGER REFERENCES subscriptions(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    service_name TEXT NOT NULL,
    old_amount INTEGER,
    old_currency TEXT,
    new_amount INTEGER,
    new_currency TEXT,
    occurred_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_activity_events_date
    ON activity_events(occurred_at,id);
CREATE TABLE IF NOT EXISTS notification_channels (
    id INTEGER PRIMARY KEY CHECK(id=1),
    discord_webhook TEXT NOT NULL DEFAULT '',
    discord_enabled INTEGER NOT NULL DEFAULT 1,
    telegram_bot_token TEXT NOT NULL DEFAULT '',
    telegram_chat_id TEXT NOT NULL DEFAULT '',
    telegram_enabled INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS notification_rules (
    id INTEGER PRIMARY KEY CHECK(id=1),
    notify_upcoming INTEGER NOT NULL DEFAULT 1,
    notify_changes INTEGER NOT NULL DEFAULT 1,
    notify_monthly INTEGER NOT NULL DEFAULT 1,
    days_before INTEGER NOT NULL DEFAULT 3,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS notification_deliveries (
    id INTEGER PRIMARY KEY,
    delivery_key TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS app_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT OR IGNORE INTO users(id,name,currency) VALUES(1,'사용자','KRW');
INSERT OR IGNORE INTO notification_channels(id) VALUES(1);
INSERT OR IGNORE INTO notification_rules(id) VALUES(1);
`
	if _, err := a.db.Exec(schema); err != nil {
		return err
	}
	if err := a.ensureColumn("subscriptions", "billing_anchor", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := a.ensureColumn("subscriptions", "trial_ends_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := a.ensureColumn("services", "supports_trial", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := a.ensureColumn("subscription_occurrences", "currency", "TEXT NOT NULL DEFAULT 'KRW'"); err != nil {
		return err
	}
	if err := a.ensureColumn("sessions", "user_agent", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := a.ensureColumn("notification_channels", "discord_enabled", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := a.ensureColumn("notification_channels", "telegram_enabled", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	columns := []struct {
		name       string
		definition string
	}{
		{"email", "TEXT NOT NULL DEFAULT ''"},
		{"password_hash", "TEXT NOT NULL DEFAULT ''"},
		{"is_admin", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		if err := a.ensureColumn("users", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := a.migrateAmountsToMinorUnits(); err != nil {
		return err
	}
	services := []service{
		{Name: "네이버플러스 멤버십", Icon: "N+", Category: "생활", BillingCycle: "monthly", Currency: "KRW", Color: "#8FC9A3"},
		{Name: "YouTube Premium", Icon: "YT", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#D99A9A"},
		{Name: "ChatGPT", Icon: "AI", Category: "AI", BillingCycle: "monthly", Currency: "KRW", Color: "#91B8A7"},
		{Name: "Claude", Icon: "CL", Category: "AI", BillingCycle: "monthly", Currency: "KRW", Color: "#C6A98D"},
		{Name: "Spotify", Icon: "SP", Category: "음악", BillingCycle: "monthly", Currency: "KRW", Color: "#91C89C"},
		{Name: "벅스", Icon: "BG", Category: "음악", BillingCycle: "monthly", Currency: "KRW", Color: "#B2A7D6"},
		{Name: "멜론", Icon: "ML", Category: "음악", BillingCycle: "monthly", Currency: "KRW", Color: "#A6C99A"},
		{Name: "FLO", Icon: "FL", Category: "음악", BillingCycle: "monthly", Currency: "KRW", Color: "#AAB5D8"},
		{Name: "Netflix", Icon: "NF", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#D99191"},
		{Name: "TVING", Icon: "TV", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#D9879B"},
		{Name: "Wavve", Icon: "WV", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#829FD1"},
		{Name: "Disney+", Icon: "D+", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#879BC8"},
		{Name: "WATCHA", Icon: "WA", Category: "영상", BillingCycle: "monthly", Currency: "KRW", Color: "#D98DAA"},
		{Name: "iCloud+", Icon: "iC", Category: "클라우드", BillingCycle: "monthly", Currency: "KRW", Color: "#8CB8D5"},
		{Name: "Google One", Icon: "G1", Category: "클라우드", BillingCycle: "monthly", Currency: "KRW", Color: "#8EB79D"},
		{Name: "쿠팡 와우 멤버십", Icon: "CW", Category: "생활", BillingCycle: "monthly", Currency: "KRW", Color: "#A593CE"},
		{Name: "배민클럽", Icon: "BM", Category: "생활", BillingCycle: "monthly", Currency: "KRW", Color: "#82C6C4"},
		{Name: "밀리의 서재", Icon: "MI", Category: "독서", BillingCycle: "monthly", Currency: "KRW", Color: "#B4C985"},
	}
	for _, s := range services {
		if _, err := a.db.Exec(`INSERT OR IGNORE INTO services(name,icon,default_category,default_billing_cycle,default_currency,color) VALUES(?,?,?,?,?,?)`, s.Name, s.Icon, s.Category, s.BillingCycle, s.Currency, s.Color); err != nil {
			return err
		}
	}
	for _, name := range []string{"신용(체크) 카드", "휴대폰결제", "계좌이체", "네이버페이", "카카오페이"} {
		if _, err := a.db.Exec(`INSERT OR IGNORE INTO payment_methods(name,type,is_builtin) VALUES(?,'builtin',1)`, name); err != nil {
			return err
		}
	}
	currencies := []struct {
		code string
		name string
	}{
		{"KRW", "대한민국 원"},
		{"USD", "미국 달러"},
		{"JPY", "일본 엔"},
		{"EUR", "유로"},
		{"TRY", "튀르키예 리라"},
		{"ARS", "아르헨티나 페소"},
	}
	for _, currency := range currencies {
		if _, err := a.db.Exec(`INSERT OR IGNORE INTO currencies(code,name,is_builtin) VALUES(?,?,1)`, currency.code, currency.name); err != nil {
			return err
		}
	}
	_, _ = a.db.Exec(`UPDATE services SET supports_trial=1 WHERE name IN ('YouTube Premium','ChatGPT','Claude','Spotify','FLO','밀리의 서재')`)
	_, err := a.db.Exec(`INSERT INTO subscription_price_history(subscription_id,amount,currency,effective_from) SELECT s.id,s.amount,s.currency,substr(s.started_at,1,10) FROM subscriptions s WHERE NOT EXISTS(SELECT 1 FROM subscription_price_history h WHERE h.subscription_id=s.id)`)
	if err != nil {
		return err
	}
	return nil
}

func (a *application) migrateAmountsToMinorUnits() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM app_metadata WHERE key='amounts_minor_units_v1'`).Scan(&done); err != nil {
		return err
	}
	if done != 0 {
		return tx.Commit()
	}
	rows, err := tx.Query(`SELECT DISTINCT currency FROM (SELECT currency FROM subscriptions UNION SELECT currency FROM subscription_occurrences UNION SELECT currency FROM subscription_price_history UNION SELECT old_currency FROM activity_events UNION SELECT new_currency FROM activity_events) WHERE currency IS NOT NULL AND currency<>''`)
	if err != nil {
		return err
	}
	var currencies []string
	for rows.Next() {
		var currency string
		if err := rows.Scan(&currency); err != nil {
			rows.Close()
			return err
		}
		currencies = append(currencies, strings.ToUpper(currency))
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, currency := range currencies {
		factor := minorUnitFactor(currency)
		if factor == 1 {
			continue
		}
		for _, query := range []string{
			`UPDATE subscriptions SET amount=amount*? WHERE UPPER(currency)=?`,
			`UPDATE subscription_occurrences SET amount=amount*? WHERE UPPER(currency)=?`,
			`UPDATE subscription_price_history SET amount=amount*? WHERE UPPER(currency)=?`,
			`UPDATE activity_events SET old_amount=old_amount*? WHERE old_amount IS NOT NULL AND UPPER(old_currency)=?`,
			`UPDATE activity_events SET new_amount=new_amount*? WHERE new_amount IS NOT NULL AND UPPER(new_currency)=?`,
		} {
			if _, err := tx.Exec(query, factor, currency); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO app_metadata(key,value) VALUES('amounts_minor_units_v1','1')`); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *application) ensureColumn(table, column, definition string) error {
	rows, err := a.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			return rows.Close()
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = a.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}
