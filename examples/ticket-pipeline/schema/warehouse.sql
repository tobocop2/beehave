-- The warehouse. sqlite is the stand-in for any SQL warehouse: the load is one transaction that replaces a whole day.
CREATE TABLE IF NOT EXISTS labeled (
    day   TEXT NOT NULL,
    id    TEXT NOT NULL,
    label TEXT NOT NULL,
    PRIMARY KEY (day, id)
);
-- One row per loaded day. input_sha is the SHA-256 of the extract, so a load of the same input at the same version changes nothing.
CREATE TABLE IF NOT EXISTS manifest (
    day       TEXT PRIMARY KEY,
    version   TEXT NOT NULL,
    rows      INTEGER NOT NULL,
    input_sha TEXT NOT NULL,
    loaded_at TEXT NOT NULL
);
-- The estimated spend of the agent calls of one backfill. A row is a reservation made before the call.
CREATE TABLE IF NOT EXISTS spend (
    backfill  TEXT NOT NULL,
    day       TEXT NOT NULL,
    est_cents INTEGER NOT NULL,
    PRIMARY KEY (backfill, day)
);
