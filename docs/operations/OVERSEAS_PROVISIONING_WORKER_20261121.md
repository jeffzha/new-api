# Overseas binding queue recovery, 2026-11-21

- The active Agency HTTP container has AGENCY_HUB_BACKGROUND_DISABLED=true. This suppresses StartBackground, including ProcessProvisioningJobs. User 22's job 2 remained queued without task blockers.
- The existing audited customer-provision bridge completed the original job and bound the requested user to agency 3 (libo). Original quota, billing mode and auth version remained unchanged. No HTTP restart was performed.
- Do not enable the entire legacy background loop under OPS custody. The new agency-hub provision-worker/provision-once commands invoke only the existing provisioning queue, with five jobs per pass, bounded DB context, cancellation and failure backoff. Existing CAS/fencing, task and OPS admission checks remain authoritative.
- This adds no schema, index or wallet writer. Normal mode polls every two seconds; failures back off to ten seconds. A separate CPU-limited worker needs no public port, payment credentials or relay key.
- Local binding tests cover in-flight task deferral, completion, replay, unchanged OPS-owned funds/auth/billing mode, cancellation, onboarding disable and database failure. Full agencyhub regression passed before release.
- Production availability remains mandatory: keep serving HTTP/API instances unchanged. Deploy only a committed, tested immutable worker artifact, validate its exact SHA and live overseas DB scope, preserve all history and pending manual-review video bills. Rollback affects only this new worker, never user API routing. No database backup is performed per explicit user instruction.
- Deployment and live observation must be recorded separately; adding these sources does not itself mean the queue processor is running in production.
