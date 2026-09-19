import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { Field, ErrorNotice } from "../../components/ui";
import { ActionIcon } from "../../components/Heading";
import { useQuery } from "../../lib/client";
import type { PricingDraft } from "./types";

export function PolicyFields(props: {
  draft: PricingDraft;
  root: boolean;
  disabled?: boolean;
  onChange: (draft: PricingDraft) => void;
}) {
  const { t } = useTranslation();
  const listId = useId();
  const [search, setSearch] = useState("");
  const [model, setModel] = useState("");
  const models = useQuery<{ items: string[] }>("/models?limit=200&q=" + encodeURIComponent(search));
  function updateOverride(index: number, key: "settlement" | "sales", value: string) {
    props.onChange({
      ...props.draft,
      overrides: props.draft.overrides.map((row, i) =>
        i === index ? { ...row, [key]: value } : row,
      ),
    });
  }
  return (
    <fieldset disabled={props.disabled} className="policy-fields">
      <legend>{t("Price coefficients")}</legend>
      <div className="form-grid">
        <Field label={t("Default settlement coefficient")}>
          <input
            inputMode="decimal"
            value={props.draft.settlement}
            readOnly={!props.root}
            onChange={(e) => props.onChange({ ...props.draft, settlement: e.target.value })}
            required
          />
        </Field>
        <Field label={t("Default sales coefficient")}>
          <input
            inputMode="decimal"
            value={props.draft.sales}
            onChange={(e) => props.onChange({ ...props.draft, sales: e.target.value })}
            required
          />
        </Field>
        <Field label={t("Minimum spread")}>
          <input
            inputMode="decimal"
            value={props.draft.spread}
            readOnly={!props.root}
            onChange={(e) => props.onChange({ ...props.draft, spread: e.target.value })}
            required
          />
        </Field>
        <Field label={t("Sales coefficient cap")}>
          <input
            inputMode="decimal"
            value={props.draft.cap}
            readOnly={!props.root}
            onChange={(e) => props.onChange({ ...props.draft, cap: e.target.value })}
            required
          />
        </Field>
      </div>
      {!props.root && (
        <p className="muted">{t("Settlement and minimum spread are set by the administrator.")}</p>
      )}
      <h3>{t("Model price overrides")}</h3>
      <p className="muted">
        {t("Leave an override blank to inherit the default. Zero is an explicit value.")}
      </p>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{t("Public model")}</th>
              <th>{t("Settlement coefficient")}</th>
              <th>{t("Sales coefficient")}</th>
              <th>{t("Actions")}</th>
            </tr>
          </thead>
          <tbody>
            {props.draft.overrides.map((row, index) => (
              <tr key={row.model}>
                <td>{row.model}</td>
                <td>
                  <input
                    aria-label={t("Settlement coefficient") + ": " + row.model}
                    inputMode="decimal"
                    readOnly={!props.root}
                    value={row.settlement}
                    placeholder={t("Inherit")}
                    onChange={(e) => updateOverride(index, "settlement", e.target.value)}
                  />
                </td>
                <td>
                  <input
                    aria-label={t("Sales coefficient") + ": " + row.model}
                    inputMode="decimal"
                    value={row.sales}
                    placeholder={t("Inherit")}
                    onChange={(e) => updateOverride(index, "sales", e.target.value)}
                  />
                </td>
                <td className="table-action-cell">
                  <button
                    type="button"
                    className="secondary button-icon compact-action"
                    onClick={() =>
                      props.onChange({
                        ...props.draft,
                        overrides: props.draft.overrides.filter((_, i) => i !== index),
                      })
                    }
                  >
                    <ActionIcon name="close" />
                    {t("Remove override")}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {props.draft.overrides.length === 0 && (
        <p className="muted">
          {t("All models use the default coefficients. Add an override for a specific model.")}
        </p>
      )}
      <div className="model-search-row">
        <Field label={t("Search public models")}>
          <input value={model} list={listId} onChange={(e) => setModel(e.target.value)} />
        </Field>
        <datalist id={listId}>
          {(models.data?.items ?? []).map((name) => (
            <option key={name} value={name} />
          ))}
        </datalist>
        <button
          type="button"
          className="secondary button-icon"
          onClick={() => setSearch(model)}
          disabled={models.loading}
        >
          <ActionIcon name="search" />
          {t("Search")}
        </button>
        <button
          type="button"
          className="secondary button-icon"
          disabled={
            !model ||
            !models.data?.items.includes(model) ||
            props.draft.overrides.some((row) => row.model === model) ||
            props.draft.overrides.length >= 1000
          }
          onClick={() => {
            props.onChange({
              ...props.draft,
              overrides: [...props.draft.overrides, { model, settlement: "", sales: "" }],
            });
            setModel("");
          }}
        >
          <ActionIcon name="plus" />
          {t("Add override")}
        </button>
      </div>
      <ErrorNotice error={models.error} />
    </fieldset>
  );
}
