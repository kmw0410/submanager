# AGENTS.md

## 1. Project Overview
Submanager is a lightweight self-hosted subscription management dashboard for Korean users.

Core characteristics:
- The server is a single Go application.
- All application data is stored in SQLite.
- The UI uses server-rendered HTML for the initial page and Vanilla JavaScript for dashboard interactions.
- HTML, CSS, and JavaScript under `web/` are embedded into the Go binary with `embed.FS`.
- Docker operation should remain simple and use a single persistent database volume.

Keep the project small, self-contained, and easy to run without introducing a separate frontend toolchain or external database unless explicitly requested.

## 2. Directory Structure
```text
submanager/
├── .dockerignore
├── .gitignore
├── AGENTS.md
├── Dockerfile
├── README.md
├── compose.yaml
├── go.mod
├── go.sum
├── main.go
├── main_test.go
└── web/
    ├── app.css
    ├── app.js
    ├── auth.html
    └── index.html
```

## 3. Architecture
- Application entry point and HTTP server: `main.go`
  - Environment loading, SQLite setup, migrations and seeds, route registration, middleware, notification worker, and graceful shutdown.
- Storage: SQLite through `database/sql` and `github.com/mattn/go-sqlite3`.
  - The database uses foreign keys, a busy timeout, WAL mode, and one open connection.
- Authentication:
  - The first setup creates the only administrator account.
  - Passwords are stored as bcrypt hashes.
  - Session tokens are random values stored only as SHA-256 hashes in SQLite.
- Frontend: embedded files under `web/`.
  - `auth.html` renders setup and login views.
  - `index.html` provides the authenticated application shell.
  - `app.js` loads and mutates data through the authenticated JSON API.
  - `app.css` owns the complete visual system and responsive behavior.
- Background notifications:
  - A periodic worker sends upcoming-payment and monthly-summary notifications.
  - Discord and Telegram delivery state is stored in SQLite.

Typical runtime data path:
```text
/data/submanager.db
/data/submanager.db-shm
/data/submanager.db-wal
```

## 4. Critical Rules
- Keep SQLite as the sole persistence layer unless a storage redesign is explicitly requested.
- Preserve the single-administrator setup flow; do not add open registration.
- When no administrator exists, generate a new 48-character setup token on every process start, store it only in a mode-0600 file beside the database, and remove the file after setup; never store the token in SQLite or log its value.
- Never store administrator passwords or session tokens in plaintext.
- Keep the browser UI dependency-free. Do not add React, Vue, a Node build, or a package manager unless explicitly requested.
- Keep web assets in `web/` and embedded in the server binary. Update the `go:embed` pattern if new embedded asset locations are introduced.
- Preserve SQLite foreign keys, WAL mode, the busy timeout, and the single-open-connection setting unless a reviewed concurrency change requires otherwise.
- Use parameterized SQL for all values. Never concatenate user input into SQL identifiers or clauses.
- Preserve Korean as the current product language and keep user-facing copy natural and consistent.
- Do not expose secrets in logs, API error bodies, UI notifications, or test failures.
- When a regression or unexpected behavior is reported, inspect relevant version history first when repository history is available.

## 5. Data Model and Migrations
The schema is created and evolved by `application.migrate` in `main.go`.

Important tables:
- `users`: the single administrator profile and password hash.
- `sessions`: hashed login sessions and expiration timestamps.
- `services`: built-in subscription service templates.
- `payment_methods`: built-in and custom payment methods with archive support.
- `currencies`: built-in and custom currency codes with archive support.
- `subscriptions`: current subscription configuration and status.
- `subscription_occurrences`: per-period skip/payment overrides.
- `subscription_price_history`: effective-dated amount and currency history.
- `activity_events`: add, price-change, and cancellation history.
- `notification_channels`, `notification_rules`, `notification_deliveries`: notification configuration and deduplication.

