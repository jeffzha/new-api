package controller

import (
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// AgencySSOPage serves a tiny same-origin bridge page used by the agency-hub
// sidecar to exchange a browsing Root session for a one-time SSO ticket.
//
// The hub embeds this page in a hidden iframe and waits for the ticket to be
// delivered back through window.postMessage (never through a URL). The page
// actively POSTs to /api/agency/sso-ticket, satisfying the same-origin and
// "no bare 302" constraints of the designed SSO flow. The hub still validates
// the ticket against its own state cookie before establishing a session.
const agencySSOPageTemplate = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>正在跳转到代理商中心</title>
<style>body{font:14px system-ui,sans-serif;background:#f5f7fa;color:#1f2937;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}.box{background:#fff;border:1px solid #e5e7eb;border-radius:8px;padding:20px 28px;max-width:420px;text-align:center}.muted{color:#64748b}</style></head>
<body><div class="box"><h3 id="title">正在验证主平台登录状态</h3><p class="muted" id="hint">请稍候，即将进入代理商中心…</p></div>
<script>
const PAYLOAD = __PAYLOAD__;
const origin = PAYLOAD.origin;
function done(message){parent.postMessage(Object.assign({source:PAYLOAD.mode==='verify'?'new-api-agency-verification':'new-api-agency-sso'},message),origin);}
async function request(path,body){
  async function refresh(){
    const response=await fetch('/api/user/auth/refresh',{method:'POST',credentials:'include',cache:'no-store'});
    const data=await response.json();
    if(!response.ok||!data.success||!data.data?.access_token)throw Error(data.message||'Platform sign-in required.');
    return data.data.access_token;
  }
  const token=navigator.locks?await navigator.locks.request('new-api:auth-refresh',refresh):await refresh();
  const response=await fetch(path,{method:'POST',credentials:'include',headers:{'Content-Type':'application/json','Authorization':'Bearer '+token},body:JSON.stringify(body),cache:'no-store'});
  const data=await response.json();
  if(!response.ok||!data.success)throw Error(data.message||'Verification failed.');
  return data.data;
}
if(PAYLOAD.mode==='verify'){
  let used=false;
  window.addEventListener('message',async function(event){
    if(used||event.source!==parent||event.origin!==origin||event.data?.source!=='agency-hub-verification')return;
    used=true;
    const input=event.data;
    try{const data=await request('/api/agency/verify',input.payload);done({ok:true,request_id:input.request_id,proof:data.proof});}
    catch(error){done({ok:false,request_id:input.request_id,error:error.message});}
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

// agencySSOFrameAncestors returns the CSP frame-ancestors value that permits
// the hub origins in AGENCY_SSO_ALLOWED_ORIGIN to embed this same-origin
// bridge page. The page must be frameable by the hub (cross-origin iframe),
// so X-Frame-Options is deliberately not used here.
func agencySSOFrameAncestors() string {
	origins := allowedAgencySSOOrigins()
	if len(origins) == 0 {
		return ""
	}
	parts := make([]string, 0, len(origins))
	for origin := range origins {
		parts = append(parts, origin)
	}
	sort.Strings(parts)
	return "frame-ancestors " + strings.Join(parts, " ")
}

func AgencySSOPage(c *gin.Context) {
	if len(allowedAgencySSOOrigins()) == 0 {
		respondAgencySSOPageError(c, http.StatusServiceUnavailable, "SSO桥接未配置，请联系管理员")
		return
	}
	stateHash := strings.TrimSpace(c.Query("state_hash"))
	mode := c.Query("mode")
	if mode != "" && mode != "verify" {
		respondAgencySSOPageError(c, http.StatusBadRequest, "无效的桥接模式")
		return
	}
	if stateHash == "" && mode != "verify" {
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
	if ancestors := agencySSOFrameAncestors(); ancestors != "" {
		c.Header("Content-Security-Policy", ancestors)
	}
	_, _ = c.Writer.WriteString(page)
}

func respondAgencySSOPageError(c *gin.Context, status int, message string) {
	body := "<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\"><title>SSO</title></head><body><p>" + message + "</p></body></html>"
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if ancestors := agencySSOFrameAncestors(); ancestors != "" {
		c.Header("Content-Security-Policy", ancestors)
	}
	c.String(status, body)
}
