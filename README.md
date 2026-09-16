# CredFlow API

| Overview | [API reference](API.md) | [Architecture](ARCHITECTURE.md) |
|:---:|:---:|:---:|

Backend for CredFlow, a debt and collections tracker for African SMEs. A
business records its customers, what each one owes, and the payments that come
in against those debts — and gets a running picture of what is outstanding,
what is overdue, and how collections are trending.

Go and Postgres, no framework beyond a router. Every request belongs to one
business, and every query is scoped to it.

## Stack

| | |
|---|---|
| Language | Go 1.25 |
| Router | chi v5 |
| Database | PostgreSQL 16, pgx driver |
| Migrations | golang-migrate, applied on startup |
| Auth | JWT access tokens, rotating refresh tokens in an httpOnly cookie |

## Getting started

```bash
# 1. Start Postgres
make db-up

# 2. Configure
cp .env.example .env
#    Set DATABASE_URL, and generate a secret:
#    openssl rand -base64 64

# 3. Run — migrations apply automatically
make run
```

```bash
curl localhost:8080/health
# {"data":{"status":"ok"},"meta":null,"error":null}
```

For a browser client, set `ALLOWED_ORIGINS` to its exact origin and
`COOKIE_SECURE=false` for local http. `.env.example` documents every variable.

## Commands

```bash
make run               # start the server
make build             # compile to ./bin/credflow
make test              # unit tests — fast, no database
make test-integration  # integration tests — needs Postgres
make test-all          # everything
make lint              # go vet
make db-up / db-down / db-reset
```

Integration tests **skip** rather than fail when the database is unreachable, so
check the output for `SKIP` before trusting a green run.

## Layout

```
cmd/server/          config, wiring, routes, graceful shutdown
internal/
  auth/              register, login, JWT, password hashing, sessions
  users/             team management — invite, change role, remove
  audit/             append-only trail of destructive and financial actions
  businesses/        business profile and onboarding
  customers/         customer records
  notes/             customer follow-up history
  debts/             debts, mark-paid, derived balances
  payments/          recording, idempotency, correction, void
  analytics/         dashboard and analytics aggregates
  search/            cross-entity lookup
  middleware/        auth, roles, CORS, rate limits, request guards
pkg/                 database, mailer, rate limiter, JSON envelope
migrations/          versioned .up.sql / .down.sql pairs
test/integration/    end-to-end tests (build tag: integration)
```

Each feature package has the same four layers — `handler` decodes and writes
HTTP, `service` holds the rules, `repository` owns the SQL, `models` the types —
and dependencies point one way only.

## Documentation

Use the strip at the top of any page to move between them.

| | |
|---|---|
| [**API reference**](API.md) | The contract — envelope, auth, roles, every route, currency rules, rate limits |
| [**Architecture**](ARCHITECTURE.md) | How the system is built and why — layering, tenancy, auth, money integrity, the audit trail |

Longer design notes — the endpoint catalogue, project guide, code walkthrough
and per-phase specs — live in `docs/`, which `.gitignore` keeps local to the
author's machine rather than in the repository.

## License

All rights reserved.

---

| Overview | [API reference](API.md) | [Architecture](ARCHITECTURE.md) |
|:---:|:---:|:---:|
