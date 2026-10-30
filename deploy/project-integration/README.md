# 聚合平台统一认证联调配置

适用分支：codex/project-integration-uac-20261029。本目录配置只用于独立项目实例，产品实例保持现有配置。密钥通过部署环境注入，不写入数据库选项、前端环境或版本库。

## 登录方式

| 配置 | 行为 |
| --- | --- |
| 未启用 UAC | 保留现有单系统登录 |
| UAC_OIDC_ENABLED=true、UAC_OIDC_REQUIRED=false | 同时提供原有登录和统一认证登录 |
| 两项均为 true | 项目实例仅接受统一认证登录，密码、注册和其他主认证入口由后端拒绝 |

本次 /test 联调使用第三种，不设置测试用户名单。统一认证中心仍须给客户端配置运维人员准入，通过应用准入后才能取得授权码。本地权限由本平台管理，不根据 UAC 角色自动升降级。

统一认证的发起、回调、二次验证续接和退出回跳分别使用现有会话接口限流，各自按来源 IP 计数。默认每项每分钟 120 次，由 AUTH_SESSION_RATE_LIMIT 与 AUTH_SESSION_RATE_LIMIT_DURATION 配置；超过时返回 429 和 Retry-After。上述步骤不占用密码登录的尝试次数，也不共用彼此的次数。UAC 的账号密码验证及其防暴力尝试策略由认证中心执行。

## 配置步骤

1. 参考 uac.env.example 设置项目实例环境。UAC_OIDC_CLIENT_SECRET 仅交给后端；UAC_PUBLIC_URL 是浏览器访问的完整实例地址，末尾不加斜线。
2. 给项目实例生成独立的 SESSION_SECRET，不能复用产品实例的签名密钥。即使共用主数据库，也要分别配置 Cookie、签名密钥和前端构建目录；本地测试使用独立测试数据库。
3. 前端构建设置 VITE_INSTANCE_BASE_PATH=/test、VITE_UAC_INSTANCE=true、VITE_UAC_REQUIRED=true。随后从本工作目录构建后端，使其嵌入该前端。产品构建不设置这三项。
4. 反向代理将 /test/ 原样转发给项目实例，由应用移除此前缀；不要在代理层再移除一次。产品根路径仍交给产品实例。上线前单独检查流式请求、附件和产品切换入口。
5. 向 UAC 登记精确地址：登录回调为 https://gateway.nexus-reach.com/test/api/oauth/uac/callback，退出回跳为 https://gateway.nexus-reach.com/test/api/oauth/uac/logout/callback。本地 HTTPS 地址须另行登记。
6. 认证中心使用私有 CA 时，经可信渠道取得 CA 和指纹，配置 UAC_OIDC_CA_FILE。客户端始终校验证书，不使用中转请求的跳过证书校验设置。

首次登录按精确的 issuer 与 sub 关联本地账号，不按邮箱合并。UAC_INITIAL_ROLE=10 为管理员，100 为超级管理员，仅首次创建时生效；后续以本地已有角色为准。账号停用或降为普通用户后不能继续进入本运维控制台。已启用的本地二次验证仍需完成。

2026-10-30 确认本机 WLAN 地址为 192.168.124.109。本地联调登记改用以下地址，保留正式 /test 地址：

| 用途 | 地址 |
| --- | --- |
| 本地启动地址 | https://192.168.124.109:3443/test/ |
| 本地登录回调 | https://192.168.124.109:3443/test/api/oauth/uac/callback |
| 本地退出回跳 | https://192.168.124.109:3443/test/api/oauth/uac/logout/callback |

本地启动脚本通过 UAC_LOCAL_PUBLIC_URL 覆盖访问地址，并据此设置后端回调和可信来源。实际启动 HTTPS 服务时，监听地址和证书的 IP SAN 需要覆盖该地址。上述本机 IP 登录回调和退出回跳已通过真实 UAC 浏览器联调。

## 代码接入位置

认证协议、临时状态及令牌校验集中在 service/uac；账号关联放在 model/uac_identity.go；HTTP 入口放在 controller/uac.go、router/uac.go；会话复查放在 service/uac_session.go；前端入口和实例路径适配放在 web/src/features/auth/uac。部署配置集中在本目录。

