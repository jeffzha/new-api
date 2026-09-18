import { cloneElement, isValidElement, useEffect, useId, useRef } from "react";
import type { ReactElement, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Column } from "../lib/types";
import { ActionIcon } from "./Heading";

export function Field(props: { label: string; children: ReactNode; hint?: string }) {
  const hintId = useId();
  const input =
    props.hint && isValidElement(props.children)
      ? cloneElement(props.children as ReactElement<{ "aria-describedby"?: string }>, {
          "aria-describedby": hintId,
        })
      : props.children;
  return (
    <div className="field">
      <label className="field">
        <span>{props.label}</span>
        {input}
      </label>
      {props.hint && (
        <small id={hintId} className="muted">
          {props.hint}
        </small>
      )}
    </div>
  );
}
export function ErrorNotice(props: { error?: unknown }) {
  const { t } = useTranslation();
  if (!props.error) return null;
  return (
    <p role="alert" className="error">
      {t(props.error instanceof Error ? props.error.message : String(props.error))}
    </p>
  );
}
export function Loading() {
  const { t } = useTranslation();
  return (
    <p role="status" className="muted">
      {t("Loading…")}
    </p>
  );
}
export function DataTable<T>(props: {
  rows: T[];
  columns: Column<T>[];
  rowKey?: (row: T) => string;
  empty?: string;
  actions?: (row: T) => ReactNode;
}) {
  const { t } = useTranslation();
  if (!props.rows.length) return <p className="empty">{props.empty || t("No records yet.")}</p>;
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {props.columns.map((col) => (
              <th key={col.key} scope="col">
                {t(col.label)}
              </th>
            ))}
            {props.actions && <th scope="col">{t("Actions")}</th>}
          </tr>
        </thead>
        <tbody>
          {props.rows.map((row, i) => (
            <tr key={props.rowKey?.(row) || i}>
              {props.columns.map((col) => (
                <td key={col.key}>
                  {col.render
                    ? col.render(row)
                    : String((row as Record<string, unknown>)[col.key] ?? "—")}
                </td>
              ))}
              {props.actions && (
                <td>
                  <div className="actions">{props.actions(row)}</div>
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
export function Dialog(props: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  busy?: boolean;
}) {
  const { t } = useTranslation();
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const dialog = ref.current;
    dialog?.showModal();
    return () => {
      dialog?.close();
      previous?.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-label={props.title}
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        if (!props.busy) props.onClose();
      }}
    >
      <div className="toolbar">
        <h2 id={titleId}>{props.title}</h2>
        <button type="button" className="secondary button-icon" disabled={props.busy} onClick={props.onClose}>
          <ActionIcon name="close" />
          {t("Close")}
        </button>
      </div>
      {props.children}
    </dialog>
  );
}
export function Pagination(props: {
  nextCursor?: string;
  onNext: () => void;
  onReset: () => void;
  hasPrevious?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <div className="actions">
      <button
        className="secondary button-icon"
        type="button"
        disabled={!props.hasPrevious}
        onClick={props.onReset}
      >
        <ActionIcon name="refresh" />
        {t("First page")}
      </button>
      <button
        className="secondary button-icon"
        type="button"
        disabled={!props.nextCursor}
        onClick={props.onNext}
      >
        <ActionIcon name="play" />
        {t("Next page")}
      </button>
    </div>
  );
}

export function formatMoney(value: string | number | undefined, currency = "CNY"): string {
  if (value === undefined || value === null) return "—";
  try {
    const amount = BigInt(value);
    const absolute = amount < 0n ? -amount : amount;
    const whole = (absolute / 1000000n).toLocaleString();
    const fraction = String(absolute % 1000000n)
      .padStart(6, "0")
      .replace(/0{1,4}$/, "");
    return `${currency} ${amount < 0n ? "-" : ""}${whole}.${fraction}`;
  } catch {
    return "—";
  }
}
export function formatTime(value?: number | string): string {
  if (!value || !Number.isFinite(Number(value))) return "—";
  const time = Number(value);
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai",
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(time < 1e12 ? time * 1000 : time));
}

export const Table = DataTable;
export const Pager = Pagination;
export function Time(props: { value?: number | string }) {
  return <time>{formatTime(props.value)}</time>;
}
export function Money(props: { value?: number | string; currency?: string }) {
  return <span>{formatMoney(props.value, props.currency)}</span>;
}
