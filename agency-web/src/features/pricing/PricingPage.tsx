import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PageHeading } from "../../components/Heading";
import { PageHeader } from "../../components/PageHeader";
import { ErrorNotice, Field, Pager } from "../../components/ui";
import { useQuery } from "../../lib/client";
import type { AgencyList } from "../agencies/types";
import { AgencySalesEditor } from "./AgencySalesEditor";
import { PlatformPricingEditor } from "./PlatformPricingEditor";

export function PricingPage(props: { root: boolean; agencyId: string | null }) {
  const { t } = useTranslation();
  const [selected, setSelected] = useState(props.agencyId ?? "");
  const [cursor, setCursor] = useState("");
  const agencies = useQuery<AgencyList>(
    props.root ? "/root/agencies?limit=200&cursor=" + encodeURIComponent(cursor) : null,
  );

  if (!props.root) {
    return (
      <>
        <PageHeader
          icon="pricing"
          title={t("Agency sales coefficients")}
          description={t("Review your platform cost and set customer sales coefficients by model.")}
        />
        <AgencySalesEditor root={false} agencyId={null} />
      </>
    );
  }

  return (
    <>
      <PageHeader
        icon="pricing"
        title={t("Platform pricing policy")}
        description={t("Live models and channels come from the platform. Configure the three business coefficients for each model.")}
      />
      <PlatformPricingEditor />
      <section className="pricing-agency-section">
        <PageHeading icon="agency" level={2}>{t("Agency sales coefficients")}</PageHeading>
        <p className="muted">
          {t("Choose an agency to review its platform cost and manage only its customer sales coefficients.")}
        </p>
        <div className="page-filter-bar">
          <Field label={t("Agency sales coefficients")}>
            <select value={selected} onChange={(event) => setSelected(event.target.value)}>
              <option value="">{t("Select an agency")}</option>
              {selected && !agencies.data?.items.some((row) => String(row.id) === selected) && (
                <option value={selected}>{selected}</option>
              )}
              {(agencies.data?.items ?? []).map((row) => (
                <option key={row.id} value={String(row.id)}>{row.display_name}</option>
              ))}
            </select>
          </Field>
          <Pager
            nextCursor={agencies.data?.meta?.next_cursor}
            hasPrevious={Boolean(cursor)}
            onNext={() => setCursor(agencies.data?.meta?.next_cursor ?? "")}
            onReset={() => setCursor("")}
          />
          <ErrorNotice error={agencies.error} />
        </div>
        {selected ? (
          <AgencySalesEditor key={selected} root agencyId={selected} />
        ) : (
          <p className="empty">{t("Select an agency to view and edit its sales coefficients.")}</p>
        )}
      </section>
    </>
  );
}