Migration rules:
- Migrations must be idempotent and safe on an existing database.
- Do not delete or silently rewrite user data during startup migration.
- Use `ensureColumn` for compatible additive changes to existing tables.
- Keep foreign-key relationships valid and wrap related writes in transactions.
- Update backup export/import when a persisted field or table is added.
- Add migration and round-trip tests for schema changes.

Seed rules:
- Keep the 8 built-in service templates idempotent.
- Keep the 5 built-in payment methods idempotent and immutable through normal custom-item endpoints.
- Keep the built-in currencies `KRW`, `USD`, `JPY`, `EUR`, `TRY`, and `ARS` idempotent.
- Built-in records must not be duplicated by repeated migrations.

## 6. Subscription and Billing Behavior
- Supported billing cycles are `monthly` and `yearly`.
- Amounts are stored as nonnegative integers; do not introduce floating-point money arithmetic.
- Billing dates use `YYYY-MM-DD` and billing days must remain between 1 and 31.
- End-of-month calculations must clamp invalid days to the last day of the month.
- Yearly subscriptions must remain anchored to their configured billing month.
- A trial end date is optional; when present, the first billing date cannot precede it.
- Cancelling a subscription preserves historical records and marks the subscription `cancelled`.
- Skipping affects only the selected monthly occurrence and must not rewrite the base subscription.
- Changes to amount or currency must append an effective-dated `subscription_price_history` row.
- Historical month totals must use the price and currency effective for that month. Do not recalculate past months using only the current subscription price.
- Related subscription, price-history, and activity-event writes should remain transactional.

## 7. Currency and Payment Method Rules
- Do not convert or combine different currencies.
- Dashboard totals, yearly estimates, deltas, and graphs must remain grouped by currency.
- Custom currency codes must be exactly three ASCII letters and normalized to uppercase.
- A currency must exist and be active before it can be assigned to a subscription or selected as the default.
- Built-in currencies cannot be deleted.
- Used custom currencies and payment methods should be archived rather than physically deleted.
- Unused custom currencies and payment methods may be deleted.
- Built-in payment methods cannot be renamed or deleted through custom-item endpoints.

## 8. Authentication and Sessions
- Initial setup is allowed only while the administrator password hash is empty.
- The setup update must remain conditional so concurrent setup attempts cannot create or replace a second administrator.
- Validate administrator email addresses and keep passwords between 8 and 72 bytes/characters as required by bcrypt handling.
- Generate session tokens with `crypto/rand` and store only their SHA-256 hash.
- Session cookies must remain `HttpOnly`, `SameSite=Strict`, scoped to `/`, and expired on logout.
- Preserve the current 30-day session expiration unless explicitly changed together with documentation and tests.
- Keep login and setup failures rate-limited by both client IP and normalized account identity, and retain at most 10 active sessions.
- Keep authenticated API routes behind `requireAuth`.
- Do not include password hashes, raw session tokens, notification credentials, or authentication secrets in operational logs.

## 9. HTTP API and Validation
Current routes are registered in `main.go` with Go's method-aware `http.ServeMux` patterns.

Public routes:
- `GET /`
- `GET /assets/app.css`
- `GET /assets/app.js`
- `POST /auth/setup`
- `POST /auth/login`
- `GET /health`

Authenticated routes:
- `POST /auth/logout`
- `GET /api/state`
- Subscription create, update, skip, and cancel routes under `/api/subscriptions`.
- Settings, payment method, currency, notification test, and data backup routes under `/api`.

API rules:
- Return JSON errors in the existing `{"error":"..."}` shape.
- Keep validation errors distinct from internal server failures.
- Do not return raw database, network, or secret-bearing errors to clients.
- Use `decode` for ordinary JSON bodies so unknown fields are rejected and request bodies remain limited to 1 MiB.
- Keep backup import limited to 20 MiB and reject unknown JSON fields.
- Validate path IDs as positive integers.
- Use appropriate status codes, including `201` for creates, `400` for invalid input, `401` for unauthenticated API access, `403` for forbidden setup or built-in mutations, and `404` for missing records.
- Keep `/health` lightweight and independent of authenticated dashboard rendering.

