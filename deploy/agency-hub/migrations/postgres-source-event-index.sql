-- PostgreSQL online repair for Agency Hub event deduplication queries.
-- Matches AgencySourceEvent's portable GORM index for future migrations.
-- Back up the primary database first. Run with psql --set ON_ERROR_STOP=1.
-- Run in autocommit mode: CONCURRENTLY cannot run inside BEGIN/COMMIT.
-- Non-unique: reversal events may share the original operation and revision.
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- IF NOT EXISTS checks only the name; reject invalid or mismatched indexes.
DO $$
DECLARE
    existing_index oid := to_regclass('public.idx_agency_source_operation_revision');
BEGIN
    IF existing_index IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_index i
        JOIN pg_class c ON c.oid = i.indexrelid
        JOIN pg_am a ON a.oid = c.relam
        WHERE i.indexrelid = existing_index
          AND i.indrelid = 'public.agency_hub_source_events'::regclass
          AND i.indisvalid AND i.indisready AND i.indislive
          AND NOT i.indisunique AND NOT i.indisprimary
          AND i.indpred IS NULL AND i.indexprs IS NULL
          AND a.amname = 'btree' AND i.indnatts = 3
          AND i.indoption::text = '0 0 0'
          AND pg_get_indexdef(i.indexrelid, 1, true) = 'source_operation_id'
          AND pg_get_indexdef(i.indexrelid, 2, true) = 'journal_revision'
          AND pg_get_indexdef(i.indexrelid, 3, true) = 'id'
    ) THEN
        RAISE EXCEPTION 'idx_agency_source_operation_revision is invalid or has an unexpected definition; inspect it before retrying';
    END IF;
END
$$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agency_source_operation_revision
    ON public.agency_hub_source_events (source_operation_id, journal_revision, id);

RESET statement_timeout;
RESET lock_timeout;

-- Record the resulting definition and validity with the release evidence.
SELECT pg_get_indexdef(i.indexrelid) AS index_definition, i.indisvalid, i.indisready
FROM pg_index i
WHERE i.indexrelid = 'public.idx_agency_source_operation_revision'::regclass;
