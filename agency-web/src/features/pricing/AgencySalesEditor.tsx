import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import { ErrorNotice, Field, Loading } from "../../components/ui";
import { useQuery } from "../../lib/client";
import { useMutation } from "../../lib/mutations";
import { formatCoefficient, parseCoefficient } from "./policy";
import type { ModelSales } from "./types";

export function AgencySalesEditor(props: { root: boolean; agencyId: string | null }) {
  const path = props.root ? `/root/agencies/${props.agencyId}/pricing` : "/pricing";
  const pricing = useQuery<ModelSales>(path + "/model-sales");
  if (pricing.loading) return <Loading />;
  if (!pricing.data) return <ErrorNotice error={pricing.error} />;
  return (
    <AgencySalesForm
      key={`${pricing.data.agency_id}:${pricing.data.revision}:${pricing.data.platform_revision}`}
      root={props.root}
      path={path}
      data={pricing.data}
      reload={pricing.reload}
    />
  );
}

function AgencySalesForm(props: {
  root: boolean;
  path: string;
  data: ModelSales;
  reload: () => void;
}) {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [sales, setSales] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      props.data.items.map((row) => [
        row.origin_model_name,
        row.override_sales_bps == null ? "" : formatCoefficient(row.override_sales_bps),
      ]),
    ),
  );

  async function publish() {
    setError(null);
    try {
      await mutation.mutate(
        props.root ? props.path + "/sales/publish" : "/pricing/sales/publish",
        {
          expected_revision: props.data.revision,
          default_sales_bps: props.data.default_sales_bps,
          model_sales_overrides: props.data.items.map((row) => ({
            origin_model_name: row.origin_model_name,
            sales_bps:
              sales[row.origin_model_name] === ""
                ? null
                : parseCoefficient(sales[row.origin_model_name]),
          })),
          reason,
        },
        { action: "pricing.sales.publish", objectId: `agency:${props.data.agency_id}` },
      );
      props.reload();
    } catch (cause) {
      setError(cause);
    }
  }

  return (
    <section className="pricing-matrix-card agency-sales-card">
      <div className="toolbar pricing-toolbar">
        <div>
          <h3>{props.data.agency_name}</h3>
          <p className="muted">
            {t("Agency cost is controlled by the platform. Leave sales blank to inherit the platform default.")}
          </p>
        </div>
        <button className="secondary button-icon" type="button" onClick={props.reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button>
      </div>
      <div className="table-wrap pricing-matrix-wrap">
        <table className="pricing-matrix">
          <thead>
            <tr>
              <th>{t("Model name")}</th>
              <th>{t("Agency cost coefficient")}</th>
              <th>{t("Sales coefficient")}</th>
            </tr>
          </thead>
          <tbody>
            {props.data.items.map((row) => (
              <tr key={row.origin_model_name}>
                <td><strong>{row.origin_model_name}</strong></td>
                <td><span className="coefficient-readonly">{formatCoefficient(row.agency_cost_bps)}</span></td>
                <td>
                  <div className="sales-coefficient-field">
                    <input
                      aria-label={`${t("Sales coefficient")}: ${row.origin_model_name}`}
                      inputMode="decimal"
                      value={sales[row.origin_model_name]}
                      placeholder={formatCoefficient(row.platform_default_sales_bps)}
                      onChange={(event) =>
                        setSales((current) => ({
                          ...current,
                          [row.origin_model_name]: event.target.value,
                        }))
                      }
                    />
                    <small>
                      {sales[row.origin_model_name] === ""
                        ? t("Using platform default {{value}}", {
                            value: formatCoefficient(row.platform_default_sales_bps),
                          })
                        : t("Agency override")}
                    </small>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {props.data.items.length === 0 && (
        <p className="empty">{t("Configure platform model coefficients first.")}</p>
      )}
      <div className="pricing-publish-row">
        <Field label={t("Change reason")}>
          <input value={reason} maxLength={1000} onChange={(event) => setReason(event.target.value)} />
        </Field>
        <button
          className="button-icon"
          type="button"
          disabled={mutation.pending || !reason.trim() || props.data.items.length === 0}
          onClick={() => void publish()}
        >
          <ActionIcon name="save" />
          {t("Publish sales coefficients")}
        </button>
      </div>
      <ErrorNotice error={error} />
    </section>
  );
}
