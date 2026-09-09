const BEIJING_OFFSET_MILLISECONDS = 8 * 60 * 60 * 1000
const LOCAL_DATE_TIME_PATTERN =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?$/

export function parseBeijingDateTimeLocal(value: FormDataEntryValue | null) {
  const raw = String(value ?? '').trim()
  if (raw === '') return undefined

  const match = LOCAL_DATE_TIME_PATTERN.exec(raw)
  if (!match) throw new RangeError('Invalid Beijing datetime-local value')

  const [, yearText, monthText, dayText, hourText, minuteText, secondText = '0', millisecondText = '0'] = match
  const year = Number(yearText)
  const month = Number(monthText)
  const day = Number(dayText)
  const hour = Number(hourText)
  const minute = Number(minuteText)
  const second = Number(secondText)
  const millisecond = Number(millisecondText.padEnd(3, '0'))

  const wallClock = new Date(0)
  wallClock.setUTCFullYear(year, month - 1, day)
  wallClock.setUTCHours(hour, minute, second, millisecond)
  if (
    wallClock.getUTCFullYear() !== year ||
    wallClock.getUTCMonth() !== month - 1 ||
    wallClock.getUTCDate() !== day ||
    wallClock.getUTCHours() !== hour ||
    wallClock.getUTCMinutes() !== minute ||
    wallClock.getUTCSeconds() !== second ||
    wallClock.getUTCMilliseconds() !== millisecond
  ) {
    throw new RangeError('Invalid Beijing datetime-local value')
  }

  return new Date(
    wallClock.valueOf() - BEIJING_OFFSET_MILLISECONDS
  ).toISOString()
}

export function toBeijingDateTimeLocal(value: Date) {
  if (Number.isNaN(value.valueOf())) {
    throw new RangeError('Invalid date')
  }
  return new Date(value.valueOf() + BEIJING_OFFSET_MILLISECONDS)
    .toISOString()
    .slice(0, 16)
}

export function formatBeijingDate(value?: string, locale = 'zh') {
  if (!value) return '—'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.valueOf())) return value

  return new Intl.DateTimeFormat(locale === 'zh' ? 'zh-CN' : 'en-US', {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone: 'Asia/Shanghai',
  }).format(parsed)
}
