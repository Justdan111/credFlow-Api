# Architecture

| [Overview](README.md) | [API reference](API.md) | Architecture |
|:---:|:---:|:---:|

How the CredFlow API is put together, and why it is put together that way.
[README.md](README.md) covers running it; [API.md](API.md) is the route
reference. This document is about the reasoning.

---

## What the system is

A business signs up, records its customers, records what each customer owes,
and records payments as they come in. The API keeps three things true:

1. **A debt's status always matches the payments behind it.** Nothing else in
   the product works if the ledger disagrees with itself.
2. **A business can only ever see its own rows.** Not "hidden from" — unreachable.
3. **Destructive and financial actions are attributable.** Who voided that
   payment is a question that gets asked after the fact, not before.

Everything below follows from those three.

---

## Layers

Each feature is a package under `internal/` with the same four files.
Dependencies point one way: a handler never touches the database, a repository
never knows about HTTP.

```
HTTP request
    │
    ▼
 handler.go      decode input, map errors to status codes, write the envelope
    │
    ▼
 service.go      validation, defaults, business rules, authorization decisions
    │
    ▼
 repository.go   SQL, always scoped by business_id
    │
    ▼
 PostgreSQL
```

`models.go` holds the domain types and request shapes the three share.

The split earns its keep at the seams. Validation lives in the service, so the
onboarding flow can create a customer and a debt through `customers.Service`
and `debts.Service` inside its own transaction and get exactly the same rules
the HTTP endpoints apply — no second code path that forgets to default
`risk_level` and trips a database CHECK.

### Interfaces are declared by the consumer

`internal/users` needs to email an invitation. Rather than importing the auth
service, it declares:

```go
type Inviter interface {
    SendInvitation(ctx context.Context, userID, email, name, invitedByName string) error
}
```

`auth.Service` happens to satisfy it. `internal/audit` does the same with
`ActorLookup`, which `users.Repository` satisfies. `main.go` wires them together.

This is not ceremony. `internal/audit` imports `internal/auth` for the request
context helpers, so an audit→users→auth→audit chain would be an import cycle.
Declaring the narrow interface at the point of use breaks it, and it means a
test can substitute a recorder without standing up a mail server.

---

## Multi-tenancy

Every authenticated request carries a `business_id` resolved from the JWT.
Repository queries filter on it unconditionally:

```sql
WHERE business_id = $1 AND id = $2 AND deleted_at IS NULL
```

A row belonging to another tenant is not filtered out of a result set — it never
matches. The practical consequence is that a valid id from another business
returns **404, not 403**: confirming the row exists elsewhere would itself leak.

`test/integration/tenant_isolation_test.go` walks every cross-tenant path and
asserts each one fails closed. It is the single most important test in the repo.

---

## The request path

```
CORS → security headers → body limit → request id → logger
     → recover → timeout → require JSON
     → [route group] require auth → rate limit → validate UUID params
     → [route] require role → handler
```

CORS runs first so even a rejected request carries the headers a browser needs
to read the rejection. The body limit runs before anything parses, so an
oversized payload cannot exhaust memory before a handler exists to reject it.

Three of these close specific holes found by probing the running server:

- **`RequireJSON`** — a write declaring `text/plain` used to be accepted.
  Bearer auth already rules out browser form CSRF, but an endpoint that
  silently takes `text/plain` is what becomes exploitable the day somebody adds
  cookie auth. A form can post `text/plain`; it can never post
  `application/json`.
- **`ValidateUUIDParams`** — `/api/customers/not-a-uuid` used to reach Postgres,
  which refused the cast, and the client's mistake surfaced as a **500**.
  It mounts on a nested `r.Route("/{customerId}", …)`, not the parent group:
  chi fills URL parameters only once the pattern carrying them has matched, so
  on the parent it reads an empty string and validates nothing. A test pins that
  behaviour so a future chi release cannot silently disable the check.
- **`Recoverer` / `NotFound` / `MethodNotAllowed`** — chi's defaults answer with
  plain text and empty bodies, which breaks a client that parses every response
  as JSON. Ours answer in the envelope, and a panic quotes the request id so a
  bug report can be matched to the logged stack. The panic value itself never
  reaches the client: it may name a table, a file path, or another tenant's data.

---

## The response envelope

Every response, success or failure, is the same shape:

```jsonc
{ "data": {}, "meta": null, "error": null }
```