现有文件只增加路由注册、会话检查、登录入口及 /test 路径适配。原有单系统登录继续使用原来的实现；实例不开启 UAC 时保持原行为。后续扩展优先在上述模块内增加，不复制登录系统，不重构模型调用和计费模块。两个实例的状态通过环境配置区分，不依赖共享数据库内的全局开关。

## 本版边界

UAC 令牌、登录临时状态和二次验证续接保存在进程内存，尚未实现多实例共享。项目联调部署使用单实例；重启后需要重新登录。账号关联保存在现有身份关联表，重启不会重复创建账号。本次没有新增数据库表或迁移。

正常访问最长每 30 秒复查 UAC 用户信息；访问令牌将到期时刷新并验证新 ID Token，刷新令牌只保留新值。UAC 撤销准入后，本地下一次复查或刷新失败即拒绝访问。当前没有后端单点退出通知，不能承诺撤权后零延迟退出。退出本平台先撤销本地会话，再发起 UAC 退出；UAC 不可用时本平台仍可退出。

/test 与产品使用不同的认证 Cookie 名称、Cookie 路径、浏览器认证通知通道和状态缓存。同源路径不是安全隔离边界；需要更强隔离时采用独立域名。本轮不调整已有功能菜单及其他系统的接入。

## 2026-10-30 验证记录

- 本地 HTTPS 模拟 UAC：授权码、PKCE S256、Basic 客户端认证、RS256/JWKS、issuer/audience/nonce/有效期/at_hash/sub 校验、重复回调、浏览器绑定、令牌刷新、准入撤销、账号关联、本地会话及退出、二次验证续接专项测试通过。
- 实际数据库：SQLite 3.50.4、MySQL 8.4.6、PostgreSQL 16.10 的 controller 包 TestUAC 专项通过。身份关联使用已有表，重复迁移后关联数据保留。
- 现有会话创建、刷新、撤销专项回归通过。前端 UAC、二次验证、会话及状态缓存测试共 34 项通过；类型检查和项目构建通过。桌面及手机登录页使用构建产物和模拟状态接口检查，未发现脚本异常。
- 全量 service 回归中的固定图片计费测试失败，同一失败在未改动的产品分支复现；未修改计费代码。
- 全量 router 回归中的 TestAgencyRoutesLimitAuthenticatedUsersWithoutConsumingLoginAttempts 返回 400，测试预期 403；使用 Git overlay 还原本分支未修改的 HEAD 后同样复现，属于已有测试失败。本次未调整分销商接口。
- 全量 controller 回归未通过。与未修改的 HEAD 使用相同命令对照，失败集合一致：含子用例共 24 项，涉及分销计费、资料读取、充值额度等 12 项顶层测试；本次没有新增失败项。不能将专项通过表述为全量回归通过。
- Windows 应用控制曾拒绝执行新构建程序，正常重试后已可运行，未修改安全策略。完整聚合后端、隔离 SQLite 数据库、本地 HTTPS 模拟 UAC 和 /test 前端的浏览器验证通过：登录、自动建号、页面刷新恢复、会话刷新、通过页面退出及认证中心退出回跳、Cookie 路径、后端拒绝密码登录、实例外路径拒绝。路由和环境配置专项通过。该结果仅代表本地模拟认证中心联调。
- 真实 UAC 证书信任问题已通过对方提供的 CA 解决：后端使用应用专用 CA 校验，发现文档和 JWKS 均返回 HTTP 200，应用实际 OIDC 客户端的发现请求通过。为验证真实浏览器跳转，已将对方 CA 和本地 HTTPS 测试证书导入当前 Windows 用户的 Root 证书库，未写入 LocalMachine 证书库，未关闭 TLS 校验。对方 CA 有效至北京时间 2026-11-29 11:29:24，后续部署需要更新有效证书。
- 对方重新提供客户端后，正式 /test 回调的授权请求已返回 HTTP 302，进入 /uac/login。Basic 客户端认证使用故意无效的授权码检查，返回预期的 invalid_grant / Invalid 'code' in request，未再出现 invalid_client。此检查不代表真实授权码换票通过。新密钥只在请求及本地后端进程内存中使用，未写入配置文件或版本库。
- 前一轮真实浏览器测试中，原先提供的运营管理员和租户管理员均完成 UAC 账号登录，随后在授权恢复阶段收到 HTTP 400、access_denied，未返回聚合回调。该结果仅针对当时两个账号及其授权状态；未据此调整 UAC 权限。
- 随后使用新提供、据对方说明仅有 UAC 运维管理员角色的账号完成两次独立浏览器测试：真实授权码回调及换票、进入聚合平台控制台、本地登录会话刷新、页面重载、页面退出、UAC 退出及本机 IP 退出回跳均通过。两次均取得同一本地账号（隔离测试库用户 ID 2），该账号只有一条 UAC 身份关联，没有重复建号。浏览器未跳过 TLS 校验，未出现页面脚本异常。
- 本地测试启动配置为 UAC_INITIAL_ROLE=100，因此首次创建的本地账号为聚合平台超级管理员；这是本平台的初始化策略，不是 UAC 运维管理员角色的自动映射，也不要求 UAC 账号具有超级管理员角色。部署示例默认值仍为 10，正式交付按确认的本地初始角色设置。上述刷新验证针对本地登录会话，真实 UAC 访问令牌到期后的续期尚未实测。
- 连续联调曾使 /api/oauth/uac/start 返回 429。原实现把统一认证全部步骤计入密码登录使用的同一限额（默认 20 分钟 20 次），导致发起、回调和退出互相占用次数。已仅调整 router/uac.go，分别复用会话接口限流；路由回归用例先在原实现复现失败，修改后内存和 Redis 两种限流均通过，验证步骤互不占用、超过限额仍返回 429/Retry-After、密码登录保持原限额。controller 与 router 的 UAC、项目路径和会话限流专项通过，后端重新构建通过。本地测试后端重启一次，真实 UAC 登录、同账号关联、页面重载、本地会话刷新及退出回跳再次通过；未重启产品或运维平台服务。
- 2026-10-30 14:33 左右再次检查用户反馈的 temporarily_unavailable：本地发起接口返回 302，UAC 发现文档及 JWKS 返回 200，无登录会话的授权请求返回 302；真实浏览器中 UAC 账号密码验证返回 200，随后 /uac/oidc/resume/ 返回 429，错误体为 temporarily_unavailable，未到聚合平台回调。已停止重复登录。该次复现确认 UAC 侧触发频率限制，具体计数范围、阈值和恢复时间待 UAC 运维核查；不根据错误名称单独推断协议关闭，也不调整或绕过对方限流。

