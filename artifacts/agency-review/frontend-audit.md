# 代理商服务前端、身份与接口契约验收记录

日期：2026-09-12。依据：`pkg/doc/05-agency-hub-sidecar-design.md` 第 5、13、14、18、22.2 节。结论：**前后端业务闭环未完成，不能据现有单元测试通过认定文档全部完成。**

本次仅使用本地合成账号、内存 SQLite、真实 Gin Router 和真实 `/agency/` 页面脚本；未连接生产数据库、未部署、未执行真实提现或模型调用。

## 实际发布页面

- `pkg/agencyhub/app.go:90`、`:91` 将 `/agency` 和 `/agency/` 注册到 `a.index`。
- `pkg/agencyhub/static.go:49` 直接返回文件内的 `agencyIndexHTML`；页面实际业务实现集中在第 35～46 行的内联 JavaScript。
- `agency-web/src/main.tsx` 是另一套 React 页面，只有概览、客户、佣金、提现列表、价格 JSON 等基本展示。
- `Dockerfile:34`、`:35`、`:42` 构建 gateway 的 `web/dist` 和 `agency-hub` Go 二进制，没有构建并嵌入 `agency-web/dist`。因此 `agency-web` 构建成功不等于线上代理商页面完成，也不保证改动会出现在 `/agency/`。

## 已执行的真实联调验收

命令在仓库根目录执行：

```powershell
$env:GIN_MODE = 'release'
go test -overlay artifacts/agency-review/frontend/overlay.json ./pkg/agencyhub -run 'TestFrontendAcceptanceAudit|TestFrontendAudit' -count=1 -v
```

测试以 Go overlay 加载独立验收文件，不向生产 package 写入失败测试。`router_probe_test.go` 创建真实 Router 和合成业务数据；`browser-probe.cjs` 从 Router 下载实际 HTML，以 jsdom DOM 执行原始页面脚本并点击/填写表单，请求实际 Router，不模拟业务 API 的成功/失败结果。Bun 无法通过 jsdom 的 VM 执行全局 Proxy，故提供显式浏览器全局参数执行原始脚本；这不是 Chrome 视觉验收，不覆盖排版、焦点或真实跨站 Cookie 行为。

页面检查结果：**19 项检查中 7 项通过，12 项失败**。另有 2 项契约/安全验收失败。JSON 保存请求 path、body、HTTP 状态和脱敏结果，可复核：

- `frontend/root-results.json`：Root 3 项通过、5 项失败。
- `frontend/operator-results.json`：代理商 3 项通过、6 项失败。
- `frontend/first-login-results.json`：首次登录 1 项通过、1 项失败。

| 操作 | 实际结果 | 影响与代码证据 |
| --- | --- | --- |
| Root 概览 | 成功显示机构数和客户数 | 仅证明基础查询链路；`static.go:38` |
| Root 机构列表 | HTTP 200，显示合成机构 | 基础读取已联通 |
| 普通代理商概览和客户列表 | HTTP 200，显示所属合成客户 | 基础读取已联通 |
| 首次登录后改密 | 页面直接显示“首次登录后必须先修改密码”，没有改密表单 | `auth.go:178` 正确限制；`static.go:46` 忽略 `must_change_password`，造成必经流程死端 |
| Root 客户标签 | 请求 `/agency/api/v1/customers?limit=200`，返回 403 `agency_required` | `static.go:36` 只改写精确 `/customers`，`:38` 标签使用带查询串路径 |
| Root 佣金账本 | 返回 403 `agency_required` | `static.go:38` 请求本机构接口，却没有 enter agency 操作 |
| Root 新建机构 | POST 返回 403 `verification_required` | `static.go:43` 未获取/提交证明；`idempotency.go:180` 必须验证 |
| Root 选择机构编辑价格 | 显示价格 JSON，无编辑、试算、发布控件 | `static.go:38`、`:42` |
| Root 进入/退出代管 | 无可操作入口、无 `/enter` 请求 | 后端 `agency_service.go:752` 已有能力，前端没接 |
| 客户筛选 | 输入不匹配名称后，列表不变 | `static.go:38` 生成输入框，`:42` 无输入处理 |
| 代理商使用明细 | 点击客户行不产生详情请求 | `static.go:38` 只显示客户列表；`:42` 行点击仅处理机构列表 |
| 代理商充值记录 | 点击客户行不产生充值详情请求 | 同上 |
| 代理商销售价格 | 只读 JSON，无编辑、试算、发布 | `static.go:38` |
| 新增收款账户 | POST 返回 403 `verification_required` | `static.go:44` 字段名与 DTO 一致，缺少证明链路；不能误报成字段名错误 |
| 申请提现 | POST 返回 403 `verification_required` | `static.go:45` 没有 `/auth/verify` 调用 |
| 提现表单金额/账户 DTO | 补上验证仍存在格式错误：`account_id: 1` 被 `decimalInt64` 拒绝 | `static.go:45` 使用 `Number`；`finance_service.go:646`、`decimal.go:17` 要求十进制字符串；独立验收返回 `value must be a decimal string` |
| 操作员高风险证明跨 Session 使用 | Session A 签发的收款账户创建证明，可在 Session B 成功执行，HTTP 201；要求为 403 | `auth.go:401` 保存证明不含 Session；`idempotency.go:207` 查询只绑定 actor/body/action/object，不绑定 Session；独立真实 Router 验收已复现 |