One shape means a client writes one parser. `meta` carries `{page, pageSize,
total}` on list endpoints. The only deliberate exception is the analytics CSV
export, which streams a file.

---

## Authentication

**Short access token, long refresh token.** The access JWT lives 15 minutes and
travels in the `Authorization` header. The refresh token lives 30 days in an
httpOnly, `SameSite=Strict` cookie scoped to `/api/auth`, so JavaScript can
never read it and the browser never sends it anywhere else.

**Rotation with families.** Each login opens a *family*; each refresh retires the
presented token and issues a successor in the same family. Sessions are therefore
per-device — signing out on a laptop leaves a phone signed in.

**Reuse detection.** If a retired token is presented again, that is either a
stolen token being replayed or a client that lost a response. The two are
indistinguishable, so the whole family is revoked and that device signs in
again. Other devices are untouched. A family also cannot outlive
`REFRESH_ABSOLUTE_TTL` no matter how often it rotates.

**Only digests are stored**, hashed with SHA-256 rather than bcrypt. A 256-bit
random token has no dictionary to attack, so bcrypt's slowness buys nothing —
and its per-hash salt would leave no stable value to index, turning every
lookup into a full-table comparison.

**Account recovery** always answers `202`, whether or not the address exists.
Any difference in status, body or timing would make it an enumeration oracle.
Redeeming a reset token revokes every session; changing a password from inside
revokes every *other* one and requires the current password, because a stolen
access token must not be enough to take an account over permanently.

### Roles

`member` < `admin` < `owner`. Reads and creates are open to any member;
destructive and financial actions require elevation. Three rules stop privilege
being escalated sideways:

- Nobody may grant a role above their own, or an `admin` mints an `owner` and
  inherits everything they were not given.
- The last `owner` cannot be demoted or removed — a business with no owner is
  repairable only with direct database access.
- Nobody may remove their own account, which is both a foot-gun and how a stolen
  token would cover its tracks.

### Invitations reuse password reset

`POST /api/users` creates the row with a random 32-byte password nobody holds,
then issues an ordinary reset token and mails the link. The invitee sets their
own credential through the flow that already exists.

The alternative — an administrator choosing somebody else's password — puts that
password in a chat message forever. And a bespoke "invitation token" would be a
second credential type with its own chance of getting expiry, single-use or
revocation wrong. One token type, one redemption path.

Removing a member soft-deletes them and revokes their refresh tokens in one
transaction. Their access token survives until it expires, at most `JWT_TTL`,
because a stateless token cannot be recalled — which is precisely why that TTL
is 15 minutes and not 24 hours.

---

## Money

**Stored as `NUMERIC(14,2)`**, and every total, average and comparison is
computed in SQL, where that arithmetic is exact. Floats would drift.

**One currency per business**, locked once any debt or payment exists. Changing
it converts nothing, so every stored amount would silently be reinterpreted. The
API returns `currencyLocked` so a client can disable the selector rather than
offer a change that comes back as 409.

**Idempotency on payment creation.** A partial unique index on
`(business_id, idempotency_key)` makes a replay return `200` with the original
row instead of recording the money twice. A double-clicked button or a retried
request after a timeout is safe.

**Status is derived, never asserted.** Recording, correcting or voiding a payment
recomputes the parent debt inside the same transaction:

```
admin-closed        → paid      (an administrative close survives a void)
paid ≥ amount       → paid
paid > 0            → partial
otherwise           → pending
```

An earlier bug guarded this on `status = 'paid'` rather than on
`manually_marked_paid`, so voiding the payment that settled a debt left it
pinned at "paid" while the money behind it vanished. The regression test for
that lives in `test/integration/payments_regression_test.go`.

A correction deliberately **cannot** move a payment to a different customer or
debt: that would change two balances under one opaque edit. Void it and record it
again, which leaves both actions in the audit trail.

---

## The audit trail

`audit_logs` is append-only by construction — no `updated_at`, no trigger, and
no update or delete path in the repository. A record the application can edit is
not evidence of anything, and the cheapest way to guarantee that is to never
write the code.

The actor's email and name are **denormalised onto each row**, so an entry still
names somebody after that user is removed, and does not change if they later
rename themselves.

Reads are never recorded. A trail that logs every `GET` is noise nobody reads,
and it would turn the audit screen into a record of when colleagues were at
their desks.

