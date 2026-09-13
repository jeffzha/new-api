import { ApiError } from "../../lib/api";

const messages: Record<string, string> = {
  evidence_changed: "Reload the evidence before continuing.",
  invariant_unresolved: "The discrepancy must be verified before it can be closed.",
  issue_closed: "This issue has already been handled. Reload its evidence.",
  invalid_verification: "Verification expired or was already used. Verify again to continue.",
  verification_unavailable: "Evidence could not be read. Reload it before continuing.",
  resolution_unavailable: "The resolution was not saved. Reload the evidence and try again.",
};

export function reconciliationError(error: unknown): unknown {
  if (error instanceof ApiError && messages[error.code]) return new Error(messages[error.code]);
  return error;
}
