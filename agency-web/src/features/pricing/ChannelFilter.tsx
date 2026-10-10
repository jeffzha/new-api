import { useTranslation } from "react-i18next";

export type ChannelFilterOption = { id: string; name: string; models: number };

export function ChannelTags(props: { names: { id: string | number; name: string }[] }) {
  const { t } = useTranslation();
  if (!props.names.length) return <span className="muted">{t("Channel unavailable")}</span>;
  return (
    <div className="channel-tags">
      {props.names.map((channel) => (
        <span key={channel.id}>
          <span className="channel-mark" aria-hidden="true">{Array.from(channel.name.trim())[0]?.toUpperCase() || "·"}</span>
          {channel.name}
        </span>
      ))}
    </div>
  );
}
