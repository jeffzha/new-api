# 代理商核心三项补齐与验收记录

日期：2026-09-13。范围：邀请客户入口、默认无折扣、佣金汇总展示。

这三项已完成源码实现、隔离 SQLite 验证、浏览器操作验证和前端嵌入构建，可以进入部署联调。本记录不表示完整设计文档全部验收，也不表示生产已经部署。此次没有连接生产或修改生产数据库。

## 功能结果

| 项目 | 使用位置 | 已实现行为 |
| --- | --- | --- |
| 注册链接和二维码 | 代理商中心 → 邀请客户 | 显示当前代理商名称、邀请码、完整注册链接；复制链接、打开注册页面、展示及下载 PNG 二维码；加载失败可重试，复制失败保留完整可手动复制的链接 |
| 默认无折扣 | Root → 代理商管理 → 创建代理商 | 新建表单默认销售系数为 `1.0000`；创建 API 省略该字段时默认 `10000 bps`；显式设置折扣仍保留，已有代理商策略不变 |
| 佣金汇总 | 代理商中心 → 概览 | 分币种显示可提现佣金、已提现佣金、净总佣金、累计获得、已退佣、冻结佣金；支持刷新 |

净总佣金 = 累计获得 − 已退佣。已提现和冻结属于金额分布，不从净总佣金中再次扣除。例如累计获得 1,000 元、已退佣 200 元时，净总佣金是 800 元；其中可提现 400 元、已提现 300 元、冻结 100 元。

销售系数 `1.0000` 表示按原标准价收费，`0.9000` 表示九折。该默认值不绕过现有结算价、最低价差、销售上限校验。

## 实现与隔离

- [邀请接口](../pkg/agencyhub/invitation_service.go)：新增 `GET /agency/api/v1/invitation`，机构范围仅来自登录会话，忽略请求中的其他机构 ID。Root 需要先进入某个代理商的管理范围。
- [邀请页面](../agency-web/src/features/invitations/InvitationsPage.tsx)：链接复制、二维码请求及下载、异常重试；按项目既有独立前端结构实现，补齐七种语言。
- [创建默认值与二维码](../pkg/agencyhub/agency_service.go)：JSON 解码前初始化销售系数；二维码只编码配置生成的绝对注册链接，无效公开地址返回 503。二维码使用 `no-store`。
- [佣金汇总接口](../pkg/agencyhub/finance_service.go)：增加字符串字段 `net_earned_micros`，整数运算保留精度，按代理商和币种分别返回。
- [佣金概览](../agency-web/src/features/reports/Pages.tsx)：六项金额和计算说明，桌面三列、手机单列，超大金额仍保留六位小数精度。

此次没有增加数据库表或字段，也没有为了默认无折扣批量重写旧策略。

## 实际验证结果

| 检查 | 结果与范围 |
| --- | --- |
| `bun test src` | 40 项通过，0 失败，包括七语言完整性、折扣请求契约和 PNG 错误响应拒绝 |
| TypeScript `tsc --noEmit` | 通过 |
| `go test ./pkg/agencyhub ./controller -count=1 -timeout=180s` | 两包通过；319 个顶层测试通过。3 个顶层测试及部分外部数据库子用例因平台/外库环境门槛跳过，不计入通过数 |
| Edge / Playwright 浏览器 | 8 条流程通过，0 重试、0 跳过 |
| 独立 QR 解码 | 使用 ZXing 解码浏览器实际下载的 PNG，内容与 API 的 `invite_url` 完全一致 |
| `bun run build:embed` | 通过，更新 Go 内嵌前端的入口和本次 JS/CSS |
| `go vet ./pkg/agencyhub ./cmd/agency-hub` | 通过 |
| `go build -o E:/new-api-test-cache/agency-hub-core-three.exe ./cmd/agency-hub` | 通过；为 Windows 本地验证二进制，不是 Linux 部署包 |

