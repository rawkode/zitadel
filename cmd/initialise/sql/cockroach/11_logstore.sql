CREATE SCHEMA IF NOT EXISTS logstore;

GRANT ALL ON ALL TABLES IN SCHEMA logstore TO %[1]s;

CREATE TABLE IF NOT EXISTS logstore.access (
    ts TIMESTAMPTZ,
    protocol INT,
    request_url TEXT,
    response_status INT,
    request_headers JSONB,
    response_headers JSONB
)