## 第 14 节页面逐项对照

下列“已有接口”只表示实现入口存在；页面功能未完成仍按失败/部分处理。

| 目标 | 状态 | 证据或缺口 |
| --- | --- | --- |
| Root 总览：代理商数、客户数 | 已验证基础读取 | 本地真实页面检查通过 |
| Root 总览：当月消费、当月佣金、待审核提现 | 未完成 | `static.go:38` 仅四个指标，没有对应统计请求 |
| Root 总览：积压、对账异常 | 部分 | 读取 sync/status；准确性依赖后端对账实现，非页面通过即可证明 |
| Root 新建代理商 | 失败 | 缺高风险验证，403 |
| Root 编辑、启用、禁用代理商 | 无页面入口 | 后端接口位于 `app.go:141`～`:144`，页面只有新建按钮 |
| Root 重置密码与临时交付确认 | 无完整页面流程 | 后端 reset-password、delivery ack 已注册；页面没有调用；创建后仅 alert，不支持丢响应重取和 ack |
| Root 邀请码、邀请链接 | 部分 | 机构列表显示 invite_code；未显示 invite_url 或复制链接按钮 |
| Root 进入/退出代管 | 未完成 | 后端有 enter/leave；前端未调用，横幅却总把 Root 称作“代管模式” |
| Root 默认 C/S、模型例外 | 未完成 | 只有新建表单默认值；没有编辑完整版本的 UI |
| Root 价格试算、版本历史 | 未完成 | 无 preview/history 请求、无控件 |
| Root 客户与用量 | 失败 | Root 客户标签 403；无客户用量钻取 |
| Root 充值记录 | 未完成 | Root 标签甚至没有充值入口 |
| Root 佣金账本 | 失败 | 缺代管范围，403 |
| Root 提现审核/打款/未知支付处理 | 未完成 | 仅列表，没有 review/transition/mark-paid/reject 操作 |
| Root 同步和对账 | 部分 | 只读 JSON 和异常列表，没有发起对账/处理异常流程 |
| Root 审计日志 | 部分 | 只读列表，无筛选/分页/详情 |
| 代理商总览 | 部分 | 客户、余额可以读取；没有消费统计；待处理提现实际使用所有提现总数 |
| 代理商邀请客户 | 未完成 | 无邀请码、链接、复制入口 |
| 代理商客户资料 | 部分 | 当前仅 user_id、username、binding_id、revision；无注册时间、状态、联系方式和详情操作 |
| 使用明细：时间、模型、用户、状态筛选、CSV | 未完成 | 展示客户列表，无钻取与筛选；没有导出操作 |
| 充值记录：时间、金额、状态、订单信息 | 未完成 | 展示客户列表，无充值请求 |
| 销售价：默认、模型例外、试算、发布 | 未完成 | JSON 展示，不可操作 |
| 佣金：余额、历史 | 部分 | 基础读取存在；多币种只取首个余额；金额直接显示 micros |
| 佣金：模型和用户汇总、冲正 | 未完成 | 无统计筛选和冲正明细操作 |
| 提现：收款账户、申请、历史状态 | 部分/关键操作失败 | 读列表可用，新增账户与申请均 403；修改/停用/撤回无 UI |
| 账号安全：改密、最近登录 | 未完成 | 没有账号安全标签，只有账号审计列表 |
| 空页面下一步、危险操作确认 | 部分 | 空态通用提示存在，但未提供可执行下一步；高风险流程无确认/验证 |
| 稳定分页、日期、币种金额格式 | 未完成 | 无 next_cursor 消费、翻页控件、日期范围；直接显示微单位/时间戳；筛选输入无效 |
| 国际化与可访问性 | 不完整 | 发布页面固定中文；没有完整多语言接入；客户/机构行交互用 `<tr>` 点击，无键盘交互契约测试 |

