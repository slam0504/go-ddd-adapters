CREATE TABLE inbox_records (
    consumer    TEXT        NOT NULL,
    event_id    TEXT        NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);
