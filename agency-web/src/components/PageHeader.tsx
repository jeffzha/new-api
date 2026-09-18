import type { ReactNode } from "react";
import { PageHeading, type HeadingIconName } from "./Heading";

export function PageHeader(props: {
  icon: HeadingIconName;
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="page-header">
      <div className="page-header-copy">
        <PageHeading icon={props.icon}>{props.title}</PageHeading>
        {props.description && <p className="page-header-description muted">{props.description}</p>}
      </div>
      {props.actions && <div className="page-header-actions">{props.actions}</div>}
    </div>
  );
}