安全设计依据为 OWASP Authentication、Session Management、OAuth 2.0 Cheat Sheets，参考 ASVS 5.0.0 的认证、会话和 OAuth 控制；当前验证记录不构成完整 ASVS 合规结论。

主要验证命令：

    go build ./...
    go test ./controller ./router -run 'TestUAC|TestProjectInstance' -count=1
    go test ./service -run 'Test(LoginSession|CreateLoginSession|PasswordResetDoesNot|UserAuthVersion)' -count=1
    bun run typecheck
    bun run test src/features/auth/uac/__tests__/sign-in.test.tsx src/features/auth/otp/__tests__/login-verification.test.tsx src/lib/auth-session.test.ts src/lib/__tests__/status-query.test.tsx
    bun run build

三种数据库使用 TEST_SECURITY_DIALECT 和对应 TEST_SQLITE_DSN、TEST_MYSQL_DSN、TEST_POSTGRES_DSN 运行 controller 的 TestUAC；未设置方言时使用隔离 SQLite。前端命令在 web 目录执行，项目构建需先设置前述三项 VITE 配置。验证过程不使用产品数据库。

临时 Gateway、HTTPS 模拟认证中心、MySQL、PostgreSQL 均已停止。清理命令被自动审批策略拒绝，测试库、模拟证书、程序及日志暂留 .local-dev/uac-validation，MySQL 临时目录链接 D:/DevCache/uac-mysql-validation-20261030 仍在。该目录不包含真实 UAC 客户端凭据。

真实 UAC 本地联调实例继续保留：Gateway 监听 3002，本机 HTTPS 代理监听 192.168.124.109:3443，入口为 https://192.168.124.109:3443/test/；使用 .local-dev/uac-real/gateway.db 隔离测试库。该实例未部署到项目服务器，未修改产品分支和产品数据库。测试脚本每次结束均退出账号，不保留浏览器登录会话；用户名、密码和客户端密钥未写入本记录。
