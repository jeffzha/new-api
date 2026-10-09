import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Dialog, Field } from "../../components/ui";
import {
  adjustCoefficientValues,
  formatCoefficient,
  parseSignedCoefficient,
  stepSignedCoefficient,
  type CoefficientAdjustmentError,
  type CoefficientAdjustmentOptions,
  type CoefficientAdjustmentRow,
} from "./policy";

// Fixed step used by the +/- shortcuts in the adjustment amount field.
const STEP_BPS = 1000;

export type AdjustmentOutcome = {
  values: Record<string, string>;
  errors: Record<string, CoefficientAdjustmentError>;
  updated: number;
};

/**
 * Shared bulk-adjustment dialog. It asks for an anchor (my cost or the drafted
 * value) plus a signed step, previews every selected row and only writes the
 * rows that keep the cost, spread and cap rules.
 */
export function CoefficientAdjustDialog(props: {
  title: string;
  description: string;
  rows: CoefficientAdjustmentRow[];
  values: Record<string, string>;
  options: CoefficientAdjustmentOptions;
  onApply: (outcome: AdjustmentOutcome) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [anchor, setAnchor] = useState<"cost" | "current">("cost");
  const [amount, setAmount] = useState("0.1000");
  const [error, setError] = useState<string | null>(null);
  const deltaBPS = useMemo(() => {
    try {
      return parseSignedCoefficient(amount.trim());
    } catch {
      return null;
    }
  }, [amount]);
  const outcome = useMemo(
    () =>
      deltaBPS == null
        ? null
        : adjustCoefficientValues(props.rows, props.values, props.rows.map((row) => row.model), deltaBPS, {
            ...props.options,
            anchor,
          }),
    [anchor, deltaBPS, props.options, props.rows, props.values],
  );
  const failed = outcome ? Object.keys(outcome.errors).length : 0;

  function message(code: CoefficientAdjustmentError) {
    if (code === "missing_cost") return t("My cost is missing for this model.");
    if (code === "below_cost") return t("This adjustment would be below the agency cost.");
    if (code === "below_spread")
      return t(
        "This adjustment is above cost but does not meet the minimum spread. Use the exact cost price or meet the full minimum spread.",
      );
    if (code === "above_cap") return t("This adjustment would exceed the sales cap.");
    return t("The current coefficient is invalid. Enter a valid coefficient before adjusting it.");
  }

  function step(deltaBPS: number) {
    setAmount((current) => stepSignedCoefficient(current, deltaBPS));
    setError(null);
  }

  function currentText(row: CoefficientAdjustmentRow) {
    const drafted = props.values[row.model]?.trim();
    if (drafted) return drafted;
    return row.inheritedBPS == null ? t("Not configured") : formatCoefficient(row.inheritedBPS);
  }

  function apply() {
    if (!outcome) {
      setError(t("Enter a valid adjustment, such as 0.1000 or -0.1000."));
      return;
    }
    if (!outcome.updated) {
      setError(t("No selected models can be adjusted with this amount."));
      return;
    }
    props.onApply(outcome);
  }

  return (
    <Dialog title={props.title} onClose={props.onClose}>
      <p className="muted">{props.description}</p>
      <div className="coefficient-adjust-controls">
        <div className="coefficient-anchor" role="group" aria-label={t("Anchor")}>
          <span className="coefficient-anchor-label">{t("Anchor")}</span>
          <button
            type="button"
            className={anchor === "cost" ? "" : "secondary"}
            aria-pressed={anchor === "cost"}
            onClick={() => setAnchor("cost")}
          >
            {t("From my cost")}
          </button>
          <button
            type="button"
            className={anchor === "current" ? "" : "secondary"}
            aria-pressed={anchor === "current"}
            onClick={() => setAnchor("current")}
          >
            {t("From current value")}
          </button>
        </div>
        <Field label={t("Adjustment amount")}>
          <div className="coefficient-step">
            <input
              inputMode="decimal"
              placeholder="0.1000 / -0.1000"
              value={amount}
              onChange={(event) => {
                setAmount(event.target.value);
                setError(null);
              }}
            />
            <button type="button" className="secondary compact-action" onClick={() => step(-STEP_BPS)}>
              -0.1
            </button>
            <button type="button" className="secondary compact-action" onClick={() => step(STEP_BPS)}>
              +0.1
            </button>
          </div>
        </Field>
      </div>
      <div className="table-wrap coefficient-adjust-preview">
        <table className="pricing-matrix">
          <thead>
            <tr>
              <th>{t("Model name")}</th>
              <th>{t("My cost (coefficient)")}</th>
              <th>{t("Current value")}</th>
              <th>{t("After adjustment")}</th>
            </tr>
          </thead>
          <tbody>
            {props.rows.map((row) => {
              const rowError = outcome?.errors[row.model];
              const next = outcome && !rowError ? outcome.values[row.model] : "";
              const changed = Boolean(next) && next !== currentText(row);
              return (
                <tr
                  key={row.model}
                  className={rowError ? "customer-pricing-row-error" : changed ? "coefficient-adjust-changed" : undefined}
                >
                  <td>
                    <strong>{row.model}</strong>
                  </td>
                  <td>
                    <span className="coefficient-readonly">
                      {row.costBPS == null ? "—" : formatCoefficient(row.costBPS)}
                    </span>
                  </td>
                  <td>{currentText(row)}</td>
                  <td>
                    {rowError ? (
                      <small className="customer-pricing-inline-error">{message(rowError)}</small>
                    ) : (
                      <span className="coefficient-adjust-next">{next || "—"}</span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className={failed ? "coefficient-adjust-summary warning" : "coefficient-adjust-summary success"} role="status">
        {t("Adjusted {{updated}} models. {{failed}} models were not changed.", {
          updated: outcome?.updated ?? 0,
          failed,
        })}
      </p>
      <ErrorNoticeSlot error={error} />
      <div className="actions dialog-actions">
        <button className="secondary" type="button" onClick={props.onClose}>
          {t("Cancel")}
        </button>
        <button
          type="button"
          className="button-icon"
          disabled={!outcome || outcome.updated === 0}
          onClick={apply}
        >
          <span aria-hidden="true" className="channel-filter-icon">
            <CheckGlyph />
          </span>
          {t("Apply to selected models")}
        </button>
      </div>
    </Dialog>
  );
}

function ErrorNoticeSlot(props: { error: string | null }) {
  if (!props.error) return null;
  return (
    <p role="alert" className="error">
      {props.error}
    </p>
  );
}

function CheckGlyph() {
  return (
    <svg
      width="17"
      height="17"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="m5 12 5 5L19 7" />
    </svg>
  );
}