## 10. Backup and Restore
- Backup format version is currently `1`; reject unsupported versions.
- Export must exclude administrator password hashes and login sessions.
- Export currently includes notification integration settings, including credentials. Keep the UI warning accurate if this changes.
- Import replaces application data but must preserve the administrator password and active login sessions.
- Perform import replacement and restoration in one transaction so a failed import does not leave partial data.
- Maintain referential integrity and stable IDs across payment methods, currencies, subscriptions, occurrences, price history, and activities.
- Validate imported custom currency codes and required payment method data.
- Any schema change affecting user data must update both `dataBackup`, `exportData`, `importData`, and round-trip tests.

## 11. Notifications
- Supported delivery channels are Discord webhooks and Telegram bots.
- Accept only official HTTPS Discord webhook URLs, do not follow provider redirects, and validate Telegram credentials before outbound requests.
- Never log Discord webhook URLs, Telegram bot tokens, chat IDs, or complete outbound request URLs containing tokens.
- Use bounded HTTP clients; preserve the current outbound timeout unless deliberately changed.
- Read and discard only a bounded portion of provider responses before closing bodies.
- Keep notification delivery deduplicated through `notification_deliveries`.
- When a deduplicated delivery fails, remove its delivery key so a later retry remains possible.
- Upcoming-payment notifications respect the configured 0-to-30-day lead time.
- Monthly summaries remain grouped by currency and must not perform exchange-rate conversion.
- Test notifications may use unsaved form values, but those secrets must not appear in errors or logs.
- Notification failures must not crash the server or break normal subscription mutations.

## 12. Frontend and UI
- Preserve the current dark dashboard, responsive layout, and Korean copy style.
- Keep setup/login in `web/auth.html` and the authenticated shell in `web/index.html`.
- Keep dashboard behavior in `web/app.js` and styling in `web/app.css`.
- Escape all untrusted strings before inserting them through `innerHTML`; use `textContent` when markup is unnecessary.
- Copy or render user-controlled service names, categories, memos, payment methods, currencies, and settings safely.
- Preserve keyboard focus behavior, accessible labels, modal semantics, toast live region, and mobile layout.
- Destructive actions such as cancellation, data import, and deletion of custom items should retain clear confirmation UX.
- JSON import must remain an actual labeled file input and must not rely on a programmatic picker click.
- If CSS or JavaScript behavior changes, update asset version query strings in the HTML when needed to avoid stale browser caches.
- Do not move large HTML fragments into Go source; keep page structure in the embedded web assets.

## 13. Docker and Runtime Configuration
Environment variables:

| Name | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `DB_PATH` | `./data/submanager.db` locally, `/data/submanager.db` in the image | SQLite path |
| `TZ` | `Asia/Seoul` | Billing and notification timezone |

Runtime rules:
- Create the parent directory of `DB_PATH` before opening SQLite.
- Invalid `TZ` values currently fall back to a fixed UTC+09:00 `Asia/Seoul` zone; preserve or deliberately document any behavior change.
- Keep graceful shutdown for `SIGINT` and `SIGTERM`, including notification worker cancellation.
- Docker builds require CGO because `go-sqlite3` is used.
- Keep the final image running as the unprivileged `submanager` user.
- Persist `/data` through the named `submanager-data` volume.
- Keep the container health check pointed at `/health`.
- `compose.yaml` is the canonical local Docker configuration; keep environment variables under `environment`.

## 14. Security and Privacy
- Never log or expose administrator passwords, password hashes, raw session tokens, Discord webhook URLs, Telegram bot tokens, or private backup contents.
- Keep SQL parameterized and validate all client-supplied identifiers and enum-like values.
- Preserve request size limits for JSON and backup imports.
- Preserve these response headers unless a stronger compatible policy replaces them:
  - `X-Content-Type-Options: nosniff`
  - `X-Frame-Options: DENY`
  - `Referrer-Policy: same-origin`
