[English](design.md) · [中文](design.zh.md)

# mdm/customer design

Conclusions only, for whoever changes this component's design. How to use it: `BRICKKIT.md`; how the code is laid out: `AGENTS.md`.

## Boundaries

This component is the one record of who a customer is: name, code, tax ID, credit limit, status, contacts, billing details.

| Not here | Owner | Why |
|---|---|---|
| Who in sales owns or follows a customer (pools, owner, follow-up status, 360° view) | a CRM component | That is "how sales works with the customer", not "who the customer is"; CRM keeps this component's ID plus a summary copy |
| Orders, invoices, receivables, credit used by open orders | `erp/sales`, `erp/finance` | Transactions happen there; this component only holds the credit limit they check against |
| The organisation tree | an organisation master-data component | Unrelated: customer master data has no row-level scope (see Data scopes) |

Deleting a customer is disabling it (`POST /customers/{id}/status`); there is no delete endpoint, because orders and invoices keep referring to the customer.

## Owned data

| Table | Partitioned | Notes |
|---|---|---|
| `customers` | no | `code` unique (`customers_code_uniq`); `credit_limit NUMERIC(18,2)`; `status` `ACTIVE` / `DISABLED`; `version` for optimistic locking |
| `contacts` | no | foreign key to `customers` |
| `billing_infos` | no | foreign key to `customers`; one to three per customer |
| `command_idempotency` | no | `idempotency_key` primary key, the result ID of the first execution |
| `event_outbox`, `event_inbox` | weekly, by `created_at` | the outbox the pump publishes from; partitions are created four weeks ahead by the component itself |

There is no terminal state: `DISABLED` means "not usable for new business", and a customer can be re-enabled. `code` is generated as `C` + six digits from the ID sequence when the caller leaves it empty, and taken as given otherwise (imports, migrations from another system).

## Contract surface

gRPC `mdm.customer.v1.CustomerService`, for other components:

| rpc | Kind | Notes |
|---|---|---|
| `Create` | write | idempotent by `idempotency_key` |
| `Update`, `SetStatus` | write | idempotent, plus the current `version`; a stale version → `ABORTED` |
| `AddContact` | write | idempotent; publishes no event |
| `Get`, `List` | read | `List` pages by cursor, never by offset; default window the last 90 days |
| `BatchGet` | read | the batched read callers use instead of a loop of `Get`; missing IDs in `missing_ids` |
| `GetSummary` | read | ID, code, name, status, version: what a summary copy stores |

REST under `/mdm/customer`, every route behind a key: `GET /customers` and `GET /customers/{id}` (`mdm.customer.view`), `POST /customers` (`mdm.customer.create`), `PATCH /customers/{id}` (`mdm.customer.update`), `POST /customers/{id}/status` (`mdm.customer.set_status`), `POST /customers/{id}/contacts` (`mdm.customer.add_contact`). `BatchGet` is gRPC only: no end-user screen asks for a list of customer IDs, and on the public API it would invite use as a bulk export. There is no status endpoint for timed-out writes: no component writes here synchronously as part of its own transaction.

**Keyword search** (`GET /customers?q=`), for the customer pickers (order entry, opportunity entry, mobile search):

- `q` is trimmed; empty means no filter, so the list is exactly the unfiltered one.
- A row matches when `lower(code)` or `lower(name)` contains `lower(q)`. Matching uses `strpos` and `starts_with`, not `LIKE`: `%` and `_` in user input are literal characters and need no escaping.
- Rank 0 when code or name starts with `q`, rank 1 when it only contains it. Order: rank, then `created_at` descending, then `id` descending.
- The rank is part of the keyset: the cursor carries it once paging reaches rank 1, so pages never skip or repeat a row at the boundary. Cursors of unfiltered lists keep their previous format.
- The default 90-day window applies, as to every list in the project; a picker that must find older customers passes `created_after`. REST reads `created_after` / `created_before` (RFC 3339; malformed → `400`).
- No index. The contains branch must scan the rows in the window whatever index exists, and a prefix index cannot serve an `OR` with it; the table is master-data sized (thousands to tens of thousands of rows). If it ever grows, a trigram index (`pg_trgm`) is the answer, not a prefix index.
- REST only. gRPC `ListRequest` is unchanged; adding `q` there is an append-only change when a caller needs it.

