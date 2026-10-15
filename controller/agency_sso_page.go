package controller

import (
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// AgencySSOPage serves the same-origin browser bridge used by Agency Hub.
//
// SSO and administrator verification run in hidden iframes. Account linking
// runs in a visible popup so the operator can see, switch, and explicitly
// confirm the platform account. Proofs are returned through postMessage and
// never placed in a URL.
const agencySSOPageTemplate = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>主站账号授权</title>
<style>body{font:14px system-ui,sans-serif;background:#f5f7fa;color:#1f2937;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}.box{box-sizing:border-box;background:#fff;border:1px solid #e5e7eb;border-radius:8px;padding:24px;max-width:440px;width:calc(100% - 32px);text-align:center}.muted{color:#64748b;line-height:1.6}.account{background:#f8fafc;border:1px solid #e2e8f0;border-radius:6px;margin:18px 0;padding:14px;text-align:left}.account strong{display:block;font-size:16px;margin-top:4px;overflow-wrap:anywhere}.actions{display:flex;gap:10px;justify-content:center;margin-top:18px}button,a.button{border:0;border-radius:6px;box-sizing:border-box;cursor:pointer;font:inherit;padding:10px 16px;text-decoration:none}button.primary{background:#18181b;color:#fff}button.secondary,a.button{background:#f1f5f9;color:#0f172a}button:disabled{cursor:not-allowed;opacity:.5}.error{color:#b91c1c}.hidden{display:none}</style></head>
<body><div class="box"><h3 id="title">正在验证主站登录状态</h3><p class="muted" id="hint">请稍候…</p><div id="account" class="account hidden"><span class="muted">当前主站账号</span><strong id="username"></strong></div><p id="error" class="error hidden"></p><div id="actions" class="actions hidden"><button id="switch" class="secondary" type="button">切换账号</button><button id="confirm" class="primary" type="button" disabled>确认关联此账号</button></div><div id="login-actions" class="actions hidden"><a id="login" class="button">登录主站账号</a></div></div>
<script>
const PAYLOAD = __PAYLOAD__;
const origin = PAYLOAD.origin;
let auth=null,requestId='',used=false;
const target=PAYLOAD.mode==='link'?window.opener:parent;
function done(message){const source=PAYLOAD.mode==='verify'?'new-api-agency-verification':PAYLOAD.mode==='link'?'new-api-agency-link':'new-api-agency-sso';target?.postMessage(Object.assign({source},message),origin);}
async function refresh(){
  const run=async()=>{const response=await fetch('/api/user/auth/refresh',{method:'POST',credentials:'include',cache:'no-store'});const data=await response.json();if(!response.ok||!data.success||!data.data?.access_token)throw Error('请先登录主站账号');return data.data;};
  return navigator.locks?navigator.locks.request('new-api:auth-refresh',run):run();
}
async function request(path,body){
  if(!auth)auth=await refresh();
  const response=await fetch(path,{method:'POST',credentials:'include',headers:{'Content-Type':'application/json','Authorization':'Bearer '+auth.access_token,'X-Auth-Session':auth.session?.sid||''},body:JSON.stringify(body),cache:'no-store'});
  const data=await response.json();
  if(!response.ok||!data.success)throw Error(data.message||'Verification failed.');
  return data.data;
}
function show(id,visible=true){document.getElementById(id).classList.toggle('hidden',!visible);}
function signInURL(){const url=new URL('/sign-in',location.origin);url.searchParams.set('redirect',location.href);return url.href;}
if(PAYLOAD.mode==='link'){
  window.addEventListener('message',async function(event){
    if(event.source!==window.opener||event.origin!==origin||event.data?.source!=='agency-hub-account-link')return;
    requestId=event.data.request_id||'';
    document.getElementById('confirm').disabled=!auth||auth.user?.role===100||!requestId;
  });
  done({ready:true});
  document.getElementById('login').href=signInURL();
  document.getElementById('confirm').addEventListener('click',async function(){
    if(used||!requestId||!auth||auth.user?.role===100)return;used=true;this.disabled=true;
    try{const data=await request('/api/agency/link-proof',{request_id:requestId});done({ok:true,request_id:requestId,proof:data.proof});document.getElementById('hint').textContent='关联验证已完成，此窗口将自动关闭。';}
    catch(error){used=false;this.disabled=false;document.getElementById('error').textContent=error.message;show('error');}
  });
  document.getElementById('switch').addEventListener('click',async function(){
    this.disabled=true;
    try{if(auth)await request('/api/user/auth/logout',{});location.assign(signInURL());}
    catch(error){this.disabled=false;document.getElementById('error').textContent='切换账号失败：'+error.message;show('error');}
  });
  refresh().then(data=>{
    auth=data;document.getElementById('title').textContent='确认关联主站账号';document.getElementById('hint').textContent='主站账号与代理商账号名称可以不同。确认后，该主站账号可免密进入当前代理商账号。';document.getElementById('username').textContent=data.user?.username||'';show('account');show('actions');
    if(data.user?.role===100){document.getElementById('error').textContent='超级管理员账号无需关联，请切换到要关联的普通主站账号。';show('error');}
    document.getElementById('confirm').disabled=data.user?.role===100||!requestId;
  }).catch(error=>{document.getElementById('title').textContent='请登录主站账号';document.getElementById('hint').textContent='登录你要关联的主站账号，登录成功后会返回此授权窗口。';document.getElementById('error').textContent=error.message;show('error');show('login-actions');});
}else if(PAYLOAD.mode==='verify'){
  window.addEventListener('message',async function(event){
    if(used||event.source!==parent||event.origin!==origin||event.data?.source!=='agency-hub-verification')return;used=true;
    try{const data=await request('/api/agency/verify',event.data.payload);done({ok:true,request_id:event.data.request_id,proof:data.proof});}
    catch(error){done({ok:false,request_id:event.data.request_id,error:error.message});}
  });
  done({ready:true});
}else{
  request('/api/agency/sso-ticket',{state_hash:PAYLOAD.state_hash})
    .then(data=>done({ok:true,ticket:data.ticket}))
    .catch(error=>done({ok:false,error:error.message}));
}
</script></body></html>`

// allowedAgencySSOOrigins returns the exact browser origins the agency SSO
// bridge page is allowed to hand tickets to, from AGENCY_SSO_ALLOWED_ORIGIN
// (comma separated). An empty set disables the bridge.
func allowedAgencySSOOrigins() map[string]struct{} {
	allowed := map[string]struct{}{}
	for _, raw := range strings.Split(os.Getenv("AGENCY_SSO_ALLOWED_ORIGIN"), ",") {
		if origin := strings.TrimRight(strings.TrimSpace(raw), "/"); origin != "" {
			allowed[origin] = struct{}{}
		}
	}
	return allowed
}

func agencySSOCSP(mode string) string {
	base := "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'; "
	if mode == "link" {
		return base + "frame-ancestors 'none'"
	}
	origins := allowedAgencySSOOrigins()
	if len(origins) == 0 {
		return ""
	}
	parts := make([]string, 0, len(origins))
	for origin := range origins {
		parts = append(parts, origin)
	}
	sort.Strings(parts)
	return base + "frame-ancestors " + strings.Join(parts, " ")
}

func AgencySSOPage(c *gin.Context) {
	if len(allowedAgencySSOOrigins()) == 0 {
		respondAgencySSOPageError(c, http.StatusServiceUnavailable, "SSO桥接未配置，请联系管理员")
		return
	}
	stateHash := strings.TrimSpace(c.Query("state_hash"))
	mode := c.Query("mode")
	if mode != "" && mode != "verify" && mode != "link" {
		respondAgencySSOPageError(c, http.StatusBadRequest, "无效的桥接模式")
		return
	}
	if stateHash == "" && mode != "verify" && mode != "link" {
		respondAgencySSOPageError(c, http.StatusBadRequest, "缺少 state_hash 参数")
		return
	}
	origin := strings.TrimSpace(c.Query("origin"))
	if _, ok := allowedAgencySSOOrigins()[origin]; !ok {
		respondAgencySSOPageError(c, http.StatusForbidden, "目标来源不在白名单内，拒绝跳转")
		return
	}
	payload, err := common.Marshal(map[string]string{"state_hash": stateHash, "origin": origin, "mode": mode})
	if err != nil {
		respondAgencySSOPageError(c, http.StatusInternalServerError, "页面初始化失败")
		return
	}
	page := strings.Replace(agencySSOPageTemplate, "__PAYLOAD__", string(payload), 1)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if policy := agencySSOCSP(mode); policy != "" {
		c.Header("Content-Security-Policy", policy)
	}
	_, _ = c.Writer.WriteString(page)
}

func respondAgencySSOPageError(c *gin.Context, status int, message string) {
	body := "<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\"><title>SSO</title></head><body><p>" + message + "</p></body></html>"
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if policy := agencySSOCSP(""); policy != "" {
		c.Header("Content-Security-Policy", policy)
	}
	c.String(status, body)
}
