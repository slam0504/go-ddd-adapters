-- pgxoutbox migration 002: terminal dead-letter table.
--
-- No FK to outbox_records.id because Terminate deletes the source row
-- in the same CTE that inserts here.

CREATE TABLE outbox_dead_letters (
    id              BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    outbox_id       BIGINT      NOT NULL,
    event_id        TEXT        NOT NULL,
    topic           TEXT        NOT NULL,
    event_name      TEXT        NOT NULL,
    aggregate_id    TEXT        NOT NULL,
    aggregate_type  TEXT        NOT NULL,
    payload         BYTEA       NOT NULL,
    headers         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL,
    available_at    TIMESTAMPTZ NOT NULL,
    attempts        INTEGER     NOT NULL,
    reason          TEXT        NOT NULL,
    failed_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_outbox_dead_letters_failed_at
    ON outbox_dead_letters (failed_at DESC);

CREATE INDEX idx_outbox_dead_letters_event_id
    ON outbox_dead_letters (event_id);
