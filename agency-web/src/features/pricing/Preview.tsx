import { useTranslation } from "react-i18next";
import { Table } from "../../components/ui";
import { formatCoefficient } from "./policy";
import type { Policy, PricePreview } from "./types";

export function Preview(props: { data: PricePreview; policy?: Policy }) {
  const { t } = useTranslation();
  const standard = props.data.standard_quota;
  const values = [
    { label: t("Customer charge"), value: props.data.customer_quota },
    { label: t("Agency settlement"), value: props.data.settlement_quota },
    { label: t("Theoretical commission"), value: props.data.commission_quota },
  ];
  return (
    <section aria-live="polite">
      <h3>{t("Preview based on {{quota}} standard quota units", { quota: standard })}</h3>
      <div className="metrics">
        {values.map((row) => (
          <article key={row.label}>
            <span>{row.label}</span>
            <strong>{row.value.toLocaleString()}</strong>
          </article>
        ))}
      </div>
      {Boolean(props.data.model_previews?.length) && (
        <Table
          rows={props.data.model_previews || []}
          rowKey={(row) => row.origin_model_name}
          columns={[
            { key: "origin_model_name", label: t("Public model") },
            {
              key: "settlement",
              label: t("Settlement coefficient"),
              render: (row) => formatCoefficient(row.settlement_bps),
            },
            {
              key: "sales",
              label: t("Sales coefficient"),
              render: (row) => formatCoefficient(row.sales_bps),
            },
            {
              key: "charge",
              label: t("Customer charge"),
              render: (row) => row.customer_quota.toLocaleString(),
            },
            {
              key: "cost",
              label: t("Agency settlement"),
              render: (row) => row.settlement_quota.toLocaleString(),
            },
            {
              key: "commission",
              label: t("Theoretical commission"),
              render: (row) => row.commission_quota.toLocaleString(),
            },
          ]}
        />
      )}
      <p className="muted">
        {t("This is a price calculation. It does not call a model or generate a charge.")}
      </p>
    </section>
  );
}
