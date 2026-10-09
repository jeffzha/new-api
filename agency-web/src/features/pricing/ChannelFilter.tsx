import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";

export type ChannelFilterOption = { id: string; name: string; models: number };

/** Channel chip filter shared by the platform and agency pricing matrices. */
export function ChannelFilter(props: {
  channels: ChannelFilterOption[];
  selected: string[];
  onToggle: (id: string) => void;
  onClear: () => void;
}) {
  const { t } = useTranslation();
  if (!props.channels.length) return null;
  return (
    <div className="channel-filter" role="group" aria-label={t("Filter by channel")}>
      <div className="channel-filter-head">
        <span className="channel-filter-label">
          <ActionIcon name="search" />
          {t("Filter by channel")}
        </span>
        <button
          type="button"
          className="secondary button-icon compact-action"
          disabled={!props.selected.length}
          onClick={props.onClear}
        >
          <ActionIcon name="refresh" />
          {t("Clear filters")}
        </button>
      </div>
      <div className="channel-filter-chips">
        {props.channels.map((channel) => {
          const active = props.selected.includes(channel.id);
          return (
            <button
              key={channel.id}
              type="button"
              className={active ? "channel-chip active" : "channel-chip"}
              aria-pressed={active}
              onClick={() => props.onToggle(channel.id)}
            >
              <span className="channel-filter-icon" aria-hidden="true">
                <ChannelGlyph />
              </span>
              <span className="channel-chip-name">{channel.name}</span>
              <small>{channel.models}</small>
            </button>
          );
        })}
      </div>
    </div>
  );
}

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
