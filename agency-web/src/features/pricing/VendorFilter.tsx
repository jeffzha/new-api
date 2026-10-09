import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import type { ChannelFilterOption } from "./ChannelFilter";
import { vendorMark } from "./vendorMarks";
import type { VendorFilterOption } from "./vendorOptions";

/** Brand mark of a vendor, or a monogram when no logo is available. */
export function VendorMark(props: { icon?: string; name: string }) {
  const mark = vendorMark(props.icon);
  if (mark) return <span className="vendor-mark">{mark}</span>;
  const initial = Array.from(props.name.trim())[0] ?? "?";
  return (
    <span className="vendor-mark vendor-mark-monogram" aria-hidden="true">
      {initial.toUpperCase()}
    </span>
  );
}

/** Provider tabs mirroring the model square so agencies browse by publisher. */
export function VendorTabs(props: {
  vendors: VendorFilterOption[];
  selected: string;
  total: number;
  onSelect: (key: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="vendor-tabs" role="tablist" aria-label={t("Providers")}>
      <button
        type="button"
        role="tab"
        aria-selected={props.selected === "all"}
        className={props.selected === "all" ? "vendor-tab active" : "vendor-tab"}
        onClick={() => props.onSelect("all")}
      >
        <span className="vendor-mark vendor-mark-all" aria-hidden="true">
          <ActionIcon name="grid" />
        </span>
        <span className="vendor-tab-name">{t("All")}</span>
        <span className="vendor-tab-count">{props.total}</span>
      </button>
      {props.vendors.map((vendor) => (
        <button
          key={vendor.key}
          type="button"
          role="tab"
          aria-selected={props.selected === vendor.key}
          className={props.selected === vendor.key ? "vendor-tab active" : "vendor-tab"}
          onClick={() => props.onSelect(vendor.key)}
        >
          <VendorMark icon={vendor.icon} name={vendor.name} />
          <span className="vendor-tab-name">{vendor.name}</span>
          <span className="vendor-tab-count">{vendor.models}</span>
        </button>
      ))}
    </div>
  );
}

/** Compact channel picker so channel filtering no longer crowds the page. */
export function ChannelFilterMenu(props: {
  channels: ChannelFilterOption[];
  selected: string[];
  onToggle: (id: string) => void;
  onClear: () => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const container = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    function closeOnOutside(event: PointerEvent) {
      if (!container.current?.contains(event.target as Node)) setOpen(false);
    }
    function closeOnEscape(event: KeyboardEvent) {
      if (event.key === "Escape") setOpen(false);
    }
    document.addEventListener("pointerdown", closeOnOutside);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOnOutside);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [open]);
  if (!props.channels.length) return null;
  const active = props.selected.length > 0;
  return (
    <div className="filter-menu" ref={container}>
      <button
        type="button"
        className={active ? "secondary button-icon filter-menu-trigger active" : "secondary button-icon filter-menu-trigger"}
        aria-expanded={open}
        aria-haspopup="true"
        onClick={() => setOpen((current) => !current)}
      >
        <ActionIcon name="filter" />
        {active ? `${t("Channels")} · ${props.selected.length}` : t("All channels")}
        <ActionIcon name="chevron-down" />
      </button>
      {open && (
        <div className="filter-menu-panel" role="group" aria-label={t("Filter by channel")}>
          <div className="filter-menu-head">
            <span>{t("Filter by channel")}</span>
            <button type="button" disabled={!active} onClick={props.onClear}>
              {t("Clear filters")}
            </button>
          </div>
          <div className="filter-menu-options">
            {props.channels.map((channel) => (
              <label key={channel.id} className="filter-menu-option">
                <input
                  type="checkbox"
                  checked={props.selected.includes(channel.id)}
                  onChange={() => props.onToggle(channel.id)}
                />
                <span>{channel.name}</span>
                <small>{channel.models}</small>
              </label>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