- Keep panic recovery from exposing stack traces or internal error details to clients.
- Authentication and setup changes should include focused tests for privilege boundaries and secret storage.
- Treat exported backups as sensitive because they include subscription data and notification integration credentials.

## 15. Testing and Validation
The SQLite driver requires CGO and a C compiler.

Run before handing off Go changes:
```bash
gofmt -w main.go main_test.go
go test ./...
go vet ./...
go build ./...
```

For Docker or runtime changes, also run when available:
```bash
docker compose config
docker compose build
docker compose up -d
docker compose ps
```

Test expectations:
- Use temporary SQLite databases through `t.TempDir()`.
- Keep seed idempotency covered.
- Keep first-account and password-hashing behavior covered.
- Test billing date boundaries, monthly/yearly anchors, trials, skips, cancellations, and historical price/currency totals.
- Test authenticated and unauthenticated API behavior when changing routes.
- Test backup export/import round trips and verify authentication data is excluded.
- Test secret-safe notification failures and delivery deduplication when notification code changes.
- Add focused frontend source assertions for UX regressions that cannot be exercised without a browser harness.

## 16. Change Workflow
- Before editing, read this file, `README.md`, and `WORK.md` when it exists, then inspect the affected code and tests.
- Preserve unrelated user changes in a dirty worktree.
- Keep changes scoped and update tests with behavior changes.
- Update `README.md` when environment variables, setup, Docker usage, storage, backup behavior, or user-visible operation changes.
- When modifying this file, include it in the relevant commit rather than leaving it uncommitted.
- When committing, exclude `WORK.md` unless the user explicitly asks to include it.
- When committing, add a `Co-authored-by: Codex <codex@openai.com>` trailer unless the user explicitly asks not to.
- When modifying Markdown files, include the filename in the commit subject or body so the changed documentation is clear.
- Use short conventional English commit messages when commits are requested, for example:
  - `feat: add billing reminder options`
  - `fix: preserve historical currency totals`
  - `docs: update AGENTS.md backup rules`
- Do not commit generated SQLite files, WAL/SHM files, backup exports, logs, local `.env` files, or compiled binaries.
- If Git metadata is absent, do not initialize a repository or create commits unless explicitly requested.

### Work Log Format
- Record completed commit-and-push work in `WORK.md` using date headings in the form `# YYYY-MM-DD`.
- If the current date already has a heading, append new entries to that block instead of creating a duplicate heading.
- Every entry below a date heading must start with `- `, and each dated block must contain at least two entry lines.
- Use the first entry line to summarize what changed and the second to describe the concrete files, code, or behavior affected.
- When a commit and push occurred, include the commit ID and commit message in the same dated block.
- If a follow-up fix is required because of an agent mistake, record what was wrong and how it was corrected in the same format.

## 17. Common Mistakes to Avoid
- Replacing historical totals with calculations based only on current prices.
- Combining different currencies into one total without an explicit exchange-rate feature.
- Using floating-point values for subscription amounts.
- Physically deleting used custom currencies or payment methods and breaking history.
- Allowing repeated setup to replace the administrator.
- Storing or logging raw session tokens or notification secrets.
- Adding a frontend build dependency for a change that fits the existing Vanilla JavaScript UI.
- Forgetting to update backup export and import after a schema change.
- Making backup import non-atomic.
- Removing notification deduplication or marking failed deliveries as permanently sent.
- Breaking the embedded asset paths or forgetting cache-version updates after frontend changes.
- Building with `CGO_ENABLED=0` while the project still uses `go-sqlite3`.

## 18. Final Guidance
Keep Submanager:
- lightweight
- Korean-first
- self-hosted
- single-admin
- SQLite-backed
- dependency-minimal
- Docker-friendly

When editing, preserve:
- historical billing accuracy
- per-currency accounting
- transactionally consistent data
- secure password and session handling
- atomic backup restoration
- secret-safe notification behavior
- embedded, accessible, responsive web UI