## 第 5、13、18、22.2 节身份和 API 对照

| 目标 | 核对结果 | 验证边界 |
| --- | --- | --- |
| 独立账号、哈希密码、机构账号唯一性 | 已有实现 | `agency_service.go:96` 创建；模型唯一约束需跨 DB 验证由部署审计负责 |
| 首次改密、12～72 字节 | 后端有实现，前端无法走通 | `auth.go:178`、`:296` |
| 不存在账号同类哈希校验、统一错误 | 有实现 | `auth.go:72` dummy hash |
| IP+账号限流、5 次失败锁 15 分钟 | 不完整 | 有失败计数/锁定时间；`app.go:87` Router 只有 Recovery，无登录/验证/导出等限流；错误密码即使锁定仍执行哈希并更新失败计数。不得把次数锁定等同 IP 限流 |
| 临时密码短期加密、幂等恢复、ack | 后端测试通过，UI 缺失 | Delivery 系列测试覆盖 AAD、过期销毁、源 Root、数组脱敏；前端无 ack/恢复 |
| Root 只允许浏览器会话签票 | 有代码检查 | `controller/agency.go:71` 要求 session identity；gateway 路由带 RootAuth；不能据 SSO 页面字符串测试声称全部实际浏览器行为已验证 |
| SSO state、签名、aud/iss/过期、单次使用 | 现有契约测试通过 | `sso_contract_test.go` 已运行；真正跨站 Cookie/iframe、密钥轮换未做浏览器验证 |
| SSO 来源会话注销/降权立即失效 | 测试通过 | SourceSessionRevocation / SourceSessionDowngrade |
| SSO 多 kid 公钥轮换 | 未见完整实现 | `App.ssoPublicKey` 为单个公钥；Verify 固定入参单 key，需要补充完整轮换验收 |
| SSO nonce 与 Origin | 部分 | 生成 `agency_sso_nonce`，callback 只核对 state Cookie；login/callback 没有 Origin 检查；state 仍有防护，不直接声称可绕过认证 |
| Root 高风险证明绑定 Session/action/body、单用 | 现有契约测试通过 | RootVerificationProof 系列 |
| 普通代理商证明绑定 originating Session | 验收失败 | A Session 签发证明可在 B Session 创建账户，HTTP 201；见独立验收 |
| Session Cookie 安全属性与空闲/绝对过期 | 部分 | HttpOnly/SameSite/Secure 可配置；Cookie Path 实际 `/agency` 而不是文档 `/agency/`；read 每次 DB 校验满足撤权目标但容量另验 |
| 认证后 CSRF 和 Origin | 有实现 | `auth.go:181`；空 Origin 不拒绝，需要结合客户端约束评审 |
| 业务幂等、body hash、先鉴权后重放 | 部分测试通过 | normalized body/cursor/Root proof/command replay 等现有测试通过；UI 每次请求生成新 key，不具备未知结果重试语义 |
| 基础机构与归属管理 API | 有入口 | `app.go:138`～`:161`；页面不能完成这些流程 |
| Root 创建示例 DTO | 文档与代码不一致 | 文档 §13.3 用 `pricing.default.settlement_bps/sales_bps`，实际 Policy 与页面使用扁平 `default_settlement_bps/default_sales_bps` |
| Root 完整价格/代理商 sales DTO | 有实现与部分测试 | 严格 sales 保留 C、例外恢复等测试；UI 无预览/发布，不能形成验收闭环 |
| 客户/用量/充值跨机构隔离、签名 cursor | 已有测试通过 | CustomerUsageAndTopups、CustomerFactCursor、RootCustomerList 等；不代表所有过滤和导出 ID 攻击场景全部覆盖 |
| 用量字段完整性 | 不完整 | `report_service.go:703` 响应只有 event/model/endpoint/status/Q/S/B/currency/skip/time；缺 input/output/cache read/cache write、计费规格、标准化错误/request_id 等 §13.5 必需字段 |
| 日期间隔、精确时间、时区边界 | 现有 tests 通过 | ReportRange、ReportSummaryEndDateBoundary（主审计运行总套件）；UI 无日期选择 |
| 异步导出、下载鉴权、公式注入 | 有后端及测试，UI 缺失 | 导出物化、公式转义、过期、路径穿越、文件篡改相关测试已运行；完整“下载时撤权”场景仍需单独验收 |
| int64 金额字符串 | 部分 | 佣金响应已有字符串和测试；提现 UI account_id 用 number，与其 DTO 冲突 |
| 收款账户加密与版本 | 后端存在、加密测试通过 | AES-GCM AAD 和历史 key 测试通过；新增 UI 缺证明，无法操作 |
| Root reconcile run API | 缺少文档 endpoint | `app.go:157`～`:158` 只有 issues/list/resolve，没有 POST `/root/reconciliation/runs` |
| 管理 UI 防点击劫持 | 未见保障 | `static.go:59` 仅设置 Content-Type/Cache-Control；Router 无 CSP，需代理统一补充且验证实际响应；SSO bridge 的 CSP 不等于整个代理商站点已有保护 |
| 主站邀请注册联动 | 代码存在 | `sign-up-form.tsx:113`、`:177` 携带 invite、排除 aff；`:391`/`:402` 隐藏 OAuth；controller 邀请注册测试由核心审计覆盖 |
| 主站客户有效销售价格展示、Root 管理入口 | 未接入前端 | 搜索 `web/src` 只有邀请注册相关 agency 引用；未找到 effective-pricing 请求或代理商管理导航 |

