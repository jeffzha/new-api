export function getAgencyCenterUrl(platformLogin = false): string {
  const configured = import.meta.env.VITE_AGENCY_CENTER_URL?.trim()
  const base =
    configured ||
    (import.meta.env.DEV
      ? `${window.location.protocol}//${window.location.hostname}:3202/agency/`
      : new URL('/agency/', window.location.origin).href)
  const target = new URL(base, window.location.origin)
  if (platformLogin) {
    target.searchParams.set('platform_login', '1')
  }
  return target.href
}

export function shouldAutoEnterAgencyCenter(
  isSuperAdmin: boolean,
  mode?: string,
  agencySignedOut?: boolean
): boolean {
  return isSuperAdmin && mode === 'agency' && agencySignedOut !== true
}
