import { StrictMode, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { createRoot } from 'react-dom/client'
import './style.css'

const messages = {
  'zh-CN': { title: '代理商中心', loginHint: '使用独立代理商账号登录', username: '账号', password: '密码', login: '登录', logout: '退出登录', balance: '可提现余额', earned: '累计佣金', customers: '客户数', loginFailed: '登录失败', overview: '概览', customerList: '客户', ledger: '佣金流水', withdrawals: '提现记录', pricing: '销售价格', noData: '暂无数据', status: '状态', amount: '金额', model: '模型', quota: '额度', time: '时间' },
  en: { title: 'Agency Center', loginHint: 'Sign in with your agency account', username: 'Username', password: 'Password', login: 'Sign in', logout: 'Sign out', balance: 'Available to withdraw', earned: 'Total commission', customers: 'Customers', loginFailed: 'Sign-in failed', overview: 'Overview', customerList: 'Customers', ledger: 'Commission ledger', withdrawals: 'Withdrawals', pricing: 'Sales pricing', noData: 'No data', status: 'Status', amount: 'Amount', model: 'Model', quota: 'Quota', time: 'Time' },
} as const
type Locale = keyof typeof messages
const t = (locale: Locale, key: keyof typeof messages.en) => messages[locale][key]

type ApiResult = { success: boolean; data?: any; error?: { message?: string } }
async function api(path: string, init?: RequestInit): Promise<ApiResult> {
  const headers = new Headers(init?.headers)
  headers.set('Content-Type', 'application/json')
  if (init?.method && init.method !== 'GET') headers.set('X-CSRF-Token', document.cookie.split('; ').find((v) => v.startsWith('agency_csrf='))?.split('=')[1] ?? '')
  if (init?.method && init.method !== 'GET' && !path.startsWith('/auth/')) headers.set('Idempotency-Key', crypto.randomUUID())
  const response = await fetch(`/agency/api/v1${path}`, { ...init, headers, credentials: 'include' })
  return response.json() as Promise<ApiResult>
}

function App() {
  const locale: Locale = navigator.language.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
  const [loggedIn, setLoggedIn] = useState(false)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [summary, setSummary] = useState<any>(null)
  const [customerCount, setCustomerCount] = useState('-')
  const refresh = async () => {
    const me = await api('/auth/me')
    if (!me.success) return
    setLoggedIn(true)
  const [commission, customers] = await Promise.all([api('/commissions/summary'), api('/customers')])
    setSummary(commission.data?.items?.[0] ?? null)
    setCustomerCount(String(customers.data?.total ?? '-'))
  }
  useEffect(() => { void refresh() }, [])
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setError('')
    const nonce = await api('/auth/nonce')
    const result = await api('/auth/login', { method: 'POST', body: JSON.stringify({ username, password, nonce: nonce.data?.nonce }) })
    if (!result.success) { setError(result.error?.message ?? t(locale, 'loginFailed')); return }
    await refresh()
  }
  if (!loggedIn) return <main className="shell"><section className="auth"><p className="eyebrow">AGENCY</p><h1>{t(locale, 'title')}</h1><p className="muted">{t(locale, 'loginHint')}</p><form onSubmit={submit}><label>{t(locale, 'username')}<input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required /></label><label>{t(locale, 'password')}<input value={password} onChange={(e) => setPassword(e.target.value)} type="password" autoComplete="current-password" required /></label><button type="submit">{t(locale, 'login')}</button><p className="error" role="alert">{error}</p></form></section></main>
  return <Dashboard locale={locale} summary={summary} customerCount={customerCount} onLogout={async () => { await api('/auth/logout', { method: 'POST', body: '{}' }); setLoggedIn(false) }} />
}

function Dashboard({ locale, summary, customerCount, onLogout }: { locale: Locale; summary: any; customerCount: string; onLogout: () => Promise<void> }) {
  const [tab, setTab] = useState<'overview' | 'customers' | 'ledger' | 'withdrawals' | 'pricing'>('overview')
  const [data, setData] = useState<any>(null)
  useEffect(() => {
    const path = tab === 'customers' ? '/customers' : tab === 'ledger' ? '/commissions/ledger' : tab === 'withdrawals' ? '/withdrawals' : tab === 'pricing' ? '/pricing' : ''
    if (path) void api(path).then((result) => setData(result.data ?? null))
  }, [tab])
  const tabs: Array<[typeof tab, keyof typeof messages.en]> = [['overview', 'overview'], ['customers', 'customerList'], ['ledger', 'ledger'], ['withdrawals', 'withdrawals'], ['pricing', 'pricing']]
  return <main className="shell"><header><div><p className="eyebrow">AGENCY</p><h1>{t(locale, 'title')}</h1></div><button className="secondary" onClick={() => void onLogout()}>{t(locale, 'logout')}</button></header><nav className="tabs" aria-label="navigation">{tabs.map(([value, label]) => <button key={value} className={tab === value ? 'tab active' : 'tab'} onClick={() => { setTab(value); setData(null) }}>{t(locale, label)}</button>)}</nav>{tab === 'overview' && <section className="metrics"><article><span>{t(locale, 'balance')}</span><strong>{summary?.available_micros ?? 0}</strong></article><article><span>{t(locale, 'earned')}</span><strong>{summary?.earned_micros ?? 0}</strong></article><article><span>{t(locale, 'customers')}</span><strong>{customerCount}</strong></article></section>}{tab === 'customers' && <Table locale={locale} columns={['user_id', 'username', 'revision']} rows={data?.items ?? []} />}{tab === 'ledger' && <Table locale={locale} columns={['entry_type', 'amount_micros', 'currency_code', 'occurred_at_ms']} rows={data?.items ?? []} />}{tab === 'withdrawals' && <Table locale={locale} columns={['request_no', 'status', 'amount_micros', 'currency_code']} rows={data?.items ?? []} />}{tab === 'pricing' && <pre className="json">{data ? JSON.stringify(data, null, 2) : t(locale, 'noData')}</pre>}</main>
}

function Table({ locale, columns, rows }: { locale: Locale; columns: string[]; rows: any[] }) {
  if (rows.length === 0) return <section className="empty">{t(locale, 'noData')}</section>
  return <div className="table-wrap"><table><thead><tr>{columns.map((column) => <th key={column}>{column}</th>)}</tr></thead><tbody>{rows.map((row, index) => <tr key={row.id ?? row.user_id ?? index}>{columns.map((column) => <td key={column}>{String(row[column] ?? '-')}</td>)}</tr>)}</tbody></table></div>
}

createRoot(document.getElementById('root')!).render(<StrictMode><App /></StrictMode>)