浏览器覆盖：邀请链接复制、复制权限失败提示、二维码首次失败后重试、真实 PNG 下载；佣金退款及冻结/提现后的净额、多币种与大整数精度、刷新和手机无横向溢出；创建代理商并修改初始密码、折扣发布及并发修订冲突、提现审核与未知结果恢复、CSV 下载、客户归属操作、对账证据及缺失投递修复。

邀请展示使用真实 Hub API 和隔离数据库；错误二维码响应和剪贴板失败为测试主动注入。未执行手机实体扫码到生产注册，也没有在主站浏览器中完成注册。主站注册绑定由本轮通过的 Controller 集成测试覆盖，其中包含邀请注册、原子绑定、无效邀请回滚和消费到佣金报表的核心链路。

浏览器调试过程保留：首轮新增邀请测试使用标签精确匹配未定位到 textarea，改为按可访问角色和名称定位；第二轮已有提现测试遭遇浏览器将毫秒 `.200` 规范化为 `.2`，改为传入浏览器规范化后的相同时间值。没有放宽业务金额或状态断言。最后一次八条全部通过。

## 验收证据

- 最终浏览器目录：`E:/new-api-test-cache/agency-browser/20260913-203957-2039dfc2`，包含 `results.json`、服务日志及截图。
- 后端逐项结果：`E:/new-api-test-cache/agency-core-three-go.jsonl`。
- 下载图片解码结果：`http://127.0.0.1:4328/register?invite=9RR4NRX773`，与该次隔离代理商 API 返回一致；该地址仅用于本地测试。
- [邀请页面及复制失败提示](E:/new-api-test-cache/agency-browser/20260913-203957-2039dfc2/results/agency-workflows-operator--8a1f6-r-a-recoverable-image-error/invitation-page-zh.png)。
- [邀请页面手机示例](E:/new-api-test-cache/agency-browser/20260913-203957-2039dfc2/results/agency-workflows-operator--8a1f6-r-a-recoverable-image-error/invitation-page-mobile-zh.png)。
- [佣金概览桌面示例](E:/new-api-test-cache/agency-browser/20260913-203957-2039dfc2/results/agency-workflows-operator--0c57a-aid-or-locked-amounts-twice/commission-overview-zh.png)。
- [佣金概览手机示例](E:/new-api-test-cache/agency-browser/20260913-203957-2039dfc2/results/agency-workflows-operator--0c57a-aid-or-locked-amounts-twice/commission-overview-mobile-zh.png)。

构建缓存、下载缓存、临时文件及浏览器证据均在 E 盘。首次邀请测试误生成的两个 SQLite 文件已从源码目录移到 `E:/new-api-test-cache/invitation-test-artifacts`，保留可恢复；不是业务数据库。

## 部署联调要点

公开地址需要填写真正承载主站注册页面的入口：

```text
AGENCY_HUB_PUBLIC_BASE_URL=https://gateway.nexus-reach.com
AGENCY_HUB_BASE_PATH=/agency
```

预期邀请链接为 `https://gateway.nexus-reach.com/register?invite=实际邀请码`。该域名的 `/register`、`/sign-up`、`/api/user/register` 需要到主站，`/agency/*` 到 Agency Hub。`AGENCY_HUB_PLATFORM_BASE_URL` 用于其他主站交互，不替代邀请地址配置。

当前仓库部署示例仍默认关闭 `AGENCY_ONBOARDING_ENABLED`，且 Hub Compose 中 `AGENCY_HUB_COMMISSION_PROCESSING_ENABLED` 写为 `"false"`。部署验收时必须核对网关注册绑定开关、主站注册设置、Hub 消费者以及投递/命令链路；仅改 `.env` 中同名佣金变量不会覆盖 Compose 的硬编码值。此次没有自动打开这些生产能力。

对本次范围以外的注册恢复仍保留两项设计差异：`Registration-Idempotency-Key` 尚未实现，注册成功但响应丢失后重试会提示账号已存在；`invite` 与旧 `aff_code` 同传时当前返回 HTTP 200 + `success:false`，而设计要求 HTTP 400。两项均非此次新增邀请页面引入，完整设计验收仍需单独处理。
