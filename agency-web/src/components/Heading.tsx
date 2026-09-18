import type { ReactNode } from "react";

export type HeadingIconName =
  | "overview"
  | "customers"
  | "ledger"
  | "audit"
  | "status"
  | "sync"
  | "accounts"
  | "exports"
  | "invitation"
  | "pricing"
  | "withdrawals"
  | "agency";

export type ActionIconName =
  | "refresh"
  | "plus"
  | "close"
  | "save"
  | "download"
  | "play"
  | "search"
  | "eye"
  | "check";

function Icon({ name }: { name: HeadingIconName }) {
  const common = {
    width: 17,
    height: 17,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.8,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    "aria-hidden": true,
  };
  switch (name) {
    case "overview":
      return <svg {...common}><rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" /><rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" /></svg>;
    case "customers":
      return <svg {...common}><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75" /></svg>;
    case "ledger":
      return <svg {...common}><path d="M6 3h12a2 2 0 0 1 2 2v16H6a3 3 0 0 1-3-3V6a3 3 0 0 1 3-3ZM3 18a3 3 0 0 1 3-3h14M8 7h7M8 11h5" /></svg>;
    case "audit":
      return <svg {...common}><path d="M4 4h16v16H4zM8 8h8M8 12h8M8 16h5" /></svg>;
    case "status":
      return <svg {...common}><path d="M3 12h4l2.2-6 4.2 12 2.2-6H21" /></svg>;
    case "sync":
      return <svg {...common}><path d="M20 11a8 8 0 0 0-14.7-4L3 10M3 5v5h5M4 13a8 8 0 0 0 14.7 4L21 14M21 19v-5h-5" /></svg>;
    case "accounts":
      return <svg {...common}><rect x="3" y="5" width="18" height="14" rx="2" /><path d="M3 10h18M7 15h4" /></svg>;
    case "exports":
      return <svg {...common}><path d="M12 3v12M7 10l5 5 5-5M5 21h14" /><path d="M5 3h4M15 3h4" /></svg>;
    case "invitation":
      return <svg {...common}><rect x="3" y="3" width="18" height="18" rx="4" /><path d="M12 7v10M7 12h10" /></svg>;
    case "pricing":
      return <svg {...common}><path d="M12 3v18M17 7.5c0-1.7-1.9-3-5-3S7 5.8 7 7.5 8.9 10 12 10s5 1.3 5 3-1.9 3-5 3-5-1.3-5-3" /></svg>;
    case "withdrawals":
      return <svg {...common}><path d="M12 3v12M7 10l5 5 5-5M5 21h14" /></svg>;
    case "agency":
      return <svg {...common}><path d="M4 21v-8h16v8M7 13V4h10v9M2 21h20M9 8h6M9 11h6" /></svg>;
  }
}

export function ActionIcon({ name }: { name: ActionIconName }) {
  const common = {
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.8,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    "aria-hidden": true,
  };
  const paths: Record<ActionIconName, ReactNode> = {
    refresh: <path d="M20 11a8 8 0 0 0-14.7-4L3 10M3 5v5h5M4 13a8 8 0 0 0 14.7 4L21 14M21 19v-5h-5" />,
    plus: <path d="M12 5v14M5 12h14" />,
    close: <path d="M6 6l12 12M18 6 6 18" />,
    save: <path d="m5 12 4 4L19 6" />,
    download: <path d="M12 3v12M7 10l5 5 5-5M5 21h14" />,
    play: <path d="m8 5 11 7-11 7V5Z" />,
    search: <path d="m21 21-4.3-4.3M10.8 18a7.2 7.2 0 1 1 0-14.4 7.2 7.2 0 0 1 0 14.4Z" />,
    eye: <path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z" />,
    check: <path d="m5 12 4 4L19 6" />,
  };
  return <svg className="action-icon" {...common}>{paths[name]}</svg>;
}

export function PageHeading(props: {
  icon: HeadingIconName;
  children: ReactNode;
  level?: 2 | 3;
  className?: string;
}) {
  const Tag = props.level === 3 ? "h3" : "h2";
  return (
    <Tag className={["page-heading", props.className].filter(Boolean).join(" ")}>
      <span className={`heading-icon heading-icon-${props.icon}`}><Icon name={props.icon} /></span>
      <span>{props.children}</span>
    </Tag>
  );
}
