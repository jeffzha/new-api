BEGIN;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10s';
CREATE TABLE IF NOT EXISTS agency_hub_ops_publishers (
    id bigserial PRIMARY KEY,
    generation bigint NOT NULL
);
CREATE TABLE IF NOT EXISTS agency_hub_ops_publications (
    id bigserial PRIMARY KEY,
    source_id bigint NOT NULL,
    event_id varchar(191) NOT NULL,
    payload_hash varchar(128) NOT NULL,
    created_at_ms bigint NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_agency_ops_publication_source
    ON agency_hub_ops_publications(source_id);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_agency_ops_publication_event
    ON agency_hub_ops_publications(event_id);
INSERT INTO agency_hub_ops_publishers(id,generation) VALUES(1,0)
    ON CONFLICT(id) DO NOTHING;
COMMIT;
