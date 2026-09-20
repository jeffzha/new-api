import type { ReactNode } from "react";

export type Locale = "en" | "zh" | "zh-TW" | "fr" | "ja" | "ru" | "vi";
export type Messages = Record<Locale, Record<string, string>>;
export interface Identity {
  actor_type: "root" | "agency_operator";
  actor_id: number;
  agency_id?: number;
  acting_agency?: {
    display_name: string;
    operator_username: string;
  };
  username?: string;
  must_change_password: boolean;
}
export interface Page<T> {
  items: T[];
  total: number;
  meta?: { next_cursor?: string };
}
export interface MutationRequest {
  path: string;
  method?: "POST" | "PATCH";
  body: unknown;
  action?: string;
  objectId?: string;
  title?: string;
}
export type Mutate = <T = unknown>(request: MutationRequest) => Promise<T>;
export interface Column<T> {
  key: string;
  label: string;
  render?: (row: T) => ReactNode;
}
