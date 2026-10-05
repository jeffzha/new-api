import type { Locale } from "./types";

export const agencyLanguageStorageKey = "agency-language";

const agencyLocales: readonly Locale[] = [
  "en",
  "zh",
  "zh-TW",
  "fr",
  "ja",
  "ru",
  "vi",
];

export function resolveAgencyLocale(savedLanguage: string | null): Locale {
  if (savedLanguage && agencyLocales.includes(savedLanguage as Locale)) {
    return savedLanguage as Locale;
  }
  return "en";
}
