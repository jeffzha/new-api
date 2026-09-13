import type { Locale } from "../../lib/types";

const rows = [
  [
    "Total earned commission",
    "累计获得",
    "累計獲得",
    "Commissions acquises",
    "獲得累計",
    "Всего начислено",
    "Tổng hoa hồng đã nhận",
  ],
  [
    "Reversed commission",
    "已退佣",
    "已退佣",
    "Commissions annulées",
    "取消済みコミッション",
    "Отменённая комиссия",
    "Hoa hồng đã hoàn",
  ],
  [
    "Net total commission",
    "净总佣金",
    "淨總佣金",
    "Total net des commissions",
    "純コミッション累計",
    "Итоговая комиссия",
    "Tổng hoa hồng ròng",
  ],
  [
    "Withdrawn commission",
    "已提现佣金",
    "已提現佣金",
    "Commissions versées",
    "出金済みコミッション",
    "Выплаченная комиссия",
    "Hoa hồng đã rút",
  ],
  [
    "Net total commission equals total earned minus reversed commission. Withdrawn and locked amounts are included in this total. Each currency is calculated separately.",
    "净总佣金 = 累计获得 − 已退佣，包含已提现和冻结中的金额。不同币种分别统计。",
    "淨總佣金 = 累計獲得 − 已退佣，包含已提現和凍結中的金額。不同幣別分別統計。",
    "Le total net correspond aux commissions acquises moins les annulations. Il inclut les montants versés et bloqués. Chaque devise est comptabilisée séparément.",
    "純累計は獲得累計から取消分を差し引いた金額です。出金済みと保留中の金額を含み、通貨別に集計します。",
    "Итоговая комиссия равна начисленной за вычетом отменённой. Она включает выплаченные и заблокированные суммы. Каждая валюта учитывается отдельно.",
    "Tổng ròng bằng hoa hồng đã nhận trừ hoa hồng đã hoàn, bao gồm khoản đã rút và đang khóa. Mỗi loại tiền được tính riêng.",
  ],
] as const;

const locales: Locale[] = ["en", "zh", "zh-TW", "fr", "ja", "ru", "vi"];
export const reportMessages = Object.fromEntries(
  locales.map((locale, index) => [
    locale,
    Object.fromEntries(rows.map((row) => [row[0], row[index]])),
  ]),
) as Record<Locale, Record<string, string>>;