## 现有测试的实际结果和局限

已执行：

```powershell
go test ./pkg/agencyhub ./controller -run 'SSO|Login|Password|CSRF|Origin|CrossAgency|Scope|Idempotency|Verification|Delivery|Cursor|PayoutAccount|Export|Audit' -count=1 -json
```

退出码 0。此选择器中的匹配测试均通过；部分 selector 同时命中 workbench 测试，不应把匹配总数当作代理商覆盖数。未声称这些测试证明了没有对应用例的 Login/IP 限流或完整 browser SSO。`static_test.go:11` 主要检查页面字符串，未覆盖页面实际操作，本次新联调已证明字符串测试通过与业务流程成功之间存在明显差距。

本次没有执行 Chrome/Edge 视觉、键盘、移动端验收，也没有验证线上镜像、服务器配置、真实短信/邮件/银行；这些应明确列入待验收，而不是算作通过。

## 修复顺序建议

1. 确定唯一发布 UI；补首次改密和统一 high-risk proof 交互；修复 Root 客户路径/代管范围和提现 ID 字符串。
2. 修复操作员 proof 的 Session 绑定，增加登录/高风险操作限流、来源与 frame 保护，并用真实端到端测试保护。
3. 按 §14 完成管理、邀请、价格版本编辑/试算/发布、客户明细/充值、提现审核、导出、审计筛选、分页和金额显示。
4. 将通过标准改为真实用户操作及业务效果，不能用 tab 文案存在、编译成功、API 存在代替功能验收。
