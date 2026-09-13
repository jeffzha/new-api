import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQuery } from "../../lib/client";
import { ErrorNotice, Loading, Pager, Table } from "../../components/ui";
import { AgencyForm } from "./AgencyForm";
import { AgencyEditor } from "./AgencyEditor";
import { PasswordDelivery } from "./PasswordDelivery";
import type { AgencyList, PasswordDelivery as Delivery } from "./types";

export function AgenciesPage(props: {
  onPricing: (id: string) => void;
  onEnter: (id: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [cursor, setCursor] = useState("");
  const [create, setCreate] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [delivery, setDelivery] = useState<Delivery | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [copied, setCopied] = useState("");
  const [entering, setEntering] = useState(false);
  const query = useQuery<AgencyList>(
    "/root/agencies?limit=50&cursor=" + encodeURIComponent(cursor),
  );
  async function copyInvite(url: string, id: string) {
    setError(null);
    try {
      await navigator.clipboard.writeText(url);
      setCopied(id);
    } catch (cause) {
      setError(cause);
    }
  }
  async function enter(id: string) {
    setError(null);
    setEntering(true);
    try {
      await props.onEnter(id);
    } catch (cause) {
      setError(cause);
    } finally {
      setEntering(false);
    }
  }
  return (
    <>
      <div className="toolbar">
        <button
          type="button"
          disabled={create || Boolean(delivery)}
          onClick={() => {
            setCreate(true);
            setEditing(null);
          }}
        >
          {t("Create agency")}
        </button>
        <button type="button" className="secondary" onClick={query.reload}>
          {t("Refresh")}
        </button>
      </div>
      <ErrorNotice error={query.error ?? error} />
      {create && !delivery && (
        <AgencyForm
          onCancel={() => setCreate(false)}
          onCreated={(value) => {
            setDelivery(value);
            setCreate(false);
            query.reload();
          }}
        />
      )}
      {delivery && (
        <PasswordDelivery
          key={String(delivery.delivery_id)}
          delivery={delivery}
          onClose={() => setDelivery(null)}
        />
      )}
      {editing && !delivery && (
        <AgencyEditor
          key={editing}
          id={editing}
          onClose={() => setEditing(null)}
          onSaved={query.reload}
          onDelivery={setDelivery}
        />
      )}
      {query.loading ? (
        <Loading />
      ) : (
        <Table
          rows={query.data?.items ?? []}
          rowKey={(row) => String(row.id)}
          columns={[
            { key: "display_name", label: t("Agency name") },
            {
              key: "status",
              label: t("Status"),
              render: (row) => t(row.status === "active" ? "Active" : "Disabled"),
            },
            { key: "invite_code", label: t("Invitation code") },
            { key: "price_revision", label: t("Price revision") },
            {
              key: "actions",
              label: t("Actions"),
              render: (row) => (
                <div className="toolbar">
                  <button
                    type="button"
                    className="secondary"
                    disabled={Boolean(delivery)}
                    onClick={() => {
                      setEditing(String(row.id));
                      setCreate(false);
                    }}
                  >
                    {t("Edit")}
                  </button>
                  <button
                    type="button"
                    className="secondary"
                    disabled={Boolean(delivery)}
                    onClick={() => props.onPricing(String(row.id))}
                  >
                    {t("Prices")}
                  </button>
                  <button
                    type="button"
                    className="secondary"
                    disabled={Boolean(delivery) || entering || row.status !== "active"}
                    onClick={() => void enter(String(row.id))}
                  >
                    {t("Enter agency")}
                  </button>
                  <button
                    type="button"
                    className="secondary"
                    disabled={!row.invite_url || row.status !== "active"}
                    onClick={() => void copyInvite(row.invite_url, String(row.id))}
                  >
                    {t(copied === String(row.id) ? "Copied" : "Copy invitation link")}
                  </button>
                </div>
              ),
            },
          ]}
        />
      )}
      {query.data?.items.length === 0 && (
        <p className="muted">
          {t("Create an agency to issue its operator account and invitation link.")}
        </p>
      )}
      <Pager
        nextCursor={query.data?.meta?.next_cursor}
        hasPrevious={Boolean(cursor)}
        onNext={() => setCursor(query.data?.meta?.next_cursor ?? "")}
        onReset={() => setCursor("")}
      />
    </>
  );
}
