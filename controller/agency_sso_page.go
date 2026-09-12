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
function done(message){try{parent.postMessage(Object.assign({source:'new-api-agency-sso'},message),origin);}catch(e){}}
(function(){
  var token = null;
  try { token = window.localStorage.getItem('new_api_access_token') || null; } catch(e) {}
  if (!token) {
    done({ok:false,error:'主平台未登录，请先打开主平台登录，再重试进入代理商中心'});
    return;
  }
  var headers = {'Content-Type':'application/json','Authorization':'Bearer '+token};
  fetch('/api/agency/sso-ticket',{method:'POST',credentials:'include',headers:headers,body:JSON.stringify({state_hash:PAYLOAD.state_hash})})
    .then(function(response){return response.json().then(function(data){return {status:response.status,data:data};});})
    .then(function(result){
      var data = result.data || {};
      if (result.status === 200 && data.success && data.data && data.data.ticket){
        done({ok:true,ticket:data.data.ticket});
      } else {
        done({ok:false,error:data.message||('http_'+result.status)});
      }
    })
    .catch(function(err){done({ok:false,error:String((err&&err.message)||err)});});
})();
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
	if stateHash == "" {
		respondAgencySSOPageError(c, http.StatusBadRequest, "缺少 state_hash 参数")
		return
	}
	origin := strings.TrimSpace(c.Query("origin"))
	if _, ok := allowedAgencySSOOrigins()[origin]; !ok {
		respondAgencySSOPageError(c, http.StatusForbidden, "目标来源不在白名单内，拒绝跳转")
		return
	}
	payload, err := common.Marshal(map[string]string{"state_hash": stateHash, "origin": origin})
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
