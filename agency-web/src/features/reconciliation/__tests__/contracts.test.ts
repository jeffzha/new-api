import { describe, expect, test } from "bun:test";
import { resolutionRequest, type ReconciliationDetail } from "../contracts";

const evidence: ReconciliationDetail = {
  issue: {
    id: "9007199254740993",
    object_type: "billing_outbox",
    object_id: "original-event",
    difference: "missing delivery",
    evidence_hash: "detected-hash",
    status: "open",
    resolution: "",
    created_at_ms: "1789228800000",
  },
  verification: {
    state: "inconsistent",
    evidence_hash: "a".repeat(64),
    checks: [],
    allowed_actions: ["restore_delivery"],
  },
};

describe("reconciliation resolution requests", () => {
  test("missing delivery repair binds the reviewed evidence without converting large IDs", () => {
    expect(
      resolutionRequest(evidence, "restore_delivery", "resolved", " Original event verified "),
    ).toEqual({
      action: "restore_delivery",
      status: "resolved",
      expected_evidence_hash: "a".repeat(64),
      resolution: "Original event verified",
    });
    expect(evidence.issue.id).toBe("9007199254740993");
  });

  test("unresolved and unsupported evidence cannot be ignored or closed", () => {
    for (const state of ["inconsistent", "unsupported"] as const) {
      const detail: ReconciliationDetail = {
        ...evidence,
        verification: {
          ...evidence.verification,
          state,
          allowed_actions: ["verify_resolved"],
        },
      };
      for (const status of ["resolved", "ignored"] as const) {
        expect(() => resolutionRequest(detail, "verify_resolved", status, "Reviewed")).toThrow(
          "The discrepancy must be verified before it can be closed.",
        );
      }
    }
    expect(() => resolutionRequest(evidence, "restore_delivery", "ignored", "Reviewed")).toThrow(
      "A delivery repair must be recorded as resolved.",
    );
  });

  test("verified no-repair treatment retains a required explanation and fresh hash", () => {
    const detail: ReconciliationDetail = {
      ...evidence,
      verification: {
        ...evidence.verification,
        state: "consistent",
        allowed_actions: ["verify_resolved"],
      },
    };
    expect(
      resolutionRequest(detail, "verify_resolved", "ignored", "No repair is needed"),
    ).toMatchObject({
      action: "verify_resolved",
      status: "ignored",
      resolution: "No repair is needed",
    });
    for (const note of [" ", "x".repeat(2001)]) {
      expect(() => resolutionRequest(detail, "verify_resolved", "resolved", note)).toThrow();
    }
    detail.verification.evidence_hash = "";
    expect(() => resolutionRequest(detail, "verify_resolved", "resolved", "Reviewed")).toThrow(
      "Reload the evidence before continuing.",
    );
  });

  test("a closed issue with no permitted actions cannot be resubmitted", () => {
    const detail: ReconciliationDetail = {
      ...evidence,
      verification: {
        ...evidence.verification,
        state: "consistent",
        allowed_actions: [],
      },
    };
    expect(() => resolutionRequest(detail, "verify_resolved", "resolved", "Reviewed")).toThrow(
      "This action is not available for the current evidence.",
    );
  });
});