## Events

Published, all through the outbox in the same transaction as the write:

| Subject | When | Payload |
|---|---|---|
| `mdm.customer.created.v1` | `Create` | id, code, name, tax ID, credit limit, status, version |
| `mdm.customer.updated.v1` | `Update`, and `SetStatus` back to `ACTIVE` | id, name, tax ID, credit limit, status, version |
| `mdm.customer.disabled.v1` | `SetStatus` to `DISABLED` | id, version (plus the same fields as updated) |

There is no "enabled" subject: re-enabling is an update whose payload carries the status. A new subject is a contract change, made in the contract first. Consumers apply an event only when its `version` is greater than the one they hold, which makes them immune to reordering and redelivery.

Consumed: none. Consuming an event would make master data depend on someone.

## Dependencies

None, by design. Expected but absent:

| Not a dependency | Why |
|---|---|
| any business component | master data is read by everyone and calls no one |
| `infra/iam-casdoor` | tokens are verified locally against the JWKS at `IAM_JWKS_URL`: configuration, not an edge |
| `infra/authz` | the permission bundle is polled from `AUTHZ_BUNDLE_URL`: configuration, not an edge |
| NATS | a base resource reached through `NATS_URL`, not a component |

## Place in the synchronous call graph

A leaf: only incoming edges. `crm/opportunity`, `erp/sales` and the mobile BFF call it over gRPC; it calls no one. It is one of the three hubs of the project (master data read by all; `erp/inventory` the only writer of stock movements; `erp/finance` mostly commanded and listening). Being read by both CRM and ERP does not break "no synchronous edge between CRM and ERP": this component is neither.

## Partitioning and archiving

`customers`, `contacts` and `billing_infos` are not partitioned and never archived: master data does not grow with time, and with no terminal state there is never a moment when a row is "finished". The schema `mdm_customer_archive` may exist by project convention; nothing writes to it, and `BatchGet`'s archive fallback finds no table and returns the hot rows only. `event_outbox` / `event_inbox` are partitioned weekly and roll forward with the partitions.

## Data scopes

None (`data_scopes: none`). Customer master data is reference data that sales, finance and the warehouse must all see the same way; "only my customers" is a restriction on the CRM relationship, not on the master record. So the tables have no owner or department columns.

## Reference implementations

| Project | Version | Module consulted | What was borrowed | License | Usage |
|---|---|---|---|---|---|
| Odoo | 17.0 | `addons/base/models/res_partner.py` | which customer and contact fields matter (tax ID, credit-related fields) | LGPL-3 | Borrowed reasoning |
| Apache OFBiz | 18.12 | `applications/party/entitydef/entitymodel.xml` | party separated from its roles and relationships: supports keeping the CRM relationship out of the master record | Apache-2.0 | Borrowed reasoning |

**Deliberately avoided**: Odoo's single `res.partner` table for companies, people, addresses and bank accounts, told apart by `is_company` / `parent_id`. Here the data is split by ownership (`customers`, `contacts`, `billing_infos`), so no query has to ask what a row is.

No slot family: both references point the same way, and no customer profile needs a different shape.

## Open questions

| Question | Current answer |
|---|---|
| Is `NUMERIC(18,2)` enough for the credit limit, and in which currency? | Single currency, two decimals, as for order totals; widening later is an additive migration |
| Contacts and billing details are written but not returned by `Get` / `List` / `BatchGet` | Not yet needed by any screen; adding them to the read path is an append-only contract change |
| Should keyword search ignore the default 90-day window? | No, for now: the project convention applies to every list, and pickers pass `created_after`. Revisit if pickers find it awkward (the frontend work decides) |
| Two concurrent requests with the same `idempotency_key`: the loser gets an error, not the winner's result | Only one row lands, which is the promise; returning the winner's result to the loser would need a retry on the primary-key conflict |
