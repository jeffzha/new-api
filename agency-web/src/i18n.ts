import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import { coreMessages } from "./lib/messages";
import { agencyMessages } from "./features/agencies/messages";
import { pricingMessages } from "./features/pricing/messages";
import { financeMessages } from "./features/finance/messages";
import { exportMessages } from "./features/exports/messages";
import { customerMessages } from "./features/customers/messages";
import { reconciliationMessages } from "./features/reconciliation/messages";
import { reportMessages } from "./features/reports/messages";
import { invitationMessages } from "./features/invitations/messages";
import type { Locale } from "./lib/types";

const language = navigator.language.toLowerCase();
let locale = language.split("-")[0];
if (language === "zh-tw" || language === "zh-hk") {
  locale = "zh-TW";
} else if (language.startsWith("zh")) {
  locale = "zh";
}
const resources = Object.fromEntries(
  (Object.keys(coreMessages) as Locale[]).map((lang) => [
    lang,
    {
      translation: {
        ...coreMessages[lang],
        ...agencyMessages[lang],
        ...pricingMessages[lang],
        ...financeMessages[lang],
        ...exportMessages[lang],
        ...customerMessages[lang],
        ...reconciliationMessages[lang],
        ...reportMessages[lang],
        ...invitationMessages[lang],
      },
    },
  ]),
);

void i18n.use(initReactI18next).init({
  resources,
  lng: locale,
  supportedLngs: Object.keys(resources),
  fallbackLng: "en",
  interpolation: { escapeValue: false },
  returnNull: false,
});
i18n.on("languageChanged", (lang) => {
  document.documentElement.lang = lang;
});
document.documentElement.lang = i18n.language;
export default i18n;
