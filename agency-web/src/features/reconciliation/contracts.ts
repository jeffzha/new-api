export type ResolutionAction = "verify_resolved" | "restore_delivery";
export interface ReconciliationIssue {
  id: string;
  object_type: string;
  object_id: string;
  object_name?: string;
  difference: string;
  evidence_hash: string;
  status: "open" | "resolved" | "ignored";
  resolution: string;
  actor_id?: string | null;
  created_at_ms: string;
  resolved_at_ms?: string | null;
  resolution_evidence?: string;
  repair_event_id?: string;
}
export interface ReconciliationDetail {
  issue: ReconciliationIssue;
  verification: {
    state: "consistent" | "inconsistent" | "unsupported";
    evidence_hash: string;
    checks: { name: string; expected: string; actual: string; matched: boolean }[];
    allowed_actions: ResolutionAction[];
  };
}

// Build only the action the operator reviewed. The server independently
// verifies the evidence again in the authorized mutation transaction.
export function resolutionRequest(
  detail: ReconciliationDetail,
  action: ResolutionAction,
  status: "resolved" | "ignored",
  resolution: string,
) {
  const verification = detail.verification;
  if (!verification.allowed_actions.includes(action)) {
    throw new Error("This action is not available for the current evidence.");
  }
  if (action === "verify_resolved" && verification.state !== "consistent") {
    throw new Error("The discrepancy must be verified before it can be closed.");
  }
  if (action === "restore_delivery" && status !== "resolved") {
    throw new Error("A delivery repair must be recorded as resolved.");
  }
  if (!resolution.trim() || resolution.length > 2000) {
    throw new Error("Enter a resolution note of up to 2000 characters.");
  }
  if (!verification.evidence_hash) {
    throw new Error("Reload the evidence before continuing.");
  }
  return {
    action,
    status,
    expected_evidence_hash: verification.evidence_hash,
    resolution: resolution.trim(),
  };
}
