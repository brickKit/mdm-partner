-- Business partners (with their formal contacts and addresses) and their roles by legal entity. Every record has a
-- version (protocol 02, S1) and is published as a full-state event in the transaction that changed it. Identities are
-- never reused; nothing is deleted in this version (merging and erasure come later).
CREATE TABLE partner (
    id             uuid PRIMARY KEY,
    kind           text NOT NULL CHECK (kind IN ('organization', 'person')),
    name           text NOT NULL CHECK (name <> ''),
    names          jsonb NOT NULL DEFAULT '{}'::jsonb,
    country        text NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    tax_id         text CHECK (tax_id <> ''),
    state          text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'suspended', 'retired')),
    contacts       jsonb NOT NULL DEFAULT '[]'::jsonb,
    addresses      jsonb NOT NULL DEFAULT '[]'::jsonb,
    search         text NOT NULL,
    search_version integer NOT NULL,
    version        bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
-- duplicates by tax ID in a country are pointed out (D22), not refused: no unique index
CREATE INDEX partner_tax ON partner (country, tax_id) WHERE tax_id IS NOT NULL;
CREATE INDEX partner_by_name ON partner (name, id);

-- one role of a kind per partner and legal entity; the legal entity is mdm/org's (an opaque reference, protocol 14)
CREATE TABLE partner_role (
    id               uuid PRIMARY KEY,
    partner_id       uuid NOT NULL REFERENCES partner (id),
    role             text NOT NULL CHECK (role IN ('customer')),
    legal_entity_id  uuid NOT NULL,
    state            text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'suspended', 'retired')),
    default_currency text CHECK (default_currency ~ '^[A-Z]{3}$'),
    payment_terms    text CHECK (payment_terms <> ''),
    version          bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (partner_id, role, legal_entity_id)
);
