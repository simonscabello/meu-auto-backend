# Meu Auto — Backend (Go)

## What this repo is

The **Go API and PostgreSQL database** behind Meu Auto, a vehicle upkeep tracker for individual car owners in Brazil.

Product truth (users, purpose, positioning, scope, open decisions) lives in **`PRODUCT.md`, which is in the sibling app repo** — `../meu-auto-app/PRODUCT.md`. It is the single source of product truth for both halves; this repo intentionally keeps no copy, so there is nothing to drift. Read it before making product decisions.

## Two separate repositories

Meu Auto is **not a monorepo**. It is split into two independent git repositories, cloned side by side:

```
meu-auto/
├── meu-auto-app/       ← separate repo — Flutter app (iOS + Android)
└── meu-auto-backend/   ← this repo — Go API + PostgreSQL
```

Consequences to respect:

- The two repos version and deploy **independently**. A shipped mobile app cannot be force-updated — **old app versions will keep calling this API**. Breaking changes need versioning or a compatibility window, not a coordinated release.
- No shared build, no shared module, no cross-repo import. Anything both sides need (API contract, shared types) must be duplicated deliberately or generated from a spec — decide which before the first endpoint is written.
- The parent `meu-auto/` folder is just a convenience directory. It is not a repo and nothing is tracked there.
- If you are working here alone, `../meu-auto-app/` may not be checked out. Do not assume you can read it.

## Stack

- **Go** — HTTP API.
- **PostgreSQL** — primary datastore.
- Client is a **Flutter** app on iOS and Android (separate repo, see above).

Chosen and recorded in **[`SPEC.md`](./SPEC.md)** — read it before adding a dependency or a directory:

- **`chi`** router, **`pgx/v5`** driver, **`sqlc`** for data access, **`golang-migrate`** for migrations (embedded in the binary).
- **No ORM, no framework, no DI container, no database mocks.**
- Layout is a **modular monolith**: `internal/platform/*` (no domain knowledge) plus one package per domain module. Modules talk through service interfaces, never through each other's repository.
- `sqlc` generates **one package per module**, not a shared one — that boundary is what stops a module reaching another's tables.

`SPEC.md` also carries the business rules (RN-01..RN-10), the entity model and the deferred decisions with the trigger that reopens each one. It is the implementation contract; this file is the orientation.

## Domain and data conventions

- Data and locale are **Brazilian**: currency BRL stored in the smallest unit (centavos) unless a deliberate decision says otherwise, distance in kilometres, timezone `America/Sao_Paulo`. Store timestamps in UTC; render in São Paulo time.
- **Brazilian vehicle domain terms stay in Portuguese** in schema and code, because translating them loses the legal meaning: `ipva`, `licenciamento`, `crlv`, `revisao`, `seguro`, `multa`, `abastecimento`. A column is `data_licenciamento`, not `licensing_date`.
- Everything else — code, identifiers, comments — is in English.
- User-facing strings returned by the API (error messages the app may display) are **pt-BR**. Machine-readable error codes are English and stable.

## Load-bearing product constraints

These come from `PRODUCT.md` and shape the schema directly:

- **Multiple vehicles per account**, with an account owning many.
- **Accounts and cloud sync** — the server is the source of truth, and the same account works across devices.
- **Receipt and document images** (receipts, CRLV, insurance policies) are stored and served. Storage strategy is undecided.
- **The service history is a defensible record.** Dates, amounts and attachments are load-bearing for resale value and for disputes with a shop — favour append-with-audit over silent mutation, and think hard before allowing hard deletes.
- **Offline operation is explicitly out of scope**, so no sync-conflict resolution is required today. The product record flags this as a real risk worth revisiting, since owners log things in parking garages and on roadsides. If it is ever reversed, it changes the API shape substantially — do not architect it away, but do not build for it unasked either.

## State of the repo

**MVP-1 complete (phases 0–5).** The foundation is in place (validated config, structured logging, typed domain errors, `apperr → HTTP`, pgx pool, embedded migrations applied on boot, `/healthz` and `/readyz`, CI), plus three domain modules:

- **identity** — `POST /v1/auth/{register,login,refresh,logout}`, `POST /v1/auth/password-reset/{request,confirm}`, `GET|PATCH|DELETE /v1/me`. argon2id passwords, HS256 access tokens with the algorithm pinned, opaque rotating refresh tokens with reuse detection, rate limiting by e-mail and by IP.
- **vehicle** — `GET|POST /v1/vehicles`, `GET|PATCH|DELETE /v1/vehicles/{id}`, `GET|POST /v1/vehicles/{id}/odometer`, `DELETE /v1/odometer/{id}`. Ownership-based authorisation, the odometer monotonicity rule, keyset pagination.
- **maintenance** — `GET|POST /v1/maintenance-items`, `GET|POST /v1/vehicles/{id}/maintenance-plans`, `PATCH|DELETE /v1/maintenance-plans/{id}`, `GET|POST /v1/vehicles/{id}/maintenance-records`, `GET|PATCH|DELETE /v1/maintenance-records/{id}`. A seeded catalogue, plans materialised automatically on vehicle creation, records with line items, and the due engine.

**`internal/maintenance/due.go` is the most important file in this repo.** It is pure — no database, no clock, no context — and it holds the rule the whole product exists to serve. Change it only with its test suite in front of you, and keep it that way: the same function serves an HTTP request today and a notification cron later, and the two must never disagree.

