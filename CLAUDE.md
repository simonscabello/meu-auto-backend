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

## Working in this repo

This machine is Windows with Git Bash. Two things bite immediately:

- **Go is installed but not on the Bash `PATH`.** Prefix your shell with
  `export PATH="$PATH:/c/Program Files/Go/bin"` or nothing will build.
- **Postgres runs on host port `5433`, not 5432** — the default was already taken by
  another project on this machine. `docker-compose.yml` and `.env.example` reflect this.

```bash
export PATH="$PATH:/c/Program Files/Go/bin"

cp .env.example .env                 # once; already points at 5433
docker compose up -d                 # Postgres only, nothing else
set -a && . ./.env && set +a && go run ./cmd/api

go vet ./... && go test ./...        # full suite, seconds
gofmt -l .                           # see the CRLF note below before believing it
```

- **`gofmt -l .` lies on this machine, and it is not your change.** `core.autocrlf=true`, so
  some files sit in the working tree with CRLF and gofmt wants to rewrite every line of
  them. Git stores LF and CI checks out LF, where the whole tree is clean. To check what CI
  will actually see:

  ```bash
  rm -rf /tmp/lfcheck && mkdir -p /tmp/lfcheck
  { git ls-files '*.go'; git ls-files --others --exclude-standard '*.go'; } | while read -r f; do
    mkdir -p "/tmp/lfcheck/$(dirname "$f")"; tr -d '\r' < "$f" > "/tmp/lfcheck/$f"
  done
  (cd /tmp/lfcheck && gofmt -l .)     # THIS must print nothing
  ```

  Do not "fix" the flagged files by running `gofmt -w` on them: it rewrites their line
  endings, which turns a no-op into a whole-file diff.

- **`go test -race` does not work here** — no gcc on this machine, and the race detector
  needs cgo. CI runs it on Linux (`make test-race`). Locally, `go test ./...` is the check.
- **The integration suite needs Docker Desktop actually running**, not just installed. It
  starts its own Postgres through testcontainers, and the failure when the engine is down
  is the unhelpful `rootless Docker is not supported on Windows`. `docker info` is the
  quick check.

  ```bash
  make test-unit           # ./internal/... — no database, sub-second
  make test-integration    # ./test/... — starts a container, ~10s
  make test-golden         # regenerate test/golden, then READ the diff
  ```

  - `TEST_DATABASE_URL=postgres://meuauto:meuauto@localhost:5433/postgres?sslmode=disable`
    skips the container and uses the compose Postgres instead. It must point at a
    **server**, not at the app's database: the suite creates and drops databases on it.
  - `TESTDB_KEEP=1` leaves a failing test's database behind, named after the test, for
    inspection with psql.
  - `TEST_LOG=1` puts the request log back — it is silenced by default, because the
    authorisation matrix alone emits about ninety rejected requests.
- **sqlc runs through Docker**, so no local install is needed. Regenerate after touching
  anything in `db/queries` or `db/migrations`:

  ```bash
  MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD:/src" -w /src sqlc/sqlc:1.31.1 generate
  ```

  Then **read the generated struct.** sqlc infers nullability badly around UNIONs and
  scalar subqueries — three bugs in this repo came from it, and each would have been a
  runtime scan failure, not a compile error (SPEC.md D-09).

- **Validate the API contract** after any DTO change:

  ```bash
  MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD:/spec" redocly/cli lint /spec/api/openapi.yaml
  ```

- **Build and run the production image** the way Railway will:

  ```bash
  docker build -t meu-auto-api:local .
  ```

  See `docs/DEPLOY.md` for the full environment it expects.

## State of the repo

**MVP-1 complete (phases 0–5).** The foundation is in place (validated config, structured logging, typed domain errors, `apperr → HTTP`, pgx pool, embedded migrations applied on boot, `/healthz` and `/readyz`, CI), plus three domain modules:

