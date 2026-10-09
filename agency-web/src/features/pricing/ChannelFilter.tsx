import { useTranslation } from "react-i18next";

export type ChannelFilterOption = { id: string; name: string; models: number };

export function ChannelGlyph() {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <rect x="3" y="4" width="18" height="7" rx="2" />
      <rect x="3" y="13" width="18" height="7" rx="2" />
      <path d="M7 7.5h.01M7 16.5h.01" />
    </svg>
  );
}

export function ChannelTags(props: { names: { id: string | number; name: string }[] }) {
  const { t } = useTranslation();
  if (!props.names.length) return <span className="muted">{t("Channel unavailable")}</span>;
  return (
    <div className="channel-tags">
      {props.names.map((channel) => (
        <span key={channel.id}>
          <span className="channel-filter-icon" aria-hidden="true">
            <ChannelGlyph />
          </span>
          {channel.name}
        </span>
      ))}
    </div>
  );
}