- **obligation** — `GET|POST /v1/vehicles/{id}/obligations`, `PATCH|DELETE /v1/obligations/{id}`, `GET|POST /v1/vehicles/{id}/seguros`, `PATCH|DELETE /v1/seguros/{id}`. IPVA and licenciamento share a table with an explicit `kind`; a seguro has its own, because it is a contract with a period rather than a dated debt.

- **insight** — `GET /v1/vehicles/{id}/{dashboard,alerts,timeline}`. The read model.

Deploy is prepared but **not yet done** — `Dockerfile`, `.dockerignore`, `railway.toml` and [`docs/DEPLOY.md`](./docs/DEPLOY.md). The production image was built and exercised locally (23.7 MB distroless; `postgresql://` normalisation, embedded tzdata, migrations on boot, SIGTERM drain, and every config guard confirmed in the real container). The Railway project itself needs the owner's account.

The biggest remaining gap is **no automated tests below the pure logic**: repositories, SQL semantics, handlers and authorisation have only ever been checked by hand. That is the thing to fix before MVP-2, which starts with abastecimento — a module that writes into `odometer_readings` and the cost totals, exactly where a silent regression would land. See the roadmap in `SPEC.md` section 10.

**`internal/insight` is the only module allowed to depend on other modules.** The arrow is one-way and read-only: it calls the owning module's service and never re-derives a status. If you find yourself computing "overdue" inside insight, stop — the rule belongs to the module that owns the data, or the screen will drift from the domain behind it.

**Local-dev trap:** `TRUNCATE users CASCADE` cascades to *tables*, not rows, and wipes the global `maintenance_items` catalogue seeded by migration 000005 — which will not re-run. Clean test data with `DELETE FROM users` and `DELETE FROM vehicles` instead, or re-apply that migration file by hand.

**Date arithmetic lives in `internal/platform/civil`, nowhere else.** Civil dates — the day a service happened, the day an IPVA falls due — are `time.Time` at midnight UTC, matching a Postgres `date` column exactly. Do not reimplement `AddMonths`: Go's `AddDate` turns 31 January + 1 month into 3 March, which drifts every anniversary in the product.

Patterns now exist — `internal/identity` is the reference. Follow it rather than inventing a second way:

- A module is `handler.go` / `service.go` / `repository.go` / `dto.go` plus a generated `db/` package.
- **Handler** only translates: decode, delegate, render. It never decides anything.
- **Service** owns the rules and is the only layer that builds `apperr` values — so every client-visible message for a module sits in one file.
- **Repository** returns plain sentinel errors (`ErrUserNotFound`), never `apperr`. Multi-step writes are single methods that own their transaction, so no caller can perform half of one.
- Handlers surface errors through `httpx.Error`; nothing else writes an error body.
- Response DTOs are written by hand, never the sqlc struct — a renamed column must not silently change the API contract.
- **`api/openapi.yaml` is the contract, and it is hand-written.** Change a request or response DTO and you change it in the same edit — the Flutter app generates its Dart client from that file, and nothing automated catches the drift yet. Validate with `docker run --rm -v "$PWD:/spec" redocly/cli lint /spec/api/openapi.yaml`.

Two rules that are load-bearing rather than stylistic:

- **Nothing reaches a vehicle by id without an ownership join.** `vehicle/authorize.go` is the single choke point, and the repository offers no query that fetches a vehicle without it. A vehicle the caller cannot access is reported as **404, never 403** — "you may not see this" confirms it exists. Adding a by-id-only query is how authorisation bugs get written.
- **A module that owns user-scoped data registers an `identity.UserDataEraser`.** Account deletion cannot cascade to vehicles, because vehicles carry no `user_id`. Identity depends on the interface; wiring happens in `cmd/api/main.go`.
- **Modules talk through ports with primitive signatures**, never shared structs — a shared type would have to live in one module or the other, and either direction creates an import this architecture does not want. See `maintenance.VehiclePort` and `vehicle.PlanInitializer`. The cycle between those two is broken with a setter in `main.go`, in the open.
- **`vehicles.current_mileage_km` is maintained by a database trigger**, not by Go. Any module may insert into `odometer_readings` tagged with its own `source`, inside its own transaction; nobody has to remember to refresh the cache. Do not add a Go-side recalculation — see SPEC.md D-11.
- **Register routes as flat patterns, never nested `chi.Route`.** Both `vehicle` and `maintenance` hang endpoints off `/vehicles/{vehicleID}`, and two subrouters on overlapping prefixes make chi panic at startup.

## Things not to invent

Some of the facts `PRODUCT.md` lists as open were **decided during the backend brainstorming** and are recorded in `SPEC.md` — `PRODUCT.md` has not been updated, so trust `SPEC.md` on these:

- **Authentication:** e-mail + password (argon2id), short-lived JWT plus a rotating opaque refresh token. Social login is deferred and additive.
- **Vehicle types:** cars only in MVP-1. The `vehicle_type` column exists from the first migration so motorcycles become a catalogue seed, not a backfill.
- **Fines (multas):** not tracked in MVP-1; an `expenses` category in MVP-2.
- **Fuel logging:** not in MVP-1 at all — first item of MVP-2.
- **IPVA/licenciamento calendars:** entered by the owner. No official-data integration in the MVP.
- **Receipt and document images:** deferred out of MVP-1. Object storage is the only new infrastructure this project would need, and nothing depends on it yet — `docker-compose.yml` is Postgres and nothing else.

Still genuinely open, and still not to be invented: notification delivery, monetization and account limits.
