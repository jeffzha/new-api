import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";

export type ChannelFilterOption = { id: string; name: string; models: number };

/** One physical table row per channel keeps wrapped names aligned with their cost. */
export function ChannelPricingRows(props: {
  channels: { id: string | number; name: string; cost: ReactNode }[];
  leading: ReactNode[];
  trailing: ReactNode[];
  emptyCost?: ReactNode;
  className?: string;
}) {
  const { t } = useTranslation();
  const channels = props.channels.length
    ? props.channels
    : [
        {
          id: "unavailable",
          name: t("Channel unavailable"),
          cost: props.emptyCost ?? t("Channel unavailable"),
        },
      ];
  return (
    <>
      {channels.map((channel, index) => (
        <tr key={channel.id} className={props.className}>
          {index === 0 &&
            props.leading.map((cell, position) => (
              <td key={position} rowSpan={channels.length}>
                {cell}
              </td>
            ))}
          <td className="pricing-channel-cell">{channel.name}</td>
          <td className="pricing-cost-cell">{channel.cost}</td>
          {index === 0 &&
            props.trailing.map((cell, position) => (
              <td key={position} rowSpan={channels.length}>
                {cell}
              </td>
            ))}
        </tr>
      ))}
    </>
  );
}
