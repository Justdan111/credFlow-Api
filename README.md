# CredFlow API

A multi-tenant REST API for businesses to track money owed to them — customers, debts,
and payments. Every business sees only its own data; tenant isolation is enforced at the
query layer and covered by dedicated tests.

Built in Go with `chi`, PostgreSQL, and JWT auth.

---

## Table of contents

- [Stack](#stack)
- [Getting started](#getting-started)
- [Configuration](#configuration)
- [Developer commands](#developer-commands)
- [Architecture](#architecture)
- [Response envelope](#response-envelope)
- [Authentication & roles](#authentication--roles)
- [API reference](#api-reference)
- [Testing](#testing)
- [Project layout](#project-layout)

---

## Stack

| Concern | Choice |
|---|---|
| Language | Go 1.25 |
| HTTP router | `go-chi/chi/v5` |
| Database | PostgreSQL 16 |
| DB driver | `jackc/pgx/v5` (native pool) |
| Migrations | `golang-migrate/migrate/v4` |
| Auth | JWT (`golang-jwt/jwt/v5`) + bcrypt |

---

## Getting started

**Prerequisites:** Go 1.25+, Docker (for Postgres), `psql`.

```bash
# 1. Start Postgres
make db-up

# 2. Configure
cp .env.example .env
# then set DATABASE_URL and generate a secret:
#   openssl rand -base64 64

# 3. Run — migrations are applied automatically on startup
make run
```

The server listens on `:8080` by default.

```bash
curl localhost:8080/health
# {"data":{"status":"ok"},"meta":null,"error":null}
```

---

## Configuration

All configuration comes from the environment; `.env` is loaded at startup if present.

| Variable | Required | Default | Purpose |
|---|:---:|---|---|
| `DATABASE_URL` | yes | — | Postgres connection URI |
| `JWT_SECRET` | yes | — | HMAC signing key; generate 64 random bytes |
| `PORT` | no | `8080` | HTTP listen port |
| `JWT_TTL` | no | `15m` | Access-token lifetime (Go duration) |
| `REFRESH_TTL` | no | `720h` | Refresh-token lifetime (30 days) |
| `REFRESH_ABSOLUTE_TTL` | no | `2160h` | Hard ceiling on a session (90 days) |
| `ALLOWED_ORIGINS` | no | `http://localhost:5173` | Comma-separated exact origins for CORS |
| `COOKIE_SECURE` | no | `true` | Send the refresh cookie over HTTPS only |
| `APP_BASE_URL` | no | `http://localhost:5173` | Frontend origin used to build reset links |
| `PASSWORD_RESET_TTL` | no | `1h` | How long a reset link stays valid |
| `TRUST_PROXY_HEADERS` | no | `false` | Honour `X-Forwarded-For` — only behind a proxy that overwrites it |
| `MAX_REQUEST_BODY_BYTES` | no | `1048576` | Request body cap (1 MiB) |
| `ENABLE_HSTS` | no | `true` | Send HSTS on TLS requests |

Currency and the monthly collection target are stored per business, not configured
here — see [Currency](#currency).
| `DB_MAX_CONNS` | no | `10` | Connection-pool ceiling |
| `DB_MIN_CONNS` | no | `2` | Connection-pool floor |
| `APP_ENV` | no | `development` | Environment label |

`DATABASE_URL` and `JWT_SECRET` are fatal if missing — the server refuses to start
rather than run misconfigured.

---

## Developer commands

```
make run               Start the server
make build             Compile to ./bin/credflow
make test              Unit tests only (fast, no DB)
make test-integration  Integration tests (needs Postgres)
make test-all          Every test
make lint              go vet ./...
make tidy              go mod tidy

make db-up             Start the Postgres container
make db-down           Stop the Postgres container
make db-reset          Drop + recreate the credflow_test database
```

---

## Architecture

Each feature is a self-contained package under `internal/` with the same four layers.
Dependencies point in one direction only — a handler never touches the database, and a
repository never knows about HTTP.

```
HTTP request
    │
    ▼
 handler.go      decode + validate input, write the JSON envelope
    │
    ▼
 service.go      business rules, defaults, authorization decisions
    │
    ▼
 repository.go   SQL, always scoped by business_id
    │
    ▼
 PostgreSQL
```

`models.go` in each package holds the domain types and request/response shapes shared
across the three layers.

**Multi-tenancy.** Every authenticated request carries a `business_id` resolved from the
JWT. Repository queries filter on it unconditionally, so a row belonging to another
tenant is not merely hidden — it is unreachable. `test/integration/tenant_isolation_test.go`
asserts that no cross-tenant access path returns a success status.

---

## Response envelope

Every response — success or failure — uses the same shape, so clients parse one format.

```jsonc
{
  "data":  { },      // object, array, or {} on error
  "meta":  null,     // pagination on list endpoints, else null
  "error": null      // { "message": "...", "code": "..." } on failure
}
```

List endpoints populate `meta`:

```json
{ "page": 1, "pageSize": 20, "total": 137 }
```

---

## Authentication & roles

Register or log in to receive a **short-lived access token** (15 minutes) in the response
body, plus a **long-lived refresh token** (30 days) in an httpOnly cookie. Send the access
token on every subsequent request:

```
Authorization: Bearer <accessToken>
```

When it expires, call `POST /api/auth/refresh` — the browser sends the cookie
automatically — to get a new one. `POST /api/auth/logout` ends the session.

### Session model

The refresh cookie is `HttpOnly`, `Secure`, `SameSite=Strict`, scoped to `Path=/api/auth`.
JavaScript can never read it, so an XSS bug cannot steal a long-lived credential, and the
browser never transmits it to any other endpoint.

Refresh tokens **rotate**: each use retires the presented token and issues a successor.
Every login opens an independent *family*, so sessions are per-device — logging out on a
laptop leaves a phone signed in.

If a **retired token is presented again**, that is either a stolen token being replayed or
a client that lost a response. The two are indistinguishable, so the entire family is
revoked and that device must log in again. Other devices are unaffected. A session also
cannot outlive `REFRESH_ABSOLUTE_TTL` no matter how often it is refreshed.

Only the digest of a token is ever stored. It is hashed with SHA-256 rather than bcrypt:
a 256-bit random token has no dictionary to attack, so bcrypt's deliberate slowness would
buy nothing, and its random salt would make the digest impossible to index or look up.

### Account recovery

`POST /api/auth/forgot-password` **always** returns `202` with the same body, whether or
not the address is registered. Any difference in status, body or timing would turn it
into an account-enumeration oracle.

The link carries a 256-bit token; only its SHA-256 digest is stored, so a leaked database
contains nothing redeemable. Tokens are **single-use** and expire after
`PASSWORD_RESET_TTL` (default one hour) — a link left in an inbox must not stay valid.
Requesting a second link invalidates the first.

Redeeming a token **revokes every session** for that user: whoever forced the reset must
not keep a live one. `POST /api/auth/change-password` revokes every *other* session and
leaves the caller signed in, and requires the current password even though the caller is
already authenticated — a stolen access token must not be enough to take an account over.

`PATCH /api/auth/me` deliberately cannot change the email address: a login identifier
needs its own verification flow.

### Email delivery

Password recovery sends mail through a small `Mailer` interface. The default
implementation logs the link to the application log, so the whole flow works in
development with no external service. A real provider is one type satisfying the same
interface, wired in `cmd/server/main.go`.

`APP_BASE_URL` (default `http://localhost:5173`) builds the link the frontend consumes.

### CORS

Browser clients must be listed in `ALLOWED_ORIGINS`. The API echoes the exact matching
origin and sets `Access-Control-Allow-Credentials: true`; a wildcard is never used, since
browsers reject it on credentialed requests. `POST /refresh` and `POST /logout`
additionally reject requests declaring a non-allowlisted `Origin`.

Three roles exist, in ascending privilege: `member`, `admin`, `owner`. Read and create
operations are open to any authenticated user; destructive and financial actions require
elevation.

| Action | Minimum role |
|---|---|
| Read anything, create customers/debts/payments | `member` |
| Delete a customer, update or delete a debt | `admin` |
| Delete (void) a payment | `owner` |

---

## API reference

`{id}` path parameters are UUIDs. All `/api/*` routes except `register` and `login`
require a bearer token.

### Health

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Liveness — process is up |
| `GET` | `/health/db` | Readiness — database reachable |

### Auth

| Method | Path | Role | Description |
|---|---|---|---|
| `POST` | `/api/auth/register` | — | Create a business and its owner; opens a session |
| `POST` | `/api/auth/login` | — | Exchange credentials for a session |
| `POST` | `/api/auth/refresh` | cookie | Rotate the refresh token, get a new access token |
| `POST` | `/api/auth/logout` | cookie | Revoke the current session |
| `POST` | `/api/auth/forgot-password` | — | Request a reset link (always `202`) |
| `POST` | `/api/auth/reset-password` | — | Redeem a reset token |
| `GET` | `/api/auth/me` | any | Current user and business |
| `PATCH` | `/api/auth/me` | any | Update name and phone |
| `POST` | `/api/auth/change-password` | any | Change password; evicts other sessions |
| `GET` | `/api/auth/sessions` | any | Active logins, with the current one flagged |
| `DELETE` | `/api/auth/sessions/{sessionId}` | any | Revoke one session |

### Business & onboarding

| Method | Path | Role | Description |
|---|---|---|---|
| `GET` | `/api/businesses/current` | any | The active business profile |
| `PATCH` | `/api/businesses/current` | admin | Update name, industry, size, currency, collection target |
| `GET` | `/api/onboarding/status` | any | Completion state and the step to resume at |
| `POST` | `/api/onboarding/complete` | any | Persist the whole onboarding flow in one transaction |

`PATCH` is a partial update: an omitted key is left unchanged, while an explicit
`"monthlyCollectionTarget": null` clears the target.

`POST /api/onboarding/complete` takes the three-step payload — business profile, an
optional first customer, an optional first debt — and applies all of it atomically. A
debt without a customer is a `400`, since there would be nobody to owe it. Calling it
again returns `409` rather than creating a second "first" customer on a double submit.

`GET /api/onboarding/status` derives each step from real records rather than a stored
counter, so it cannot drift out of sync with what the business actually has.

### Customers

| Method | Path | Role | Description |
|---|---|---|---|
| `GET` | `/api/customers` | any | List — paginated, sortable, searchable |
| `POST` | `/api/customers` | any | Create |
| `GET` | `/api/customers/{customerId}` | any | Fetch one |
| `PATCH` | `/api/customers/{customerId}` | any | Partial update |
| `DELETE` | `/api/customers/{customerId}` | admin | Soft-delete |
| `GET` | `/api/customers/{customerId}/debts` | any | Debts for a customer |
| `GET` | `/api/customers/{customerId}/payments` | any | Payments for a customer |

### Debts

| Method | Path | Role | Description |
|---|---|---|---|
| `GET` | `/api/debts` | any | List — paginated, sortable, filterable |
| `POST` | `/api/debts` | any | Create |
| `GET` | `/api/debts/{debtId}` | any | Fetch one |
| `PATCH` | `/api/debts/{debtId}` | admin | Partial update |
| `DELETE` | `/api/debts/{debtId}` | admin | Soft-delete |
| `POST` | `/api/debts/{debtId}/mark-paid` | any | Settle in full |
| `POST` | `/api/debts/{debtId}/payments` | any | Record a payment against this debt |

Each debt returns `amount_paid` and `amount_remaining` derived from its payments, so the
two can never drift out of sync with the ledger.

### Dashboard

Aggregations for the dashboard landing screen. All are read-only and tenant-scoped.

| Method | Path | Role | Description |
|---|---|---|---|
| `GET` | `/api/dashboard/summary` | any | Outstanding, overdue, customers and collected, each with a month-over-month change |
| `GET` | `/api/dashboard/recent-debts` | any | Latest debts with customer name and days overdue (`limit`, max 20) |
| `GET` | `/api/dashboard/recent-payments` | any | Latest payments with customer name (`limit`, max 20) |
| `GET` | `/api/dashboard/risk-distribution` | any | Current low/medium/high customer split |
| `GET` | `/api/dashboard/collections-trend` | any | Monthly collections and closing balance (`months`, max 24) |

### Analytics

| Method | Path | Role | Description |
|---|---|---|---|
| `GET` | `/api/analytics/collection-rate` | any | Monthly collections against the business target |
| `GET` | `/api/analytics/risk-trend` | any | Risk distribution over time, from daily snapshots |
| `GET` | `/api/analytics/customer-segments` | any | Customers bucketed by lifetime debt value |
| `GET` | `/api/analytics/export` | any | CSV of the trend and rate series (`format=csv`) |

**Percentage change is `null` when the previous period was zero.** A change from zero is
undefined, so the API reports nothing rather than an invented `+100%` that would make
every new business look like it were booming.

**The risk trend depends on recorded history.** `risk_level` is a mutable field, so past
months cannot be reconstructed — a daily job records the distribution as it happens. The
response carries `meta.historyStartedAt` and `meta.monthsAvailable` so a client can tell
a short series caused by young history from one caused by having no customers. The
current month is computed live, so a business that registered since the last run sees its
position immediately.

### Payments

| Method | Path | Role | Description |
|---|---|---|---|
| `GET` | `/api/payments` | any | List — paginated, sortable, filterable |
| `POST` | `/api/payments` | any | Record a payment |
| `GET` | `/api/payments/{paymentId}` | any | Fetch one |
| `DELETE` | `/api/payments/{paymentId}` | owner | Void — rolls back the debt status |

**Idempotency.** `POST` payment endpoints accept an idempotency key. A partial unique
index on `(business_id, idempotency_key)` makes a replay return `200` with the original
row instead of double-writing. Recording or voiding a payment updates the parent debt's
status in the same transaction, so a crash mid-write cannot leave a debt disagreeing
with its payments.

### Query parameters

Every list endpoint accepts `page` (default `1`) and `pageSize` (default `20`, clamped to
a safe maximum), plus `sort`. `sort` takes an API field name, optionally prefixed with
`-` for descending; values are checked against a per-resource whitelist before reaching
SQL, so an unknown or hostile value is rejected rather than interpolated.

| Endpoint | Additional parameters |
|---|---|
| `GET /api/customers` | `search` (name/contact substring), `riskLevel` |
| `GET /api/debts` | `status`, `customerId`, `overdue=true` |
| `GET /api/payments` | `customerId`, `debtId`, `method` |

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "localhost:8080/api/debts?status=pending&overdue=true&sort=-due_date&pageSize=50"
```

---

## Currency

Each business has one currency, set on the business record and defaulting to `NGN`.
Supported values are `NGN`, `GHS`, `KES`, `ZAR` and `USD`. Every aggregate response
echoes it, so clients never hard-code a symbol.

One currency per business rather than per debt keeps every total valid without exchange
rates: summing across currencies would be meaningless, and an SME almost always invoices
in one. `monthly_collection_target` is optional and nullable — a target is a business
decision, so the API returns `null` when none is set rather than inventing one.

Money is stored as `NUMERIC(14,2)`, and every total, average and comparison is computed
in SQL where that arithmetic is exact.

### Currency is locked once money exists

Amounts carry no currency of their own — they inherit the business's. Changing the
business currency therefore **converts nothing**: a debt recorded as ₦2,500,000 would
afterwards read as ₵2,500,000.

So the currency is freely settable while the business has no debts and no payments —
which is exactly when the choice is made, during onboarding — and returns `409`
afterwards. `GET /api/businesses/current` reports `currencyLocked`, so a client can
disable the selector rather than offer a change the API will reject.

`monthlyCollectionTarget` has no such constraint and stays editable at any time.

## Rate limiting

The unauthenticated endpoints are throttled. Exceeding a limit returns `429` with
`Retry-After` in seconds.

| Endpoint | Limit | Keyed on |
|---|---|---|
| `POST /api/auth/login` | 5 / minute | IP **+** email |
| `POST /api/auth/register` | 10 / hour | IP |
| `POST /api/auth/forgot-password` | 3 / hour | email |
| `POST /api/auth/forgot-password` | 20 / hour | IP |
| `POST /api/auth/reset-password` | 10 / hour | IP |
| `POST /api/auth/refresh` | 30 / minute | IP |

**Login is keyed on IP *and* email together.** Carrier-grade NAT is widespread on African
mobile networks, so many unrelated subscribers share one public address — an IP-only
limit would let one person's failed logins lock out everybody behind the same operator.
`forgot-password` is keyed per address for the same reason, with a loose per-IP ceiling
behind it to stop bulk abuse. Nothing hard-locks: every window rolls, so a targeted user
can always recover within the hour.

Counters are held **in process**. With multiple replicas the effective limit is roughly
multiplied by the replica count, and a restart clears them. The `Limiter` interface exists
so a shared Redis backend can replace the in-memory one without touching callers.

### `TRUST_PROXY_HEADERS`

Defaults to **false**, and should stay false unless a load balancer that overwrites
`X-Forwarded-For` sits in front. When false the true TCP peer address is used and
client-supplied headers are ignored entirely — otherwise a caller could forge a different
address on every request and bypass every IP-based limit.

## Security headers

Every response carries `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
`Referrer-Policy: strict-origin-when-cross-origin` and
`Cross-Origin-Opener-Policy: same-origin`.

`Strict-Transport-Security` is sent **only over TLS**, so local http development is not
pinned to https in the browser. There is no Content-Security-Policy: this API returns
JSON and never HTML.

Request bodies are capped at `MAX_REQUEST_BODY_BYTES`; anything larger gets `413`.

## Testing

Three tiers, all runnable locally:

```bash
make test              # unit — pure logic, no database
make test-integration  # integration — full HTTP → service → repo → Postgres
make test-all          # everything
```

Integration tests are guarded by the `integration` build tag and need a running Postgres;
`make db-reset` gives them a clean `credflow_test` database. CI
(`.github/workflows/test.yml`) runs build, vet, unit, and integration tests against a
Postgres service container on every push.

---

## Project layout

```
cmd/server/          entrypoint — config, wiring, routes, graceful shutdown
internal/
  auth/              register, login, JWT, password hashing, roles
  customers/         customer CRUD
  debts/             debt CRUD, mark-paid, derived balances
  payments/          payment recording, idempotency, void
  middleware/        RequireAuth, RequireRole
  testutil/          database helpers for tests
pkg/
  database/          connection pool + migration runner
  response/          the JSON envelope
migrations/          versioned .up.sql / .down.sql pairs
test/integration/    end-to-end tests (build tag: integration)
```

---

## License

All rights reserved.