- **identity** — `POST /v1/auth/{register,login,refresh,logout}`, `POST /v1/auth/password-reset/{request,confirm}`, `GET|PATCH|DELETE /v1/me`. argon2id passwords, HS256 access tokens with the algorithm pinned, opaque rotating refresh tokens with reuse detection scoped to rotation alone (SPEC.md D-15), rate limiting by e-mail and by IP.
- **vehicle** — `GET|POST /v1/vehicles`, `GET|PATCH|DELETE /v1/vehicles/{id}`, `GET|POST /v1/vehicles/{id}/odometer`, `DELETE /v1/odometer/{id}`. Ownership-based authorisation, the odometer monotonicity rule, keyset pagination.
- **maintenance** — `GET|POST /v1/maintenance-items`, `GET|POST /v1/vehicles/{id}/maintenance-plans`, `PATCH|DELETE /v1/maintenance-plans/{id}`, `GET|POST /v1/vehicles/{id}/maintenance-records`, `GET|PATCH|DELETE /v1/maintenance-records/{id}`. A seeded catalogue, plans materialised automatically on vehicle creation, records with line items, and the due engine.

- **obligation** — `GET|POST /v1/vehicles/{id}/obligations`, `PATCH|DELETE /v1/obligations/{id}`, `GET|POST /v1/vehicles/{id}/seguros`, `PATCH|DELETE /v1/seguros/{id}`. IPVA and licenciamento share a table with an explicit `kind`; a seguro has its own, because it is a contract with a period rather than a dated debt.
- **insight** — `GET /v1/vehicles/{id}/{dashboard,alerts,timeline}`. The read model.
- **catalog** — `GET /v1/vehicle-brands`, `GET /v1/vehicle-brands/{id}/models`, `GET /v1/vehicle-models/{id}/years`, `GET /v1/vehicle-model-years/{id}`. The vehicle catalogue, mirrored from the FIPE table so registration is three dropdowns instead of three free-text fields. **The only module that talks to a third party.**

**`internal/maintenance/due.go` is the most important file in this repo.** It is pure — no database, no clock, no context — and it holds the rule the whole product exists to serve. Change it only with its test suite in front of you, and keep it that way: the same function serves an HTTP request today and a notification cron later, and the two must never disagree.

Deploy is prepared but **not yet done** — `Dockerfile`, `.dockerignore`, `railway.toml` and [`docs/DEPLOY.md`](./docs/DEPLOY.md). The production image was built and exercised locally (23.7 MB distroless; `postgresql://` normalisation, embedded tzdata, migrations on boot, SIGTERM drain, and every config guard confirmed in the real container). The Railway project itself needs the owner's account.

**The integration net is in place** (`test/`), which was the gap that had to close before MVP-2 — abastecimento writes into `odometer_readings` and the cost totals, exactly where a silent regression would land.

- **`test/testdb`** gives every test its own database. One container per test binary; the migrations run once into a template, and each test clones it with `CREATE DATABASE ... TEMPLATE`. Cloning rather than truncating is deliberate — see the `TRUNCATE` trap below.
- **`test/integration`** drives the API over HTTP through the real router, built by `app.New`. There are no database mocks and no fixtures written in SQL: a fixture that inserts directly can build a state the API would have refused.
- **Three tests are guards rather than tests**, and they are the reason the rest keeps working:
  - `TestEveryProtectedRouteIsInTheMatrix` walks the router and fails if a route is neither in the authorisation table nor on the short list of public ones. **A new endpoint cannot be merged without someone stating how it is authorised.**
  - `TestRouterAndOpenAPIAgree` compares the served routes against `api/openapi.yaml`. Drift between the code and the contract the app generates its client from is now a failing build, not a discovery.
  - `TestGoldenResponses` snapshots the *shape* of every response into `test/golden` — every key and the type of every leaf, not the values, which is what makes the files stable across runs. A renamed or vanished field fails; regenerate with `make test-golden` and read the diff, because a change there is a change to what an already-installed app receives.

