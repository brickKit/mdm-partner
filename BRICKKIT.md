# mdm/customer

## Purpose

The one record of who each customer is: name, code, tax ID, credit limit, status (active or disabled), contacts and billing details. Every component that needs a customer reads it from here, by ID, and keeps at most a summary copy updated from this component's events.

**Owns**

- Customer master data: `code` (unique; generated as `C` + six digits when the caller leaves it empty), name, tax ID, credit limit (a decimal string), status.
- Contacts and billing details of a customer.
- The events `mdm.customer.created.v1`, `mdm.customer.updated.v1`, `mdm.customer.disabled.v1`.

**Does not own**

- Who in sales owns or follows up a customer (public pool, private pool, owner, follow-up status): a CRM component, which stores this component's customer ID plus a summary copy.
- Orders, invoices, receivables and credit used up by open orders: `erp/sales` and `erp/finance`. This component only holds the credit limit they check against.
- The organisation tree and who may see which rows: there are no row-level scopes here; customer master data is visible to everyone who may open the customer pages.

## Before you deploy

- **PostgreSQL**: a schema `mdm_customer`, plus `mdm_customer_archive` if your schema convention creates one (this component never writes to it). A login role `mdm_customer_rw` with `USAGE` and `CREATE` on `mdm_customer`, and its password. The migration runs as this role and creates the tables, so the role owns them; that matters because the running component creates weekly partitions of `event_outbox` / `event_inbox` itself, which needs ownership. BrickKit creates none of this; in the BrickEnterprise assembly project `make dev-env` writes the password into `.env` and `make db-init` creates the schemas, role and grants.
- **NATS** reachable at `NATS_URL`: the component publishes its events through an outbox table and a background pump; it starts without NATS reachable, and the events wait in the outbox.
- **Authorization** (`infra/authz`) and **identity** (`infra/iam-casdoor`, or any IAM serving a JWKS) reachable at `AUTHZ_BUNDLE_URL` and `IAM_JWKS_URL` for the REST routes to answer anything but errors. They are configuration, not dependencies: the component starts without them.
- Demo data (optional): `make seed` in the component directory creates fifteen sample customers through the real gRPC API, once the component is running.

## Dependencies

None. Customer master data is read by everyone and calls no one: other components call it over gRPC (`BatchGet` to fetch the customers of one page in one call); it never calls another component and consumes no events, so it can never sit in the middle of a call chain or a cycle.

The authorization bundle and the JWKS are fetched from the URLs in the configuration, not through a dependency edge. Without them every protected route fails closed: no or invalid token → `401`; `IAM_JWKS_URL` not set → `403`; the bundle never fetched yet → `503`; a valid user without the permission key → `403`. `/healthz` stays `200` throughout: it reports only that this process is alive.

## Configuration

| Variable | Meaning |
|---|---|
| `PG_HOST` | PostgreSQL host. Usually the project's shared value (`$var:PG_HOST`). |
| `PG_PORT` | PostgreSQL port; default `5432`. |
| `PG_DATABASE` | The database holding the `mdm_customer` schema (`$var:PG_DATABASE`). |
| `PG_USER` | The login role, `mdm_customer_rw` as a literal. Inside a shell the shell logs in with its own role and switches to this one per transaction (`SET LOCAL ROLE`), so the role name must be `<PG_SCHEMA>_rw`. |
| `PG_PASSWORD` | Password of `PG_USER`. Secret: write `${MDM_CUSTOMER_DB_PASSWORD}` (or your secret store's reference), never the value. |
| `PG_SCHEMA` | Schema of all tables, the outbox and the migration state table (`schema_migrations_mdm_customer`); default `mdm_customer`. Write the literal anyway so every component's schema is visible in one place. |
| `NATS_URL` | NATS server the outbox pump publishes to (`$var:NATS_URL`). |
| `OTEL_BASE_URL` | OpenTelemetry collector base URL; empty (the default) exports nothing. |
| `AUTHZ_BUNDLE_URL` | URL of the authorization bundle the permission check polls, for example `http://infra-authz-2-0-0:8223/authz/bundle`. Required: without it every protected route answers `503`. Keep it in step with the authz version the project runs. |
| `IAM_JWKS_URL` | URL of the JWKS used to verify user tokens locally, for example `http://infra-iam-casdoor-2-0-0:8200/.well-known/jwks.json`. Required: without it every protected route answers `403`. |

## Contracts

- `contracts/mdm/customer/v1/customer.proto` — gRPC `mdm.customer.v1.CustomerService` for other components: `Create`, `Update`, `SetStatus`, `AddContact` (each takes an `idempotency_key`; `Update` and `SetStatus` also take the current `version` and fail with `ABORTED` when it is stale), `Get`, `List` (cursor paging, no offset), `BatchGet` (missing IDs come back in `missing_ids`, not as an error), `GetSummary` (ID, code, name, status, version — what a summary copy needs). The Go package is the separate module `github.com/brickKit/mdm-customer/gen/mdm/customer`.
- `contracts/customer.openapi.yaml` — REST under `/mdm/customer`, every route behind a permission key: `GET /customers` (`mdm.customer.view`; query `q` matches code or name case-insensitively, prefix matches first; `status_filter`; `created_after` / `created_before`, default the last 90 days; `cursor`, `page_size`), `GET /customers/{id}` (`mdm.customer.view`), `POST /customers` (`mdm.customer.create`), `PATCH /customers/{id}` (`mdm.customer.update`), `POST /customers/{id}/status` (`mdm.customer.set_status`; "delete" is disabling), `POST /customers/{id}/contacts` (`mdm.customer.add_contact`). `BatchGet` is not on REST.
- `contracts/events/customer.events.json` — events published through the outbox: `mdm.customer.created.v1` (on create), `mdm.customer.updated.v1` (on update and on re-enabling), `mdm.customer.disabled.v1` (on disabling). Each carries the aggregate `version`; consumers apply an event only when its version is greater than the one they hold. No events are consumed.
- `assembly.yaml` — this project's metadata: the five permission keys above, the menu entry, the edge route `/mdm/customer/**`, the schema and role, and `data_scopes: none`.

## Shell declaration

Not a shell. It can be hosted in a Go shell (in the BrickEnterprise project, `be/go-core`) or run on its own; the code is the same either way.
