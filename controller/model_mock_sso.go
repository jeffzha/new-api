package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const modelMockInternalTimestampHeader = "X-Model-Mock-Timestamp"
const modelMockInternalSignatureHeader = "X-Model-Mock-Signature"
const modelMockInternalResponseSignatureHeader = "X-Model-Mock-Response-Signature"

const modelMockSSOPageTemplate = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>正在进入模型模拟调度</title>
<style>body{font:14px system-ui,sans-serif;background:#f5f7fa;color:#1f2937;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}.box{background:#fff;border:1px solid #e5e7eb;border-radius:8px;padding:20px 28px;max-width:420px;text-align:center}.muted{color:#64748b}</style></head>
<body><div class="box"><h3>正在验证主平台登录状态</h3><p class="muted">请稍候，即将进入模型模拟调度。</p></div>
<script>
const PAYLOAD=__PAYLOAD__;
function done(message){
  if(PAYLOAD.mode==='redirect'){
    const params=new URLSearchParams();
    if(message.ok&&message.ticket)params.set('model_mock_ticket',message.ticket);
    else params.set('model_mock_error',message.error||'主站登录验证失败');
    location.replace(PAYLOAD.origin+PAYLOAD.return_path+'#'+params.toString());
    return;
  }
  parent.postMessage(Object.assign({source:'new-api-model-mock-sso'},message),PAYLOAD.origin);
}
async function refresh(){
  const response=await fetch('/api/user/auth/refresh',{method:'POST',credentials:'include',cache:'no-store'});
  const data=await response.json();
  if(!response.ok||!data.success||!data.data?.access_token)throw Error(response.status===401?'请先登录 New API 主站':(data.message||'主站登录验证失败'));
  return data.data.access_token;
}
async function run(){
  const token=navigator.locks?await navigator.locks.request('new-api:auth-refresh',refresh):await refresh();
  const response=await fetch('/api/model-mock/sso-ticket',{method:'POST',credentials:'include',headers:{'Content-Type':'application/json','Authorization':'Bearer '+token},body:JSON.stringify({state_hash:PAYLOAD.state_hash}),cache:'no-store'});
  const data=await response.json();
  if(!response.ok||!data.success)throw Error(data.message||'无法创建登录票据');
  done({ok:true,ticket:data.data.ticket});
}
run().catch(error=>done({ok:false,error:error.message}));
</script></body></html>`

func allowedModelMockSSOOrigins() map[string]struct{} {
	allowed := map[string]struct{}{}
	for _, raw := range strings.Split(os.Getenv("MODEL_MOCK_SSO_ALLOWED_ORIGIN"), ",") {
		if origin := strings.TrimRight(strings.TrimSpace(raw), "/"); origin != "" {
			allowed[origin] = struct{}{}
		}
	}
	return allowed
}

func modelMockSSOReturnPath() string {
	configured := strings.TrimSpace(os.Getenv("MODEL_MOCK_SSO_RETURN_PATH"))
	if configured == "" || configured == "/" {
		return "/"
	}
	return "/" + strings.Trim(configured, "/") + "/"
}

func allowedModelMockRoles() map[int]struct{} {
	roles := map[int]struct{}{common.RoleAdminUser: {}, common.RoleRootUser: {}}
	if value := strings.TrimSpace(os.Getenv("MODEL_MOCK_SSO_ALLOWED_ROLES")); value != "" {
		roles = map[int]struct{}{}
		for _, raw := range strings.Split(value, ",") {
			if role, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && role > 0 {
				roles[role] = struct{}{}
			}
		}
	}
	return roles
}

func modelMockRoleAllowed(role int) bool {
	_, allowed := allowedModelMockRoles()[role]
	return allowed
}

func modelMockFrameAncestors() string {
	origins := allowedModelMockSSOOrigins()
	parts := make([]string, 0, len(origins))
	for origin := range origins {
		parts = append(parts, origin)
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return ""
	}
	return "frame-ancestors " + strings.Join(parts, " ")
}

func ModelMockSSOPage(c *gin.Context) {
	if len(allowedModelMockSSOOrigins()) == 0 {
		c.String(http.StatusServiceUnavailable, "模型模拟 SSO 尚未配置")
		return
	}
	stateHash := strings.TrimSpace(c.Query("state_hash"))
	if decoded, err := hex.DecodeString(stateHash); err != nil || len(decoded) != 32 {
		c.String(http.StatusBadRequest, "无效的 SSO state")
		return
	}
	origin := strings.TrimRight(strings.TrimSpace(c.Query("origin")), "/")
	if _, ok := allowedModelMockSSOOrigins()[origin]; !ok {
		c.String(http.StatusForbidden, "目标来源不在白名单内")
		return
	}
	mode := strings.TrimSpace(c.Query("mode"))
	if mode != "" && mode != "redirect" {
		c.String(http.StatusBadRequest, "无效的 SSO 模式")
		return
	}
	returnPath := strings.TrimSpace(c.Query("return_path"))
	if returnPath == "" {
		returnPath = "/"
	}
	if returnPath != modelMockSSOReturnPath() {
		c.String(http.StatusForbidden, "目标路径不在白名单内")
		return
	}
	payload, err := common.Marshal(map[string]string{"state_hash": stateHash, "origin": origin, "return_path": returnPath, "mode": mode})
	if err != nil {
		c.String(http.StatusInternalServerError, "页面初始化失败")
		return
	}
	page := strings.Replace(modelMockSSOPageTemplate, "__PAYLOAD__", string(payload), 1)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if ancestors := modelMockFrameAncestors(); ancestors != "" {
		c.Header("Content-Security-Policy", ancestors)
	}
	_, _ = c.Writer.WriteString(page)
}

func IssueModelMockSSOTicket(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok || c.GetBool("use_access_token") || !sameOriginWorkbenchRequest(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "a same-origin browser session is required"})
		return
	}
	role := c.GetInt("role")
	if !modelMockRoleAllowed(role) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "model mock access is not allowed for this role"})
		return
	}
	var request agencySSORequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid request"})
		return
	}
	if decoded, err := hex.DecodeString(request.StateHash); err != nil || len(decoded) != 32 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid state hash"})
		return
	}
	key, kid, err := loadAgencySigningKey()
	if err != nil {
		common.SysError("model mock SSO signing key: " + err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "model mock SSO is unavailable"})
		return
	}
	now := time.Now().Unix()
	ticket, err := agencyhub.SignSSOTicket(key, agencyhub.SSOTicketClaims{
		Issuer:          "new-api",
		Audience:        "model-mock",
		Subject:         int64(identity.UserID),
		SourceSID:       identity.SessionID,
		UserAuthVersion: identity.UserAuthVersion,
		SessionVersion:  identity.SessionVersion,
		StateHash:       request.StateHash,
		JTI:             common.NewRequestId(),
		KeyID:           kid,
		ObjectID:        c.GetString("username"),
		ExpectedVersion: int64(role),
		Action:          "model-mock-operator",
		IssuedAt:        now,
		NotBefore:       now,
		ExpiresAt:       now + 60,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to sign model mock ticket"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"ticket": ticket, "expires_at": now + 60}})
}

type modelMockSessionStatusRequest struct {
	UserID          int64  `json:"user_id"`
	SourceSID       string `json:"source_sid"`
	UserAuthVersion int64  `json:"user_auth_version"`
	SessionVersion  int64  `json:"session_version"`
}

func modelMockInternalMAC(secret, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func GetModelMockSessionStatus(c *gin.Context) {
	secret := strings.TrimSpace(os.Getenv("MODEL_MOCK_INTERNAL_SECRET"))
	timestamp := strings.TrimSpace(c.GetHeader(modelMockInternalTimestampHeader))
	signature := strings.TrimSpace(c.GetHeader(modelMockInternalSignatureHeader))
	parsedTimestamp, err := strconv.ParseInt(timestamp, 10, 64)
	if secret == "" || err != nil || parsedTimestamp < time.Now().Add(-30*time.Second).Unix() || parsedTimestamp > time.Now().Add(30*time.Second).Unix() {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "invalid internal request"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	body, err := c.GetRawData()
	if err != nil || !hmac.Equal([]byte(strings.ToLower(signature)), []byte(modelMockInternalMAC(secret, timestamp+"\n"+string(body)))) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "invalid internal request"})
		return
	}
	var request modelMockSessionStatusRequest
	if err = common.Unmarshal(body, &request); err != nil || request.UserID <= 0 || request.SourceSID == "" || request.UserAuthVersion <= 0 || request.SessionVersion <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid session reference"})
		return
	}
	_, user, validationErr := service.ValidateLoginSession(service.AuthIdentity{UserID: int(request.UserID), SessionID: request.SourceSID, UserAuthVersion: request.UserAuthVersion, SessionVersion: request.SessionVersion})
	active := validationErr == nil && user != nil && modelMockRoleAllowed(user.Role)
	responseBody, marshalErr := common.Marshal(gin.H{"success": true, "data": gin.H{"active": active}})
	if marshalErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to encode status"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header(modelMockInternalResponseSignatureHeader, modelMockInternalMAC(secret, strconv.Itoa(http.StatusOK)+"\n"+timestamp+"\n"+string(responseBody)))
	c.Data(http.StatusOK, "application/json; charset=utf-8", responseBody)
}
