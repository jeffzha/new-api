package agencyhub

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// agencyWebDist is generated from agency-web by the release build. Keeping it
// embedded makes the sidecar self-contained and ensures /agency serves the
// same tested React artifact in production.
//
//go:embed webdist/* webdist/assets/*
var agencyWebDist embed.FS

// agencyIndexHTML is served at the hub's base path. When AGENCY_HUB_PLATFORM_BASE_URL
// is configured the page automatically exchanges the browsing new-api Root
// session for a one-time SSO ticket (embedded hidden iframe -> postMessage ->
// callback), so an already-logged-in administrator opens the hub directly.
// The operator password form remains as a fallback.
const agencyIndexHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>代理商中心</title>
<style>
:root{font:15px system-ui,-apple-system,"Segoe UI",sans-serif;color:#172033;background:#f4f6f8}*{box-sizing:border-box}body{margin:0}main{max-width:1220px;margin:0 auto;padding:28px 20px}.hidden{display:none!important}.muted{color:#667085}.error{color:#b42318;min-height:20px}.auth{max-width:420px;margin:8vh auto;background:#fff;border:1px solid #dfe4ea;border-radius:8px;padding:32px}h1{font-size:28px;margin:0 0 8px}h2{font-size:18px;margin:0 0 14px}label{display:grid;gap:6px;margin:14px 0;font-size:14px}input,select,textarea,button{font:inherit;border:1px solid #cbd5e1;border-radius:6px;padding:9px 11px}input,select,textarea{width:100%;background:#fff}button{cursor:pointer;background:#185adb;color:#fff;border-color:#185adb}button.secondary{background:#fff;color:#185adb}.danger{color:#b42318}.sso{width:100%;margin-top:12px}.divider{display:flex;align-items:center;gap:12px;color:#94a3b8;margin:18px 0 4px}.divider:before,.divider:after{content:"";flex:1;height:1px;background:#e2e8f0}.top{display:flex;justify-content:space-between;align-items:flex-start;gap:16px;margin-bottom:20px}.eyebrow{font-size:11px;letter-spacing:1.5px;color:#6b7280;margin:0 0 5px}.banner{padding:10px 12px;border:1px solid #b7c9ef;background:#eef4ff;border-radius:6px;margin-bottom:16px}.tabs{display:flex;gap:4px;overflow:auto;border-bottom:1px solid #dfe4ea;margin-bottom:20px}.tab{background:transparent;color:#475467;border:0;border-bottom:2px solid transparent;border-radius:0;white-space:nowrap}.tab.active{color:#185adb;border-bottom-color:#185adb}.metrics{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px}.metric,.panel{background:#fff;border:1px solid #dfe4ea;border-radius:8px;padding:18px}.metric span{display:block;color:#667085;font-size:13px;margin-bottom:12px}.metric strong{font-size:24px}.panel{margin-top:16px}.toolbar{display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin-bottom:14px}.toolbar input{max-width:240px}.table-wrap{overflow:auto;border:1px solid #dfe4ea;border-radius:7px}table{border-collapse:collapse;width:100%;font-size:13px;background:#fff}th,td{text-align:left;padding:11px 12px;border-bottom:1px solid #eef1f4;white-space:nowrap}th{font-size:12px;color:#667085;background:#f8fafc}.empty{padding:34px;text-align:center;color:#667085;background:#fff;border:1px solid #dfe4ea;border-radius:7px}.json{white-space:pre-wrap;overflow:auto;font-size:12px;background:#f8fafc;border:1px solid #e5e7eb;padding:12px;border-radius:6px;max-height:520px}.form-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.form-grid .full{grid-column:1/-1}.actions{display:flex;gap:8px;flex-wrap:wrap;margin-top:14px}.notice{padding:10px 12px;background:#fff8e6;border:1px solid #f0cf76;border-radius:6px;margin:10px 0}@media(max-width:760px){main{padding:20px 14px}.metrics,.form-grid{grid-template-columns:1fr}.top{display:block}.top button{margin-top:12px}}
</style></head>
<body><main>
<section id="login" class="auth"><p class="eyebrow">AGENCY</p><h1>代理商中心</h1><p class="muted">使用代理商独立账号登录，超级管理员可通过主平台会话进入。</p><button id="sso-btn" class="sso hidden">通过主平台账号一键进入</button><div class="divider" id="sso-divider"><span>或</span></div><form id="login-form"><label>账号<input id="username" autocomplete="username" required></label><label>密码<input id="password" type="password" autocomplete="current-password" required></label><button type="submit">登录</button><p id="login-error" class="error" role="alert"></p></form></section>
<section id="dashboard" class="hidden"><header class="top"><div><p class="eyebrow">AGENCY HUB</p><h1>代理商中心</h1><p id="identity" class="muted"></p></div><button id="logout" class="secondary">退出登录</button></header><div id="root-banner" class="banner hidden">当前为超级管理员代管模式。所有操作仍按超级管理员权限审计，不会冒充代理商账号。</div><nav id="tabs" class="tabs" aria-label="代理商中心导航"></nav><div id="content"></div></section>
</main><script>
const CONFIG=__CONFIG__,p=CONFIG.base_path+'/api/v1';
const platformOrigin=(()=>{try{return new URL(CONFIG.platform_base_url).origin}catch(e){return ''}})();
const $=id=>document.getElementById(id), csrf=()=>document.cookie.split('; ').find(x=>x.startsWith('agency_csrf='))?.split('=')[1]||'';
let ssoState=null,ssoRunning=false,me=null,currentTab='overview',selectedAgencyId='',dataCache={};
async function api(path,opt={}){const h={'Content-Type':'application/json',...(opt.headers||{})};if(opt.method&&opt.method!=='GET'){h['X-CSRF-Token']=csrf();if(!path.startsWith('/auth/'))h['Idempotency-Key']=crypto.randomUUID()}const r=await fetch(p+path,{credentials:'include',...opt,headers:h});let body;try{body=await r.json()}catch(e){body={success:false,error:{message:'服务返回无效响应'}}}if(!r.ok&&!body.error)body.error={message:'请求失败（HTTP '+r.status+'）'};return body}
function esc(v){return String(v??'').replace(/[&<>"']/g,x=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[x]))}
function json(v){return esc(JSON.stringify(v,null,2))}
function message(res){return res?.error?.message||'操作失败'}
function btn(label,action,cls=''){return '<button class="'+cls+'" data-action="'+esc(action)+'">'+esc(label)+'</button>'}
function table(columns,rows){if(!rows?.length)return '<div class="empty">暂无数据。请先完成配置或等待财务事件同步。</div>';return '<div class="table-wrap"><table><thead><tr>'+columns.map(x=>'<th>'+esc(x[1])+'</th>').join('')+'</tr></thead><tbody>'+rows.map((r,i)=>'<tr data-row-id="'+esc(r.id??r.user_id??i)+'">'+columns.map(x=>'<td>'+esc(r[x[0]]??'-')+'</td>').join('')+'</tr>').join('')+'</tbody></table></div>'}
function showLogin(){ $('login').classList.remove('hidden');$('dashboard').classList.add('hidden');if(CONFIG.platform_base_url){$('sso-btn').classList.remove('hidden');$('sso-divider').classList.remove('hidden')} }
function tabs(){const root=me?.actor_type==='root';const items=root?[['overview','运营总览'],['agencies','代理商'],['pricing','价格政策'],['customers','客户与用量'],['ledger','佣金账本'],['withdrawals','提现审核'],['sync','同步与对账'],['audit','审计日志']]:[['overview','总览'],['customers','客户'],['usage','使用明细'],['topups','充值记录'],['pricing','销售价格'],['ledger','佣金'],['withdrawals','提现'],['accounts','收款账户'],['audit','账号审计']];$('tabs').innerHTML=items.map(x=>'<button class="tab '+(currentTab===x[0]?'active':'')+'" data-tab="'+x[0]+'">'+x[1]+'</button>').join('')}
async function load(path,key){if(me?.actor_type==='root'&&path==='/customers')path='/root/customers';if(me?.actor_type==='root'&&path==='/root/agencies//pricing'){if(!selectedAgencyId)throw Error('请先在代理商列表选择机构');path='/root/agencies/'+selectedAgencyId+'/pricing'}if(dataCache[key])return dataCache[key];const r=await api(path);if(!r.success)throw Error(message(r));dataCache[key]=r.data||{};return dataCache[key]}
function metric(label,value){return '<article class="metric"><span>'+esc(label)+'</span><strong>'+esc(value)+'</strong></article>'}
async function render(){tabs();const root=me?.actor_type==='root';const c=$('content');c.innerHTML='<section class="panel">加载中…</section>';try{if(currentTab==='overview'){const s=await load(root?'/root/sync/status':'/commissions/summary','summary');const customers=await load('/customers','customers');if(root){const agencies=await load('/root/agencies?limit=200','agencies');c.innerHTML='<section class="metrics">'+metric('代理商数',agencies.total??agencies.items?.length??0)+metric('客户数',customers.total??0)+metric('同步积压',s.backlog?.deliveries?.pending??0)+metric('对账异常',s.backlog?.open_reconciliation_issues??0)+'</section><section class="panel"><h2>运行能力</h2><pre class="json">'+json(s)+'</pre></section>'}else{const item=s.items?.[0]||{};c.innerHTML='<section class="metrics">'+metric('可提现余额',item.available_micros??0)+metric('累计佣金',item.earned_micros??0)+metric('客户数',customers.total??0)+metric('待处理提现',(await load('/withdrawals','withdrawals')).total??0)+'</section><section class="panel"><h2>下一步</h2><p class="muted">从“客户”查看用量，从“销售价格”维护完整价格版本，从“提现”管理收款账户和申请。</p></section>'}}else if(currentTab==='agencies'){const d=await load('/root/agencies?limit=200','agencies');c.innerHTML='<section class="panel"><div class="toolbar">'+btn('新建代理商','new-agency')+'</div>'+table([['id','ID'],['display_name','名称'],['status','状态'],['invite_code','邀请码'],['version','版本']],d.items)+'</section><div id="modal"></div>'}else if(currentTab==='customers'){const d=await load('/customers?limit=200','customers');c.innerHTML='<section class="panel"><div class="toolbar"><input id="customer-filter" placeholder="按页面结果筛选用户名"><button data-action="refresh">刷新</button></div>'+table([['user_id','用户ID'],['username','账号'],['binding_id','绑定ID'],['revision','归属版本']],d.items)+'</section>'}else if(currentTab==='usage'||currentTab==='topups'){const d=await load('/customers?limit=200','customers');c.innerHTML='<section class="panel"><h2>选择客户后查看明细</h2>'+table([['user_id','用户ID'],['username','账号'],['revision','归属版本']],d.items)+'</section>'}else if(currentTab==='ledger'){const d=await load('/commissions/ledger?limit=200','ledger');c.innerHTML='<section class="panel">'+table([['entry_type','类型'],['origin_model_name','模型'],['commission_quota','佣金额度'],['amount_micros','金额（微单位）'],['currency_code','币种'],['occurred_at_ms','时间']],d.items)+'</section>'}else if(currentTab==='withdrawals'){const d=await load(root?'/root/withdrawals?limit=200':'/withdrawals?limit=200','withdrawals');c.innerHTML='<section class="panel"><div class="toolbar">'+(!root?btn('申请提现','new-withdrawal'):'')+'</div>'+table([['request_no','申请号'],['status','状态'],['amount_micros','金额（微单位）'],['currency_code','币种'],['version','版本']],d.items)+'</section>'}else if(currentTab==='accounts'){const d=await load('/withdrawal-accounts','accounts');c.innerHTML='<section class="panel"><div class="toolbar">'+btn('新增收款账户','new-account')+'</div>'+table([['id','ID'],['version','版本'],['last4','尾号'],['key_id','密钥版本']],d.items)+'</section>'}else if(currentTab==='pricing'){const d=await load(root?('/root/agencies/'+(me.agency_id||'')+'/pricing'):'/pricing','pricing');c.innerHTML='<section class="panel"><h2>价格政策</h2><pre class="json">'+json(d)+'</pre><p class="muted">价格发布使用整包版本和 expected_revision。页面不展示渠道、采购价或上游密钥。</p></section>'}else if(currentTab==='sync'){const s=await load('/root/sync/status','sync');const issues=await load('/root/reconciliation/issues?limit=200','issues');c.innerHTML='<section class="panel"><h2>同步状态</h2><pre class="json">'+json(s)+'</pre></section><section class="panel"><h2>对账异常</h2>'+table([['id','ID'],['object_type','对象'],['object_id','对象ID'],['status','状态'],['difference','差异']],issues.items)+'</section>'}else if(currentTab==='audit'){const d=await load(root?'/root/audit?limit=200':'/audit?limit=200','audit');c.innerHTML='<section class="panel">'+table([['action','操作'],['object_type','对象'],['object_id','对象ID'],['request_id','请求ID'],['created_at_ms','时间']],d.items)+'</section>'}}catch(e){c.innerHTML='<section class="panel"><p class="error">'+esc(e.message)+'</p><button data-action="refresh">重试</button></section>'}}
function beginSSO(){if(ssoRunning||!CONFIG.platform_base_url)return;ssoRunning=true;fetch(CONFIG.base_path+'/sso/start',{credentials:'include'}).then(r=>r.json()).then(b=>{const d=b?.data;if(!d?.state||!d?.state_hash)throw Error('sso_start_failed');ssoState=d.state;const f=document.createElement('iframe');f.style.display='none';f.src=CONFIG.platform_base_url+'/api/agency/sso?state_hash='+encodeURIComponent(d.state_hash)+'&origin='+encodeURIComponent(location.origin);document.body.appendChild(f)}).catch(()=>{ssoRunning=false;showLogin();$('login-error').textContent='SSO 启动失败，请使用账号密码登录'})}
function finishSSO(ticket){fetch(CONFIG.base_path+'/sso/callback',{method:'POST',credentials:'include',headers:{'Content-Type':'application/json'},body:JSON.stringify({ticket,state:ssoState})}).then(r=>r.json()).then(res=>{if(res.success){location.reload();return}ssoRunning=false;showLogin();$('login-error').textContent=message(res)}).catch(()=>{ssoRunning=false;showLogin()})}
window.addEventListener('message',e=>{if(!platformOrigin||e.origin!==platformOrigin)return;const d=e.data||{};if(d.source!=='new-api-agency-sso')return;if(!d.ok){ssoRunning=false;showLogin();$('login-error').textContent=d.error?'主平台 SSO：'+d.error:'请在主平台登录后再试';return}if(d.ticket)finishSSO(d.ticket)});
document.addEventListener('click',e=>{const row=e.target.closest('tr[data-row-id]');if(row&&currentTab==='agencies'){selectedAgencyId=row.dataset.rowId;currentTab='pricing';dataCache={};void render();return}const tab=e.target.closest('[data-tab]');if(tab){currentTab=tab.dataset.tab;dataCache={};void render();return}const action=e.target.closest('[data-action]')?.dataset.action;if(action==='refresh'){dataCache={};void render()}if(action==='new-agency')newAgencyForm();if(action==='new-account')newAccountForm();if(action==='new-withdrawal')newWithdrawalForm()});
function newAgencyForm(){const m=$('modal');if(!m)return;m.innerHTML='<section class="panel"><h2>新建代理商</h2><form id="agency-form" class="form-grid"><label>名称<input name="display_name" required></label><label>管理账号<input name="operator_username" required></label><label>默认结算系数（BPS）<input name="settlement" type="number" value="7500" min="0" required></label><label>默认销售系数（BPS）<input name="sales" type="number" value="9000" min="1" required></label><label>最低价差（BPS）<input name="spread" type="number" value="500" min="0" required></label><label>销售上限（BPS）<input name="cap" type="number" value="30000" min="1" required></label><div class="full actions"><button type="submit">创建</button><button type="button" class="secondary" data-action="refresh">取消</button></div></form><p id="form-error" class="error"></p></section>';$('agency-form').onsubmit=async e=>{e.preventDefault();const f=new FormData(e.target),body={display_name:f.get('display_name'),operator_username:f.get('operator_username'),status:'active',pricing:{default_settlement_bps:Number(f.get('settlement')),default_sales_bps:Number(f.get('sales')),min_spread_bps:Number(f.get('spread')),sales_cap_bps:Number(f.get('cap')),model_overrides:[]}};const r=await api('/root/agencies',{method:'POST',body:JSON.stringify(body)});if(!r.success){$('form-error').textContent=message(r);return}alert('创建成功。临时密码：'+r.data.temporary_password+'\n邀请码：'+r.data.invite_code);dataCache={};void render()}}
function newAccountForm(){const c=$('content');c.innerHTML='<section class="panel"><h2>新增收款账户</h2><form id="account-form" class="form-grid"><label>账户类型<input name="account_type" required placeholder="bank/alipay"></label><label>账户名称<input name="account_name" required></label><label>账号<input name="account_no" required></label><label>银行/渠道<input name="bank_name"></label><div class="full actions"><button type="submit">保存</button></div></form><p id="form-error" class="error"></p></section>';$('account-form').onsubmit=async e=>{e.preventDefault();const f=new FormData(e.target),r=await api('/withdrawal-accounts',{method:'POST',body:JSON.stringify({account_type:f.get('account_type'),account_name:f.get('account_name'),account_no:f.get('account_no'),bank_name:f.get('bank_name')})});if(!r.success){$('form-error').textContent=message(r);return}currentTab='accounts';dataCache={};void render()}}
function newWithdrawalForm(){const c=$('content');c.innerHTML='<section class="panel"><h2>申请提现</h2><form id="withdrawal-form" class="form-grid"><label>币种<input name="currency_code" value="CNY" required></label><label>金额（微单位）<input name="amount_micros" type="number" min="1" required></label><label>收款账户 ID<input name="account_id" type="number" min="1" required></label><div class="full actions"><button type="submit">提交申请</button></div></form><p id="form-error" class="error"></p></section>';$('withdrawal-form').onsubmit=async e=>{e.preventDefault();const f=new FormData(e.target),r=await api('/withdrawals',{method:'POST',body:JSON.stringify({currency_code:f.get('currency_code'),amount_micros:String(f.get('amount_micros')),account_id:Number(f.get('account_id'))})});if(!r.success){$('form-error').textContent=message(r);return}currentTab='withdrawals';dataCache={};void render()}}
$('sso-btn').onclick=e=>{e.preventDefault();beginSSO()};$('login-form').onsubmit=e=>{e.preventDefault();api('/auth/nonce').then(n=>api('/auth/login',{method:'POST',body:JSON.stringify({username:$('username').value,password:$('password').value,nonce:n.data?.nonce})})).then(r=>{if(r.success)location.reload();else $('login-error').textContent=message(r)})};$('logout').onclick=()=>{api('/auth/logout',{method:'POST',body:'{}'}).then(()=>location.reload())};
(async()=>{const r=await api('/auth/me');if(r.success){me=r.data;$('identity').textContent=(me.actor_type==='root'?'超级管理员代管模式：':'代理商账号：')+(me.username||'Root');if(me.actor_type==='root')$('root-banner').classList.remove('hidden');$('login').classList.add('hidden');$('dashboard').classList.remove('hidden');await render()}else if(CONFIG.platform_base_url)beginSSO();else showLogin()})();
</script></body></html>`

func (a *App) index(c *gin.Context) {
	if dist, err := fs.Sub(agencyWebDist, "webdist"); err == nil {
		if c.Request.URL.Path == a.config.BasePath || c.Request.URL.Path == a.config.BasePath+"/" {
			data, readErr := fs.ReadFile(dist, "index.html")
			if readErr == nil {
				configJSON, _ := common.Marshal(map[string]any{"platform_base_url": a.config.PlatformBaseURL, "base_path": a.config.BasePath})
				data = bytes.ReplaceAll(data, []byte("/agency/assets/"), []byte(a.config.BasePath+"/assets/"))
				data = bytes.Replace(data, []byte("</head>"), append([]byte(`<script>window.__AGENCY_CONFIG__=`), append(configJSON, []byte(`</script></head>`)...)...), 1)
				c.Header("Content-Type", "text/html; charset=utf-8")
				c.Header("Cache-Control", "no-store")
				_, _ = c.Writer.Write(data)
				return
			}
		}
	}
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

func (a *App) staticAsset(c *gin.Context) {
	name := strings.TrimPrefix(c.Param("filepath"), "/")
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	data, err := agencyWebDist.ReadFile("webdist/assets/" + name)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	var contentType string
	switch path.Ext(name) {
	case ".js", ".mjs":
		contentType = "text/javascript; charset=utf-8"
	case ".css":
		contentType = "text/css; charset=utf-8"
	default:
		contentType = mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, data)
}
