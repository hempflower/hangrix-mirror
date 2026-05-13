// Lightweight relative-time formatter. Uses i18n keys under "repo.time.*".
// Caller passes the i18n t() function so we don't pull useI18n() outside of setup.
export function relativeTime(iso: string | undefined | null, t: (key: string, params?: Record<string, unknown>) => string): string {
  if (!iso) return '—'
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return '—'
  const diffSec = Math.max(0, Math.round((Date.now() - ts) / 1000))
  if (diffSec < 5) return t('repo.time.justNow')
  if (diffSec < 60) return t('repo.time.secondsAgo', { n: diffSec })
  const diffMin = Math.round(diffSec / 60)
  if (diffMin < 60) return t('repo.time.minutesAgo', { n: diffMin })
  const diffHr = Math.round(diffMin / 60)
  if (diffHr < 24) return t('repo.time.hoursAgo', { n: diffHr })
  const diffDay = Math.round(diffHr / 24)
  if (diffDay < 7) return t('repo.time.daysAgo', { n: diffDay })
  const diffWk = Math.round(diffDay / 7)
  if (diffWk < 5) return t('repo.time.weeksAgo', { n: diffWk })
  const diffMo = Math.round(diffDay / 30)
  if (diffMo < 12) return t('repo.time.monthsAgo', { n: diffMo })
  const diffYr = Math.round(diffDay / 365)
  return t('repo.time.yearsAgo', { n: diffYr })
}
