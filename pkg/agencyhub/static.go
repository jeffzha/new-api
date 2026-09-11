package agencyhub

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const agencyIndexHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>代理商中心</title>
<style>body{font:15px system-ui,sans-serif;background:#f5f7fa;color:#1f2937;margin:0}main{max-width:960px;margin:48px auto;padding:24px;background:#fff;border:1px solid #e5e7eb;border-radius:8px}h1{margin-top:0}label{display:block;margin:12px 0 4px}input,button{font:inherit;padding:9px 12px;border:1px solid #cbd5e1;border-radius:6px}button{cursor:pointer;background:#111827;color:#fff}.hidden{display:none}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px}.metric{padding:16px;background:#f8fafc;border:1px solid #e2e8f0;border-radius:6px}.muted{color:#64748b}</style></head>
<body><main><section id="login"><h1>代理商中心</h1><p class="muted">使用代理商账号登录</p><form id="login-form"><label>账号</label><input id="username" autocomplete="username" required><label>密码</label><input id="password" type="password" autocomplete="current-password" required><button type="submit">登录</button></form><p id="login-error" role="alert"></p></section>
<section id="dashboard" class="hidden"><h1>代理商中心</h1><p id="agency-name" class="muted"></p><div class="grid"><div class="metric"><div class="muted">可提现余额</div><strong id="balance">-</strong></div><div class="metric"><div class="muted">累计佣金</div><strong id="earned">-</strong></div><div class="metric"><div class="muted">客户数</div><strong id="customers">-</strong></div></div><p><button id="logout">退出登录</button></p></section></main>
<script>(()=>{const p='/agency/api/v1';const $=id=>document.getElementById(id);const csrf=()=>document.cookie.split('; ').find(x=>x.startsWith('agency_csrf='))?.split('=')[1]||'';async function api(path,opt={}){const h={'Content-Type':'application/json',...(opt.headers||{})};if(opt.method&&opt.method!=='GET')h['X-CSRF-Token']=csrf();const r=await fetch(p+path,{credentials:'include',...opt,headers:h});return r.json()}async function load(){const m=await api('/auth/me');if(!m.success)return;$('login').classList.add('hidden');$('dashboard').classList.remove('hidden');const s=await api('/commissions/summary');const c=await api('/customers');$('agency-name').textContent=m.data.username||'代理商';$('customers').textContent=c.data?.total??'-';const b=s.data?.items?.[0];$('balance').textContent=b?String(b.available_micros):'0';$('earned').textContent=b?String(b.earned_micros):'0'}$('login-form').onsubmit=async e=>{e.preventDefault();const n=await api('/auth/nonce');const r=await api('/auth/login',{method:'POST',body:JSON.stringify({username:$('username').value,password:$('password').value,nonce:n.data?.nonce})});if(r.success)load();else $('login-error').textContent=r.error?.message||'登录失败'};$('logout').onclick=async()=>{await api('/auth/logout',{method:'POST',body:'{}'});location.reload()};load()})()</script></body></html>`

func (a *App) index(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusOK, agencyIndexHTML)
}
