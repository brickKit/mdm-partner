[English](AGENTS.md) · [中文](AGENTS.zh.md)

# mdm/customer

The AI guide to developing this component. How to use it, its boundaries and contracts: BRICKKIT.md. Why it is shaped this way: `docs/design.md`. Dependencies, configuration and deployment: component.yaml.

## Code map

| Path | Owns |
|---|---|
| `backend/module/module.go` | The only entry, `New(ctx, rt)`: builds repo → service → HTTP + gRPC, starts the outbox pump and the partition loop. Same function standalone and in a shell |
| `backend/cmd/server/main.go` | One line, `besdk.RunStandalone(module.New)` |
| `backend/cmd/migrate/main.go` | One line, `migrate.Main(migrations.FS)`: the migration container's entry (`./migrate up`) |
| `backend/internal/repo/repo.go` | Shared types, sentinel errors (`ErrVersionConflict`, `ErrNotFound`, `ErrInvalidCursor`), the column list and scan functions |
| `backend/internal/repo/write.go` | The four write commands; each runs idempotency lookup → write → record key → outbox in one transaction |
| `backend/internal/repo/read.go` | `BatchGet`, `List` (keyword `q`, ranking, keyset paging), the SQL builder `listSQL` |
| `backend/internal/repo/cursor.go` | The list cursor: base64 of `created_at` and `id`, plus the match rank once paging reaches the contains-only matches |
| `backend/internal/service/` | Input validation and the error → gRPC status mapping (`status.go`), shared by REST and gRPC |
| `backend/internal/http/http.go` | REST routes, each registered with its permission key; `parseListInput` |
| `backend/internal/grpc/grpc.go` | `mdm.customer.v1.CustomerService` |
| `backend/internal/partition/` | Background loop creating weekly partitions of `event_outbox` / `event_inbox` four weeks ahead |
| `migrations/` | SQL migrations, embedded by `migrations/embed.go` |
| `contracts/` | proto, OpenAPI, event schema |
| `gen/mdm/customer/` | Generated Go code: a nested Go module, tagged on its own as gen/mdm/customer/v1.x.y; never edited by hand |
| `scripts/` | `seed.sh` / `seed-clean.sh` for local demo data |

| Feature | Start here | Then |
|---|---|---|
| Keyword search `q` on the list | `backend/internal/repo/read.go` (`listSQL`) | `backend/internal/repo/cursor.go`, `backend/internal/repo/search_test.go` |
| A new REST query parameter | `backend/internal/http/http.go` (`parseListInput`) | `contracts/customer.openapi.yaml`, `backend/internal/http/http_test.go` |
| A new field on the customer | `migrations/` (a new migration) | `contracts/` (append only), `buf generate`, `backend/internal/repo/repo.go` (`customerColumns`) |
| A new write command | `backend/internal/repo/write.go` | `backend/internal/service/service.go`, then REST and gRPC |
| An error answering with the wrong status | `backend/internal/service/status.go` | the sentinel errors in `backend/internal/repo/repo.go` |

## Build and test

```bash
# tests run against the test database brickkit_test_db, never brickkit_db;
# at the project root, make test-db-init ID=mdm/customer refreshes it and runs this component's migrations twice
export TEST_PG_DSN="postgres://postgres:<password>@localhost:5432/brickkit_test_db?sslmode=disable"
make test                    # every package ends in "ok"; refuses to run without TEST_PG_DSN
go test ./... -count=1 -v | grep -c -- '--- SKIP'   # 0: no test skipped
make check-version dag-check contract-check import-scan module-check   # each prints one ✓ line
make docs-check              # "0 with errors, 0 warnings"
# migrations, as the login role:
PG_HOST=localhost PG_PORT=5432 PG_DATABASE=brickkit_test_db PG_USER=mdm_customer_rw \
  PG_PASSWORD=<its password> PG_SCHEMA=mdm_customer make migrate-idempotent   # ✓ 迁移幂等
```

After `buf generate` (contracts changed), the contract package needs a new tag: see Pitfalls. On a real machine, from the project root: `make verify ID=mdm/customer ROUTE=/mdm/customer/customers FOCUS=1` builds the image, starts only what this component needs, checks migration, health and the permission check, runs a focus run, and tears down.

## Design decisions

- **Read by everyone, calls no one.** No dependencies, no consumed events: master data stays a leaf in the call graph. Callers fetch the customers of a page with one `BatchGet`, never a loop of `Get`.
- **No row-level scopes** (`data_scopes: none`): customer master data is shared reference data. Who works with which customer is CRM's data, not this component's.
- **Every write is idempotent and versioned.** `idempotency_key` is the primary key of `command_idempotency`, recorded in the same transaction as the write; `Update` / `SetStatus` carry the current `version` (optimistic lock). Disabling is not terminal: a customer can be re-enabled, so nothing is ever archived.
- **Search ranking lives in SQL**: `match_rank` (0 = code or name starts with `q`, 1 = only contains it) is computed in an inner query and is part of the keyset; `strpos` / `starts_with` instead of `LIKE`, so user input needs no escaping. No index: the contains branch scans the window anyway, and the table is master-data sized.
- **The default 90-day window applies to search too**, as to every list (project convention); callers looking for older customers pass `created_after`.
- **Repo split by read and write** (`write.go`, `read.go`): the four writes share one transaction shape (`replayCustomer`, `recordIdempotency`, `publish`, `updateWithVersion`).

