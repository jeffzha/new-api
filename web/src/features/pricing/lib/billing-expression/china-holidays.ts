// Mirrors pkg/billingexpr/china_holidays.go. Weekends that are make-up
// workdays are intentionally not added; peak hours still require Mon-Fri.
const HOLIDAYS: Record<number, readonly (readonly [number, number])[]> = {
  2024: [
    [101, 101],
    [210, 217],
    [404, 406],
    [501, 505],
    [608, 610],
    [915, 917],
    [1001, 1007],
  ],
  2025: [
    [101, 101],
    [128, 204],
    [404, 406],
    [501, 505],
    [531, 602],
    [1001, 1008],
  ],
  2026: [
    [101, 103],
    [215, 223],
    [404, 406],
    [501, 505],
    [619, 621],
    [925, 927],
    [1001, 1007],
  ],
}

export function isChinaPublicHoliday(now: Date): boolean {
  const local = new Date(now.getTime() + 8 * 60 * 60 * 1000)
  const date = (local.getUTCMonth() + 1) * 100 + local.getUTCDate()
  // Unknown years match the server's empty-calendar behavior.
  return (HOLIDAYS[local.getUTCFullYear()] ?? []).some(
    ([start, end]) => date >= start && date <= end
  )
}
