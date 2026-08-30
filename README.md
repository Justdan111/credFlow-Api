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
| `GET` | `/api/auth/me` | any | Current user and business |

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
