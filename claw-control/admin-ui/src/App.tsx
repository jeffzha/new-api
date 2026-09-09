import { lazy, Suspense, useEffect, useState } from 'react'

import { useI18n } from './i18n'

const DashboardPage = lazy(() => import('./pages/DashboardPage'))
const CustomersPage = lazy(() => import('./pages/CustomersPage'))
const CustomerPage = lazy(() => import('./pages/CustomerPage'))
const PlansPage = lazy(() => import('./pages/PlansPage'))
const CredentialsPage = lazy(() => import('./pages/CredentialsPage'))
const UsagePage = lazy(() => import('./pages/UsagePage'))
const AuditsPage = lazy(() => import('./pages/AuditsPage'))
const GovernancePage = lazy(() => import('./pages/GovernancePage'))
const AgentStorePage = lazy(() => import('./pages/AgentStorePage'))

type Route = { section: string; customerId?: number }

function readRoute(): Route {
  const value = window.location.hash.replace(/^#\/?/, '')
  const parts = value.split('/').filter(Boolean)
  if (parts[0] === 'customers' && /^\d+$/.test(parts[1] ?? '')) return { section: 'customer', customerId: Number(parts[1]) }
  return { section: parts[0] || 'dashboard' }
}

export function App() {
  const { locale, setLocale, t } = useI18n()
  const [route, setRoute] = useState(readRoute)
  const nav = [
    ['dashboard', t('nav.dashboard')],
    ['customers', t('nav.customers')],
    ['plans', t('nav.plans')],
    ['credentials', t('nav.credentials')],
    ['usage', t('nav.usage')],
    ['audits', t('nav.audits')],
    ['governance', t('nav.governance')],
    ['agent-store', t('nav.agentStore')],
  ] as const

  useEffect(() => {
    const update = () => setRoute(readRoute())
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])

  let page
  switch (route.section) {
    case 'customers': page = <CustomersPage />; break
    case 'customer': page = <CustomerPage customerId={route.customerId ?? 0} />; break
    case 'plans': page = <PlansPage />; break
    case 'credentials': page = <CredentialsPage />; break
    case 'usage': page = <UsagePage />; break
    case 'audits': page = <AuditsPage />; break
    case 'governance': page = <GovernancePage />; break
    case 'agent-store': page = <AgentStorePage />; break
    default: page = <DashboardPage />
  }

  return <div className="app-shell">
    <a className="skip-link" href="#main">{t('common.skip')}</a>
    <aside className="sidebar">
      <div className="brand"><strong>{t('app.name')}</strong><span>{t('app.description')}</span></div>
      <nav aria-label={t('app.name')}>
        {nav.map(([key, label]) => <a key={key} href={`#/${key}`} aria-current={route.section === key || (key === 'customers' && route.section === 'customer') ? 'page' : undefined}>{label}</a>)}
      </nav>
      <label className="language"><span>{t('common.language')}</span><select value={locale} onChange={(event) => setLocale(event.target.value === 'en' ? 'en' : 'zh')}><option value="zh">中文</option><option value="en">English</option></select></label>
    </aside>
    <main id="main" tabIndex={-1}><Suspense fallback={<p className="state" role="status">{t('common.loading')}</p>}>{page}</Suspense></main>
  </div>
}
