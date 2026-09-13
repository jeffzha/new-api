import { useTranslation } from "react-i18next";
import { Table } from "../../components/ui";
import { formatCoefficient } from "./policy";
import type { Policy } from "./types";

export function PolicyComparison(props: { before: Policy; after: Policy }) {
  const { t } = useTranslation();
  const rows = [
    { key: "default_settlement_bps", label: t("Default settlement coefficient") },
    { key: "default_sales_bps", label: t("Default sales coefficient") },
    { key: "min_spread_bps", label: t("Minimum spread") },
    { key: "sales_cap_bps", label: t("Sales coefficient cap") },
  ].map((row) => ({
    key: row.key,
    label: row.label,
    before: formatCoefficient(props.before[row.key as keyof Policy] as number),
    after: formatCoefficient(props.after[row.key as keyof Policy] as number),
  }));
  const before = new Map(
    (props.before.model_overrides ?? []).map((row) => [row.origin_model_name, row]),
  );
  const after = new Map(
    (props.after.model_overrides ?? []).map((row) => [row.origin_model_name, row]),
  );
  for (const name of new Set([...before.keys(), ...after.keys()])) {
    for (const key of ["settlement_bps", "sales_bps"] as const) {
      const oldValue = before.get(name)?.[key];
      const newValue = after.get(name)?.[key];
      rows.push({
        key: name + key,
        label:
          name + " · " + t(key === "sales_bps" ? "Sales coefficient" : "Settlement coefficient"),
        before: oldValue == null ? t("Inherit") : formatCoefficient(oldValue),
        after: newValue == null ? t("Inherit") : formatCoefficient(newValue),
      });
    }
  }
  const changes = rows.filter((row) => row.before !== row.after);
  return (
    <section>
      <h3>{t("Changes to publish")}</h3>
      {changes.length === 0 ? (
        <p>{t("No price changes.")}</p>
      ) : (
        <Table
          rows={changes}
          rowKey={(row) => row.key}
          columns={[
            { key: "label", label: t("Price rule") },
            { key: "before", label: t("Current") },
            { key: "after", label: t("Draft") },
          ]}
        />
      )}
    </section>
  );
}
