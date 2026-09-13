-- PostgreSQL-only additive prerequisites for the Agency Hub rollout.
-- Run with psql --set ON_ERROR_STOP=1 after backup/restore rehearsal.
-- This does NOT replace new-api's normal migration of its other core tables.
-- Existing users, credentials, balances, payment evidence and columns are preserved.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Deliberately require the existing gateway tables: never initialize an empty DB.
ALTER TABLE public.users
    ADD COLUMN IF NOT EXISTS billing_mode varchar(32),
    ADD COLUMN IF NOT EXISTS funding_version bigint;

ALTER TABLE public.top_ups
    ADD COLUMN IF NOT EXISTS payment_snapshot text,
    ADD COLUMN IF NOT EXISTS quota_conversion_snapshot text;

COMMIT;
