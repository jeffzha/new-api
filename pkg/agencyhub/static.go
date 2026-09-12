package agencyhub

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// agencyIndexHTML is served at the hub's base path. When AGENCY_HUB_PLATFORM_BASE_URL
// is configured the page automatically exchanges the browsing new-api Root
// session for a one-time SSO ticket (embedded hidden iframe -> postMessage ->
// callback), so an already-logged-in administrator opens the hub directly.
// The operator password form remains as a fallback.
const agencyIndexHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>代理商中心</title>
<style>body{font:15px system-ui,sans-serif;background:#f5f7fa;color:#1f2937;margin:0}main{max-width:960px;margin:48px auto;padding:24px;background:#fff;border:1px solid #e5e7eb;border-radius:8px}h1{margin-top:0}label{display:block;margin:12px 0 4px}input,button{font:inherit;padding:9px 12px;border:1px solid #cbd5e1;border-radius:6px}button{cursor:pointer;background:#111827;color:#fff}.hidden{display:none}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px}.metric{padding:16px;background:#f8fafc;border:1px solid #e2e8f0;border-radius:6px}.muted{color:#64748b}.sso{width:100%;margin-top:12px;background:#2563eb}.divider{display:flex;align-items:center;gap:12px;color:#94a3b8;margin:18px 0 4px}.divider:before,.divider:after{content:"";flex:1;height:1px;background:#e2e8f0}</style></head>
<body><main><section id="login"><h1>代理商中心</h1><p class="muted">使用代理商账号登录</p><button id="sso-btn" class="sso hidden">通过主平台账号一键进入</button><div class="divider" id="sso-divider"><span>或</span></div><form id="login-form"><label>账号</label><input id="username" autocomplete="username" required><label>密码</label><input id="password" type="password" autocomplete="current-password" required><button type="submit">登录</button></form><p id="login-error" role="alert"></p></section>
<section id="dashboard" class="hidden"><h1>代理商中心</h1><p id="agency-name" class="muted"></p><div class="grid"><div class="metric"><div class="muted">可提现余额</div><strong id="balance">-</strong></div><div class="metric"><div class="muted">累计佣金</div><strong id="earned">-</strong></div><div class="metric"><div class="muted">客户数</div><strong id="customers">-</strong></div></div><p><button id="logout">退出登录</button></p></section></main>
<script>
const CONFIG = __CONFIG__;
const p = CONFIG.base_path + '/api/v1';
const platformOrigin = (function(){try{return new URL(CONFIG.platform_base_url).origin;}catch(e){return '';}})();
const $ = id => document.getElementById(id);
const csrf = () => document.cookie.split('; ').find(x => x.startsWith('agency_csrf='))?.split('=')[1] || '';
let ssoState = null;
let ssoRunning = false;
async function api(path, opt = {}) {
  const h = { 'Content-Type': 'application/json', ...(opt.headers || {}) };
  if (opt.method && opt.method !== 'GET') h['X-CSRF-Token'] = csrf();
  const r = await fetch(p + path, { credentials: 'include', ...opt, headers: h });
  return r.json();
}
function showLogin() {
  $('login').classList.remove('hidden');
  $('dashboard').classList.add('hidden');
  if (CONFIG.platform_base_url) { $('sso-btn').classList.remove('hidden'); $('sso-divider').classList.remove('hidden'); }
}
function showDashboard(m) {
  $('login').classList.add('hidden');
  $('dashboard').classList.remove('hidden');
  $('agency-name').textContent = (m && m.username) || '代理商';
  return Promise.all([api('/commissions/summary'), api('/customers')]).then(results => {
    const s = results[0], c = results[1];
    $('customers').textContent = c.data?.total ?? '-';
    const item = s.data?.items?.[0];
    $('balance').textContent = item ? String(item.available_micros) : '0';
    $('earned').textContent = item ? String(item.earned_micros) : '0';
  }).catch(() => {});
}
function beginSSO() {
  if (ssoRunning || !CONFIG.platform_base_url) return;
  ssoRunning = true;
  return fetch(CONFIG.base_path + '/sso/start', { credentials: 'include' })
    .then(r => r.json())
    .then(body => {
      const d = body && body.data;
      if (!d || !d.state || !d.state_hash) throw new Error('sso_start_failed');
      ssoState = d.state;
      const iframe = document.createElement('iframe');
      iframe.style.display = 'none';
      iframe.src = CONFIG.platform_base_url + '/api/agency/sso?state_hash=' + encodeURIComponent(d.state_hash) + '&origin=' + encodeURIComponent(location.origin);
      document.body.appendChild(iframe);
    })
    .catch(() => { ssoRunning = false; showLogin(); $('login-error').textContent = 'SSO 启动失败，请使用账号密码登录'; });
}
function finishSSO(ticket) {
  fetch(CONFIG.base_path + '/sso/callback', { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ticket: ticket, state: ssoState }) })
    .then(r => r.json())
    .then(res => {
      if (res.success) { location.reload(); return; }
      ssoRunning = false;
      showLogin();
      $('login-error').textContent = (res.error && res.error.message) || 'SSO 登录失败';
    })
    .catch(() => { ssoRunning = false; showLogin(); });
}
window.addEventListener('message', e => {
  if (!platformOrigin || e.origin !== platformOrigin) return;
  const d = e.data || {};
  if (d.source !== 'new-api-agency-sso') return;
  if (!d.ok) { ssoRunning = false; showLogin(); $('login-error').textContent = (d && d.error) ? ('主平台 SSO：' + d.error) : '请在主平台登录后再试'; return; }
  if (d.ticket) finishSSO(d.ticket);
});
$('sso-btn').onclick = e => { e.preventDefault(); beginSSO(); };
$('login-form').onsubmit = e => {
  e.preventDefault();
  api('/auth/nonce').then(n => api('/auth/login', { method: 'POST', body: JSON.stringify({ username: $('username').value, password: $('password').value, nonce: n.data?.nonce }) }))
    .then(res => { if (res.success) showDashboard(res.data); else $('login-error').textContent = res.error?.message || '登录失败'; });
};
$('logout').onclick = () => { api('/auth/logout', { method: 'POST', body: '{}' }).then(() => location.reload()); };
(async () => {
  const m = await api('/auth/me');
  if (m.success) await showDashboard(m.data);
  else if (CONFIG.platform_base_url) await beginSSO();
  else showLogin();
})();
</script></body></html>`

func (a *App) index(c *gin.Context) {
	configJSON, err := common.Marshal(map[string]any{
		"platform_base_url": a.config.PlatformBaseURL,
		"base_path":         a.config.BasePath,
	})
	if err != nil {
		configJSON = []byte(`{"platform_base_url":"","base_path":"/agency"}`)
	}
	page := strings.Replace(agencyIndexHTML, "__CONFIG__", string(configJSON), 1)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	_, _ = c.Writer.WriteString(page)
}