Under those sit the invariants: the odometer trigger and RN-01, aggregate atomicity, RN-09 materialisation, keyset pagination, refresh rotation and reuse detection, the password reset, and the LGPD erasure.

**The vehicle catalogue is the only thing here that depends on somebody else's server**, and everything about `internal/catalog` follows from one number: the provider's free tier is **500 requests a day**, shared by every user. So:

- **Postgres is asked first, always.** A miss fetches from the provider, persists, and returns; the next request for that branch — for anyone, forever — costs nothing. Nothing is imported ahead of time. In practice a full brand→model→year→detail walk costs exactly four external requests, once, and 5 ms per request after that.
- **`internal/catalog/fipe` is the only package that knows the provider exists.** It has its own vocabulary (`cars`, not `car`), its own DTOs, and its own error sentinels. Nothing above it sees a status code or a JSON field of theirs, and `external_id`/`provider`/`synced_at` never reach the wire — otherwise the app starts depending on them and changing supplier stops being a backend decision.
- **It does not retry, deliberately.** A retry doubles the cost of exactly the failures where it helps least: a quota retried is a quota hit twice. See the comment on `Client.get` before adding one.
- **A provider outage must never block a registration.** `GET /v1/vehicle-model-years/{id}` returns 200 with `fipe_price: null` when the valuation cannot be fetched — the catalogue half comes from our own tables. Only a request with genuinely nothing to serve returns `upstream_unavailable` (503). A 429 from the provider is **not** `rate_limited`: that code tells the app *this user* is going too fast, and the quota that ran out is ours.
- **Concurrency is a UNIQUE constraint and `ON CONFLICT`, nothing else.** Two users tapping the same brand at once both call the provider — one wasted request — and the second insert updates instead of duplicating. There is no lock, and there must not be: any lock here would have to be held across an HTTP call to a third party.
- **`FIPE_API_TOKEN` is a secret.** Header only, never a URL, never a log, never a response. It is optional; without it the quota is 500/day instead of 1000.
- **A vehicle keeps a snapshot, not a lookup.** `vehicles.brand/model/model_year/fuel_type/fipe_code` record what the owner confirmed; `catalog_brand_id/model_id/model_year_id` record which entry they picked. When the provider renames "PRIUS 1.8 16V 5p Aut. (Híbrido)", the vehicle does not change — a service history that rewrites itself is worth less at resale, and that history is the product's asset. All three ids are nullable and always will be: a hand-typed vehicle is a first-class vehicle.
- **The app never sends a brand or model id.** It sends `catalog_model_year_id` alone and the server derives the other two, so an inconsistent triple is not expressible.
- **No test reaches the real provider.** `newEnv` points the catalogue at `127.0.0.1:1`, which refuses instantly; a test that wants it working passes `withFipeServer(fake.URL)`. The fake serves payloads copied from the live API and counts requests — most assertions in `catalog_test.go` are about that count, not the body.

**`internal/insight` is the only module allowed to depend on other modules.** The arrow is one-way and read-only: it calls the owning module's service and never re-derives a status. If you find yourself computing "overdue" inside insight, stop — the rule belongs to the module that owns the data, or the screen will drift from the domain behind it.

**Local-dev trap:** `TRUNCATE users CASCADE` cascades to *tables*, not rows, and wipes the global `maintenance_items` catalogue seeded by migration 000005 — which will not re-run. Clean test data with `DELETE FROM users` and `DELETE FROM vehicles` instead, or re-apply that migration file by hand. The automated suite sidesteps this entirely by cloning a fresh database per test rather than cleaning one; the trap is still live in the compose database you develop against.

**Date arithmetic lives in `internal/platform/civil`, nowhere else.** Civil dates — the day a service happened, the day an IPVA falls due — are `time.Time` at midnight UTC, matching a Postgres `date` column exactly. Do not reimplement `AddMonths`: Go's `AddDate` turns 31 January + 1 month into 3 March, which drifts every anniversary in the product.