**Known limitation.** Entries are written after the action commits, and a
failure to write one is logged rather than returned. The action already
happened, so failing the response would report a lie to the caller. A strictly
transactional trail means threading the transaction through every mutating
service — worth doing when a compliance requirement demands it, and stated here
rather than glossed over.

---

## Rate limiting

A sliding-window log, not a fixed-window counter: a fixed window lets a caller
fire the full allowance at 0:59 and again at 1:01, doubling the real rate across
the boundary.

The keying is the interesting decision. **Carrier-grade NAT is widespread on
African mobile networks**, so many unrelated subscribers share one public
address. A purely IP-keyed login limit could lock out an entire operator pool
because one person mistyped a password. So:

| Scope | Limit | Key | Why |
|---|---|---|---|
| Login | 5/min | IP **+** email | Two subscribers behind one NAT get separate buckets |
| Register | 10/hour | IP | No account exists yet to key on |
| Forgot password | 3/hour | email | One person cannot be mail-bombed |
| Forgot password | 20/hour | IP | Loose ceiling against bulk abuse |
| Authenticated reads | 300/min | user | Blast radius on a stolen token |
| Authenticated writes | 60/min | user | One colleague cannot throttle the office |
| Invitations | 20/hour | business | Protects the sending domain's reputation |

Nothing hard-locks; every window rolls, so a targeted user can always recover.

**`TRUST_PROXY_HEADERS` defaults to false.** chi's `RealIP` overwrites
`RemoteAddr` from `X-Forwarded-For` with no validation, so mounting it without a
proxy that strips those headers lets any caller forge a new address per request
and evaporate every IP-based limit. Enable it only when a load balancer that
overwrites them sits in front.

**Counters live in this process.** With N replicas the effective limit is roughly
N times what is configured, and a restart clears them. The `Limiter` interface
exists so a Redis implementation drops in without touching callers. Documented
rather than hidden — and still far better than no limit on a single instance.

---

## Data model notes

- **Soft deletes everywhere** (`deleted_at`), with partial indexes excluding
  deleted rows so the hot path stays lean. Financial history must not vanish
  because somebody tidied a customer list.
- **Partial unique indexes** rather than table constraints, so a soft-deleted
  row frees its email or idempotency key for reuse. Adding `deleted_at` to
  `users` meant replacing the table-level `UNIQUE(email)` for exactly this
  reason — otherwise a removed teammate could never be re-invited.
- **Sort fields are an allowlist**, mapped from an API name to a column. User
  input never reaches `ORDER BY`; an unknown value is a 400, not an injection.
- **Search escapes `%` and `_`** before they reach `LIKE`. Not an injection risk
  — the term is always a bind parameter — but a correctness one: searching
  "50%" must find a percent sign, not every row beginning "50".
- `customers.risk_level` is mutable with no history, so a daily background job
  snapshots the distribution into `customer_risk_snapshots`. Last month's mix is
  otherwise unrecoverable.

Migrations are versioned `.up.sql`/`.down.sql` pairs applied automatically on
startup.

---

## Background jobs

Three goroutines share the application context, so they stop with the server
rather than being killed mid-query:

- refresh and reset token cleanup, daily (expired rows are kept for a grace
  period so reuse detection still recognises a replay);
- risk distribution snapshots, daily and once at startup;
- rate-limiter bucket eviction, every five minutes.

---

## Testing

**Unit tests** are pure: validation, role rules, sort parsing, key derivation,
UUID and content-type matching, the sliding window under `-race`. No database.

**Integration tests** run the real router against real Postgres — 200+ cases
covering every route, each privilege rule, tenant isolation, and the security
behaviours (a spoofed `X-Forwarded-For` must not reset a limit; a second user
behind one address must be unaffected by the first).

> Integration tests **skip** rather than fail when the database is unreachable.
> A green `make test-all` on a machine with no Docker means *nothing ran* —
> check the output for `SKIP` before trusting it.

---

## Deliberate omissions

Not built, because each needs a product decision or an external service, and
inventing an endpoint nobody has agreed on is worse than leaving the gap visible:
2FA, notifications and reminders, uploads, saved reports, payment receipts, debt
rescheduling and waivers, and an aggregate settings resource.

`ConsoleMailer` logs reset and invitation links instead of sending them, so the
whole recovery flow is exercisable locally with no vendor. A real provider is one
type satisfying the same interface, swapped in at wiring time.

---

| [Overview](README.md) | [API reference](API.md) | Architecture |
|:---:|:---:|:---:|
