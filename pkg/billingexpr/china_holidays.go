package billingexpr

import "time"

var chinaStandardTime = time.FixedZone("Asia/Shanghai", 8*60*60)

// chinaPublicHolidayRanges contains the published mainland China public-holiday
// windows used by the DeepSeek time-of-day price rule. Weekend make-up workdays
// are intentionally not included: DeepSeek defines peak hours by Monday-Friday.
// Keep this table small and explicit so a calendar update is reviewable.
// Coverage: 2024-2026. Update after each year's official calendar is published.
var chinaPublicHolidayRanges = map[int][][2]time.Time{
	2024: {
		{date(2024, time.January, 1), date(2024, time.January, 1)},
		{date(2024, time.February, 10), date(2024, time.February, 17)},
		{date(2024, time.April, 4), date(2024, time.April, 6)},
		{date(2024, time.May, 1), date(2024, time.May, 5)},
		{date(2024, time.June, 8), date(2024, time.June, 10)},
		{date(2024, time.September, 15), date(2024, time.September, 17)},
		{date(2024, time.October, 1), date(2024, time.October, 7)},
	},
	2025: {
		{date(2025, time.January, 1), date(2025, time.January, 1)},
		{date(2025, time.January, 28), date(2025, time.February, 4)},
		{date(2025, time.April, 4), date(2025, time.April, 6)},
		{date(2025, time.May, 1), date(2025, time.May, 5)},
		{date(2025, time.May, 31), date(2025, time.June, 2)},
		{date(2025, time.October, 1), date(2025, time.October, 8)},
	},
	2026: {
		{date(2026, time.January, 1), date(2026, time.January, 3)},
		{date(2026, time.February, 15), date(2026, time.February, 23)},
		{date(2026, time.April, 4), date(2026, time.April, 6)},
		{date(2026, time.May, 1), date(2026, time.May, 5)},
		{date(2026, time.June, 19), date(2026, time.June, 21)},
		{date(2026, time.September, 25), date(2026, time.September, 27)},
		{date(2026, time.October, 1), date(2026, time.October, 7)},
	},
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, chinaStandardTime)
}

func isChinaPublicHoliday(now time.Time) bool {
	local := now.In(chinaStandardTime)
	for _, holiday := range chinaPublicHolidayRanges[local.Year()] {
		if !local.Before(holiday[0]) && !local.After(holiday[1].Add(24*time.Hour-time.Nanosecond)) {
			return true
		}
	}
	return false
}