## Pitfalls

| Never | Symptom | Why |
|---|---|---|
| Add an entry to `dependencies.components` or consume an event | Everything builds and passes; the component stops being a leaf, and the next edge someone adds can close a cycle | Master data is read by all; `make dag-check` fails on any dependency |
| Remove `BatchGet` or make callers use `Get` in a loop | Tests stay green; a list page of N rows makes N gRPC calls | `BatchGet` is the batched read every caller relies on |
| Return the `credit_limit` the caller sent instead of reading it back | `Create` answers `"0"`, a later `Get` of the same row answers `"0.00"` | `NUMERIC(18,2)` normalises the value; `insertCustomer` reads it back with `RETURNING` |
| Partition `customers` / `contacts` / `billing_infos`, or add owner / department columns | Migrations pass; nothing ever uses the columns, and undoing a partitioned table is a data migration | Master data, thousands of rows; no data scopes |
| Change a comment or option in `contracts/mdm/customer/v1/customer.proto` without tagging the contract package | Builds pass locally (the `replace` hides it); a shell fetching `mdm-customer/v2` compiles against the old `gen/` from the tag `go.mod` requires | Any change under `gen/mdm/customer/` needs a new tag `gen/mdm/customer/v1.x.y` and the root `go.mod` requiring it |
| Order the list by anything other than `match_rank, created_at DESC, id DESC`, or drop `rank` from the cursor | Paging skips or repeats rows exactly where prefix matches end and contains matches begin | The keyset condition in `listSQL` assumes this order; `TestList_q与游标一起翻页不重不漏` covers it |
| Search with `LIKE` / `ILIKE` on the raw input | `q=%` returns every customer | `strpos` / `starts_with` treat `%` and `_` literally |
| Validate `credit_limit` with `strconv.ParseFloat` | `"NaN"`, `"Inf"`, `"1e3"` pass; NaN is stored, ranks above every number, and flows to `erp/sales` and `erp/finance` through the events, where every credit check against it passes | `validateCreditLimit` matches the string against `NUMERIC(18,2)`'s shape and never converts to a float |
| Log a caller error (`4xx`) at ERROR | The shell's and the operator's ERROR lines fill with duplicate codes and stale versions | `logFailure` keeps ERROR for errors that map to `Internal`; cancels are Warn |
| Classify a `customers_code_uniq` violation as `ErrCodeTaken` when the caller sent no code | A Create without a code answers `409` "code already taken" and is logged at Info; nobody learns that explicit codes occupy the range the sequence hands out | Generated and explicit codes share one namespace. `insertAutoCode` inserts with `ON CONFLICT (code) DO NOTHING` and moves to the next sequence value, up to `autoCodeAttempts` (5), then fails as `Internal`; `Create` classifies only when `in.Code != ""` |

## Before changing code

1. Is this about who the customer is (here) or about who works with the customer (CRM)? The second never goes here.
2. Am I adding a dependency, a consumed event, partitioning or a scope column? Stop: see Pitfalls and `docs/design.md`.
3. Contract change? Append only: a new field, a new rpc, a new query parameter. Never remove or retype a field, rpc, path or event subject.
4. Did `gen/` change? Then the contract package needs a new tag and `go.mod` must require it.
5. A new REST route is registered with `besdk.GET` / `POST` / `PATCH` and a permission key from `assembly.yaml`.
6. New rule → write the failing test first (real database for anything SQL), then the code.
7. Bump `metadata.version` before the first change after a release; update BRICKKIT.md and `docs/design.md` in the same commit as the code.

<!-- brickkit:managed:begin lang=en -->
<!-- maintained by brickkit (init, add, remove, upgrade, skills update): edits between these markers are overwritten -->

## BrickKit

This is a BrickKit component: `component.yaml` is all the platform reads. The rules it relies on:

- `configSchema` keys are the environment variable names the code reads. Never use a reserved name: `COMPONENT_ID`, `COMPONENT_VERSION`, `PORT`, `BRICKKIT_SERVED_MEMBERS`, `BRICKKIT_SERVED_MEMBERS_CONFIG`, or any `*_ENDPOINT`.
- Dependencies are exact versions. A dependency's address arrives as `<ID>_ENDPOINT`; an optional dependency that is absent has no variable at all, so read it with a fallback.
- `/healthz` checks only this process, never a dependency. The migration command runs from the same image and must fail on an argument it does not know.
- `BRICKKIT.md` travels to every project that uses this component and is read there without the repository: keep it in step with the code, with no relative links.
- Release: raise `metadata.version`, commit, push, `brickkit release`. `brickkit lint` checks the manifest and these docs — inside a project, run in this directory, it checks only this component (`--all` for the whole project).
- The full rules are in the `brickkit-component` skill (`.claude/skills/brickkit-component/SKILL.md` at the root of the project or repository where skills are installed; `brickkit skills update` installs it); for flags ask `brickkit <command> --help`; BrickKit's own documentation is `brickkit docs`.
<!-- brickkit:managed:end -->
