-- pgxoutbox migration 001: active outbox table.
--
-- Postgres baseline is 12+. gen_random_uuid() is built-in on 13+;
-- pgcrypto provides it on 12 (no-op on 13+).
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE outbox_records (
    id              BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id        TEXT        NOT NULL,
    topic           TEXT        NOT NULL,
    event_name      TEXT        NOT NULL,
    aggregate_id    TEXT        NOT NULL,
    aggregate_type  TEXT        NOT NULL,
    payload         BYTEA       NOT NULL,
    headers         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL,
    available_at    TIMESTAMPTZ NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    last_error      TEXT        NULL,
    claimed_until   TIMESTAMPTZ NULL,
    claim_token     UUID        NULL
);

-- Composite index for Fetch ORDER BY (available_at, id).
-- Partial WHERE ... <= now() is rejected (now() not IMMUTABLE), so the
-- index is unconditional; the Fetch CTE filters in the WHERE clause.
CREATE INDEX idx_outbox_records_due
    ON outbox_records (available_at, id);

-- Secondary partial index for lease-expiry sweeps. IS NOT NULL is
-- IMMUTABLE so partial form is allowed here.
CREATE INDEX idx_outbox_records_claimed_until
    ON outbox_records (claimed_until)
    WHERE claimed_until IS NOT NULL;