Patterns now exist — `internal/identity` is the reference. Follow it rather than inventing a second way:

- A module is `handler.go` / `service.go` / `repository.go` / `dto.go` plus a generated `db/` package.
- **Handler** only translates: decode, delegate, render. It never decides anything.
- **Service** owns the rules and is the only layer that builds `apperr` values — so every client-visible message for a module sits in one file.
- **Repository** returns plain sentinel errors (`ErrUserNotFound`), never `apperr`. Multi-step writes are single methods that own their transaction, so no caller can perform half of one.
- Handlers surface errors through `httpx.Error`; nothing else writes an error body.
- Response DTOs are written by hand, never the sqlc struct — a renamed column must not silently change the API contract.
- **`api/openapi.yaml` is the contract, and it is hand-written.** Change a request or response DTO and you change it in the same edit — the Flutter app generates its Dart client from that file, and `TestRouterAndOpenAPIAgree` fails the build when a route drifts from it. That guard compares paths and methods, not schemas, so a changed field is still on you — the golden files are the other half. Validate with `docker run --rm -v "$PWD:/spec" redocly/cli lint /spec/api/openapi.yaml`.

Two rules that are load-bearing rather than stylistic:

- **Nothing reaches a vehicle by id without an ownership join.** `vehicle/authorize.go` is the single choke point, and the repository offers no query that fetches a vehicle without it. A vehicle the caller cannot access is reported as **404, never 403** — "you may not see this" confirms it exists. Adding a by-id-only query is how authorisation bugs get written.
- **A module that owns user-scoped data registers an `identity.UserDataEraser`.** Account deletion cannot cascade to vehicles, because vehicles carry no `user_id`. Identity depends on the interface; wiring happens in `internal/app`.
- **Modules talk through ports with primitive signatures**, never shared structs — a shared type would have to live in one module or the other, and either direction creates an import this architecture does not want. See `maintenance.VehiclePort` and `vehicle.PlanInitializer`. The cycle between those two is broken with a setter in `internal/app/app.go`, in the open.
- **`vehicles.current_mileage_km` is maintained by a database trigger**, not by Go. Any module may insert into `odometer_readings` tagged with its own `source`, inside its own transaction; nobody has to remember to refresh the cache. Do not add a Go-side recalculation — see SPEC.md D-11.
- **Register routes as flat patterns, never nested `chi.Route`.** Both `vehicle` and `maintenance` hang endpoints off `/vehicles/{vehicleID}`, and two subrouters on overlapping prefixes make chi panic at startup.

## Things not to invent

Some of the facts `PRODUCT.md` lists as open were **decided during the backend brainstorming** and are recorded in `SPEC.md` — `PRODUCT.md` has not been updated, so trust `SPEC.md` on these:

- **Authentication:** e-mail + password (argon2id), short-lived JWT plus a rotating opaque refresh token. Social login is deferred and additive.
- **Vehicle types:** cars only in MVP-1. The `vehicle_type` column exists from the first migration so motorcycles become a catalogue seed, not a backfill.
- **Fines (multas):** not tracked in MVP-1; an `expenses` category in MVP-2.
- **Fuel logging:** not in MVP-1 at all — first item of MVP-2.
- **IPVA/licenciamento calendars:** entered by the owner. No official-data integration in the MVP.
- **FIPE:** SPEC.md listed it as deferred until after MVP-2. **It has since been built** — `internal/catalog`, migration 000009 — because it removes four free-text fields from the registration form, which is the first screen every user sees. What is built is the *catalogue*; the *valuation history* is stored (`vehicle_fipe_prices` keyed by reference month) but nothing reads more than the latest row yet.
- **Receipt and document images:** deferred out of MVP-1. Object storage is the only new infrastructure this project would need, and nothing depends on it yet — `docker-compose.yml` is Postgres and nothing else.

Still genuinely open, and still not to be invented: notification delivery, monetization and account limits.
