# Agent Store 与腾讯 ADP 智能工作台操作手册

> 2026-08-12 起，应用上架已合并为“配置 → 验证预览 → 确认并上架”的单一向导。新增应用请以 [腾讯 ADP 应用统一上架操作手册](./adp-agent-store-unified-listing-manual.md) 为准；本文中仍描述独立 Customer App/deployment 的章节仅用于历史运维背景，不再作为新增应用步骤。
>
> **AppId 与 AppKey 的填写位置**：进入 **智能体商店 → 上架应用**，在弹窗第二个区块 **腾讯 ADP 应用配置** 中填写。以“高考志愿无忧”为例，`App ID` 填 `2048342527164967296`，`AppKey` 填该 Application 自己的完整 AppKey；不要填 SecretKey、new-api API Key，也不要添加 `Bearer `。详细顺序见上面的统一上架手册第 6 节。

> 适用环境：`https://gateway.nexus-reach.com`
>
> 当前生产版本：以系统信息页显示的 `v1.0.0-rc.21.claw.*` 为准
>
> 管理入口：`https://gateway.nexus-reach.com/workbench-admin`
>
> 客户入口：`https://gateway.nexus-reach.com/agent-store`

本手册说明如何把一个已经在腾讯 ADP 中开发并发布的 Application 配置到平台、加入固定月度套餐、上架到 Agent Store，并让被授权的 new-api 用户启动独立的智能工作台。文中的 Secret、AppKey 和密码均为占位符；不要把真实值写入文档、聊天、工单或命令历史。AppKey 只允许在 HTTPS 管理界面的专用密码输入框中提交一次。

## 1. 先理解四个对象

```mermaid
flowchart LR
  U["new-api 用户"] --> M["客户成员关系"]
  M --> C["客户"]
  C --> A["已验证的腾讯 ADP Application"]
  A --> D["Agent Store 客户部署"]
  D --> I["Agent Store 目录应用"]
  I --> L["用户启动工作台"]
```

- **客户**：计费、成员、Application 和套餐的隔离边界，例如 `NEXUS-INTERNAL`。
- **Customer App**：客户授权给平台使用的一套腾讯 ADP Application 配置。AppKey 由管理界面一次性写入数据库加密保险库；界面只提示是否已经配置，不展示内部引用或指纹。AK/SK 继续由平台运维人员管理。
- **Agent Store 目录应用**：用户看到的名称、简介、头像、分类和标签；管理界面通过 **上架应用** 创建。一个目录应用可以关联多个客户部署。
- **客户部署**：目录应用与某个客户的某个 Customer App 之间的绑定，独立管理验证状态、执行开关和使用授权。

目录中的“智能体”是产品展示名称，底层绑定的是腾讯 ADP **Application**，不要求所有应用都是 Claw 模式。

## 2. 角色和权限

| 角色 | 可以执行的操作 | 不能执行的操作 |
|---|---|---|
| new-api 超级管理员 | 客户、成员、凭据引用、App、套餐、Agent Store、审计和治理操作 | 查看明文 AppKey、SecretId、SecretKey |
| 客户 `owner` | 使用该客户已授权并已发布的 Agent；客户内最高业务角色 | 进入平台超级管理控制面 |
| 客户 `admin` | 使用按角色授权的 Agent | 修改平台级凭据和目录 |
| 客户 `member` | 使用按成员或套餐授权的 Agent | 管理其他成员 |
| 客户 `viewer` | 查看或使用明确允许的只读能力 | 获得未授权执行能力 |

所有管理写操作都要求：

1. new-api 超级管理员登录状态；
2. 同源管理会话和 CSRF 校验；
3. 15 分钟内完成过一次真实的密码、2FA 或 Passkey 重新认证。

如果写操作提示“需要最近管理员认证”，页面会跳回 `/workbench-admin?step_up=1`。输入当前管理员密码，或选择 2FA/Passkey；验证成功后会自动返回管理控制面。

## 3. 腾讯侧准备

每个准备上架的 Application 至少确认以下内容：

| 字段 | 获取位置 | 示例 |
|---|---|---|
| Region | 腾讯 ADP 应用所在地域 | `ap-guangzhou` |
| SpaceId | ADP 空间信息 | `default_space` |
| AppId | ADP Application 详情 | `2085927381516339648` |
| AppKey | ADP Application 调用配置 | 在 HTTPS 管理界面一次性输入；之后不回显 |
| SecretId / SecretKey | 腾讯云访问密钥管理 | 只放入服务器 Secret 文件 |
| 模板 AgentId | 仅动态 Claw Application 需要 | `4959df3a-1f38-4906-be51-c8e2cd65eab0` |

Application 必须已经发布且处于可运行状态。平台会通过腾讯官方接口读回 `AppMode`、发布状态、动态配置状态、应用名称、说明和头像；管理员不能手工声明这些可信字段。

### 3.1 AppMode 与运行模式

| 腾讯读回结果 | 平台运行模式 | 模板 AgentId | 说明 |
|---|---|---|---|
| AppMode 1 | `standard_v2` | 留空 | 标准应用，不复制用户 Agent |
| AppMode 2 | `multi_agent_v2` | 留空 | 多 Agent 应用，使用发布态入口 |
| AppMode 3 | `workflow_v2` | 留空 | 工作流应用 |
| AppMode 4，动态配置关闭 | `claw_static_v2` | 留空 | 静态 Claw，不复制用户 Agent |
| AppMode 4，动态配置开启 | `claw_dynamic_v2` | 必填 | 每个 `(客户成员, Application)` 维护独立用户 Agent |

不要为了通过表单给 AppMode 1/2/3 填写虚假 AgentId。平台会按腾讯读回结果选择运行策略；未知 AppMode 会拒绝验证。

## 4. 首次安装腾讯云访问密钥

本节只需由服务器运维执行一次。已经配置完成的客户可以直接跳到第 5 节。

### 4.1 命名规则

文件名必须是大写环境变量名，并以 `WORKBENCH_PROVIDER_` 开头。以 `NEXUS-INTERNAL` 为例：

```text
WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_ID
WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_KEY
```

对应的管理界面引用为：

```text
env://WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_ID
env://WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_KEY
```

### 4.2 安全写入文件

登录生产服务器后执行。下面的 `read -rsp` 不会把输入回显到终端，也不会把 Secret 写入命令历史：

```bash
cd /opt/new-api/deploy/claw-workbench
install -d -o 10001 -g 10001 -m 0700 secrets/provider

read -rsp 'Tencent SecretId: ' VALUE; printf '%s' "$VALUE" > secrets/provider/WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_ID; unset VALUE; echo
read -rsp 'Tencent SecretKey: ' VALUE; printf '%s' "$VALUE" > secrets/provider/WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_KEY; unset VALUE; echo

chown 10001:10001 secrets/provider/WORKBENCH_PROVIDER_NEXUS_INTERNAL_*
chmod 0600 secrets/provider/WORKBENCH_PROVIDER_NEXUS_INTERNAL_*
```

目录必须由容器 UID/GID `10001:10001` 拥有且权限为 `0700`；文件必须由 `10001:10001` 拥有且为 `0400` 或 `0600`。如果目录是 `root:root/0700`，即使文件本身正确，非 root 容器也无法读取。

### 4.3 计算指纹

指纹脚本只输出 SHA-256，不输出 Secret：

```bash
cd /opt/new-api/deploy/claw-workbench

python3 scripts/provider_fingerprint.py credential \
  WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_ID \
  WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_KEY

sh scripts/preflight.sh
```

记录凭据脚本输出的 `sha256:<64位小写十六进制>` 结果。SecretId 和 SecretKey 本身不能填写到浏览器；AppKey 则在第 8 节的专用密码输入框中提交。

## 5. 进入新版管理控制面

1. 登录 `https://gateway.nexus-reach.com` 的超级管理员账号。
2. 打开左侧菜单 **智能工作台管理**，或直接访问 `https://gateway.nexus-reach.com/workbench-admin`。
3. 首次进入会自动使用一次性票据建立短期管理会话。
4. 执行写操作时，如出现安全验证页，输入管理员密码或使用 2FA/Passkey。
5. 管理界面左侧应看到：概览、客户、套餐、凭据、用量、审计、治理、智能体商店。

不要直接访问 `/workbench/admin/` 来绕过入口，也不要把维护用 bootstrap token 放入浏览器。

## 6. 创建凭据配置

进入 **凭据** → **新建凭据配置**。

| 界面字段 | 示例 | 说明 |
|---|---|---|
| 作用域 | 客户 | 推荐每客户独立；平台作用域会被多个客户共享 |
| 客户 ID | `1` | `NEXUS-INTERNAL` 的内部客户 ID |
| 供应商环境 | `china_tencent_adp` | 本功能使用腾讯 ADP |
| 名称 | `nexus-internal-primary` | 便于管理员识别，不是腾讯名称 |
| SecretId 引用 | `env://WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_ID` | 只填引用 |
| SecretKey 引用 | `env://WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_KEY` | 只填引用 |
| SHA-256 指纹 | `sha256:<credential-fingerprint>` | 第 4.3 节第一条结果 |

创建成功后状态应为 `active`、指纹版本应为 `1`。如果提示指纹不匹配，先检查文件名、目录归属和是否误带换行，不要关闭完整性校验。

## 7. 创建客户并绑定 new-api 用户

### 7.1 创建客户

进入 **客户** → **新建客户**：

| 字段 | 示例 |
|---|---|
| 客户代码 | `NEXUS-INTERNAL` |
| 显示名称 | `REACH NEXUS` |
| 计费用户 ID | `1` |

客户代码会规范化为小写存储，例如 `nexus-internal`。计费用户 ID 指向 new-api 用户，但固定月度套餐账单由 Workbench 控制面维护，不按每次 Turn 从 new-api 余额自动扣款。

### 7.2 添加成员

进入客户详情 → **成员** → **添加成员**：

| 字段 | 示例 |
|---|---|
| new-api 用户 ID | `1` |
| 角色 | `owner` |

同一个 new-api 用户可以属于多个客户；用户进入 Agent Store 时只会看到当前有效客户、成员关系、套餐、部署和 entitlement 的交集。禁用成员会增加授权版本并撤销现有工作台授权。

## 8. 配置并验证 Customer App

进入客户详情 → **Application 配置** → **保存**。

### 8.1 动态 Claw 示例

| 字段 | 示例 |
|---|---|
| 预期行版本 | 新建时 `0`；修改时使用界面当前值 |
| 供应商环境 | `china_tencent_adp` |
| Region | `ap-guangzhou` |
| Space ID | `default_space` |
| App ID | `2085927381516339648` |
| 模板 Agent ID | `4959df3a-1f38-4906-be51-c8e2cd65eab0` |
| 凭据配置 ID | 选择 `nexus-internal-primary` |
| AppKey | 粘贴腾讯 ADP 提供的 AppKey；保存后立即清空且不回显 |
| 应用显示名称 | `REACH NEXUS Claw Workbench` |

如果配置的是 AppMode 1/2/3 或静态 Claw，模板 Agent ID 留空。

首次配置必须填写 AppKey。已经存在已验证配置时，该字段留空表示继续使用现有 AppKey；输入新值表示创建新的加密版本并替换。界面和管理 API 都不会返回明文、Secret 引用或指纹，普通超级管理员无需登录服务器。

### 8.2 推荐能力和限制

首次生产验证建议只打开已验收能力：

```text
能力：chat

customer_concurrency = 10
user_concurrency = 2
max_runtime_seconds = 300
max_reasoning_rounds = 20
max_output_tokens = 8192
web_search_per_turn = 0
max_file_bytes = 0
```

只有完成相应真实腾讯 E2E 和安全验收后，再打开 `files`、`web_search`、`tools`、`connectors`、`oauth`、`scheduled_tasks` 或 `sandbox`。界面隐藏不等于后端授权；实际能力取 App 配置、套餐、部署和运行时策略的交集。

### 8.3 验证和启用

1. 保存后会生成一个不可变的 pending config。
2. 点击 **验证**。
3. `配置版本` 使用界面自动显示的 pending config version，不要猜测。
4. 平台会调用腾讯接口验证 AppId、SpaceId、AppKey、AppMode、发布状态和条件化模板 Agent。
5. 验证成功后，再通过 **操作** → **启用** 激活 Customer App。

任何 Region、SpaceId、AppId、AppKey、凭据配置或模板 Agent 变化都会使旧验证失效，需要重新验证。仅修改 Agent Store 的展示名称、简介、标签或排序，不需要重新调用腾讯验证。

### 8.4 同一客户新增第二个及后续应用

客户已经存在主应用时，主应用外层按钮显示 **编辑**，弹窗底部才显示 **保存**。编辑只用于修改同一个 App 的配置；不要用它录入另一个 AppId。

新增不同腾讯 AppId 时：

1. 点击 **新增应用**。
2. 填写唯一应用别名、AppId、AppKey、SpaceId、Region、能力和限制。
3. 新应用首次必须选择主应用当前使用的同一凭据配置；后续更换凭据走独立审批。
4. 创建后在 **附加应用**中点击 **验证附加应用**。
5. 验证成功后点击 **附加应用操作** → **启用**。

新增、验证和启用都只作用于所选应用记录，不覆盖主应用，也不要求把附加应用设为主应用。只有确实要改变客户默认应用时才使用 **设为主应用**。

## 9. 创建固定月度套餐和有效周期

### 9.1 创建套餐版本

进入 **套餐** → **新建套餐**：

| 字段 | 示例 |
|---|---|
| 套餐代码 | `nexus-claw-basic` |
| 显示名称 | `REACH NEXUS Claw 基础套餐` |
| 月费（人民币） | `299.00` |
| 生效时间 | `2026-08-11 00:00`（北京时间） |
| 失效时间 | 留空，或填写合同到期时间 |
| 能力 | 至少 `chat` |
| 限制 | 不应高于 Customer App 对应限制 |

### 9.2 给客户创建套餐周期

进入客户详情 → **套餐周期** → **创建周期**：

| 字段 | 示例 |
|---|---|
| 套餐版本 | 选择刚创建的套餐版本 |
| 开始时间 | `2026-08-11 00:00` |
| 结束时间 | `2026-09-11 00:00` |

然后点击 **确认付款**：

| 字段 | 示例 |
|---|---|
| 套餐周期 | 选择待付款周期 |
| 付款证据引用 | `manual-bank-transfer-20260811-NEXUS-INTERNAL` |

当前计费模式是固定月度套餐：客户应收按已确认的套餐周期冻结；腾讯官网用量由管理员人工对账，作为内部成本证据，不自动改写客户账单，也不按每次对话扣 new-api 余额。

## 10. 上架第一个 Agent Store 应用

进入 **智能体商店** → 点击页面右上角 **上架应用**。现在所有操作都在同一个向导中完成，不再要求先去客户页创建 Customer App，也不再出现“部署启用”和“应用发布”两次启用。

统一流程只有四步：

1. 填写商店展示信息；
2. 填写腾讯 ADP 的 AppId、AppKey、Region、SpaceId 和条件化模板 Agent；
3. 选择“所有符合条件的客户”或“仅指定客户”，然后点击 **验证配置**；
4. 核对供应商读回预览，点击 **确认并上架**。

最终确认会在同一个数据库事务中同时激活运行配置、开启执行并发布目录条目。成功后应直接得到 `published / active / enabled`，不会再出现 `customer App changed after deployment verification`。

### 10.1 推荐填写示例

| 字段 | 示例 | 规则 |
|---|---|---|
| Slug | `reach-nexus-claw` | 3–64 位小写字母、数字和连字符；创建后作为稳定 URL 标识 |
| 名称 | `REACH NEXUS 智能工作台` | 最多 160 字符 |
| 简介 | `基于腾讯 ADP 的企业智能助手，支持持续对话与任务处理。` | 最多 500 字符 |
| 详细说明 | `面向 REACH NEXUS 成员的内部智能工作台。首期开放对话能力。` | 最多 20,000 字符 |
| 头像 URL | `https://gateway.nexus-reach.com/static/llm-brand/logo.png` | 可留空；填写时必须是 HTTPS |
| 分类 | `办公助手` | 用于客户侧筛选 |
| 标签 | `claw, 企业助手, 腾讯adp` | 逗号分隔，会去重并规范化 |
| 排序 | `10` | 数值越小越靠前；团队应统一规则 |
| 推荐 | 勾选 | 在目录中显示为推荐应用 |
| 供应商环境 | `china_tencent_adp` | 腾讯 ADP Application |
| Region | `ap-guangzhou` | 以腾讯控制台实际值为准 |
| Space ID | `default_space` | 以腾讯控制台实际值为准 |
| App ID | `2048342527164967296` | 腾讯 Application AppId |
| AppKey | `<粘贴真实 AppKey>` | 只写；提交后加密且不回显 |
| 平台凭据 | `nexus-internal-primary` | 只显示配置名称，不显示 SecretId/SecretKey/引用 |
| 客户使用范围 | `所有符合条件的客户` | 当前和未来具备有效付费 Workbench 套餐的客户自动可见 |

管理员不能填写 AppMode、runtime profile 或供应商状态，这些值只能由腾讯验证接口读回。

### 10.2 客户使用范围

- **所有符合条件的客户**：所有状态正常、成员关系有效且当前有已付款 Workbench 套餐的客户都可以看到；未来新增客户也自动生效。
- **仅指定客户**：勾选一个或多个客户，只有这些客户中同时满足成员和套餐条件的用户可以看到。

腾讯 Application 可以被多个客户共同使用，但对话、任务、文件、历史和授权仍使用登录用户的 `customer_id + new_api_user_id` 隔离。共享 AppId 不等于共享客户数据。

## 11. 验证预览

点击 **验证配置** 后，页面至少显示：验证结果、腾讯 AppMode、映射后的 runtime profile、腾讯发布状态和供应商应用名称。

- 验证失败时不会留下占用相同 Slug 或 AppId 的草稿；AppKey 密文会立即标记撤销，修正表单后可以直接重试。
- 验证成功时仍不会立即对客户可见；只有点击 **确认并上架** 才会发布。
- AppKey、SecretId、SecretKey、Secret 引用、指纹和供应商 RequestId 均不显示。

## 12. 确认并上架

验证预览正确后点击 **确认并上架**。服务端使用验证时返回的行版本进行并发校验，并在一个事务中完成：

1. 将供应商运行配置设为 `active`；
2. 把验证后的 AuthEpoch 固化到 deployment；
3. 将 deployment 设为 `active` 且 `execution_enabled=true`；
4. 发布不可变目录版本，将目录状态设为 `published`；
5. 写入管理审计和缓存失效事件。

任一步失败都会整体回滚，不会出现应用已启用但目录未发布，或目录已发布但执行仍关闭的半完成状态。

## 13. 客户如何使用 Agent Store

1. 用户登录 `https://gateway.nexus-reach.com`。
2. 左侧原 **Playground** 入口会显示为 **Agent Store/智能体商店**；旧 `/playground` 会跳转到 `/agent-store`。
3. 打开 `https://gateway.nexus-reach.com/agent-store`。
4. 可按分类和关键词搜索，点击应用卡片查看完整说明。
5. 点击 **启动**。
6. 服务端重新检查用户、客户、成员角色、套餐、Customer App、部署、配置版本、授权版本和 entitlement。
7. 校验通过后签发 60 秒、单次使用、绑定浏览器会话和完整 App 快照的启动票据，并进入工作台。

浏览器不能在启动请求中覆盖 Customer ID、AppId、AppMode、AgentId、SpaceId 或 AppKey。用户刷新或重复使用旧启动票据会被拒绝，这是正常安全行为。

动态 Claw 模式下，每个用户的 Agent、Conversation、任务和历史记录彼此隔离；同一客户的不同用户不会共享对话记录。AppMode 1/2/3 和静态 Claw 不复制用户 Agent，但 Conversation 所有权和历史仍按规范化用户身份隔离。

## 14. 日常维护

### 14.1 修改目录应用文字、分类、标签或排序

点击目录应用的 **编辑**。修改会创建新的不可变目录版本，不改变客户部署、entitlement、执行开关或腾讯 Application。保存后按当前状态重新发布新版本。

### 14.2 修改腾讯 App 配置

1. 在客户详情保存新的 App pending config。
2. 如果更换 credential profile，先发起 `app_credential_change` 双人审批。
3. 第二名管理员在 **治理** 页面批准并执行。
4. 验证新的 App config。
5. 重新验证所有引用该 App 的 Agent Store 部署。
6. 核对新的 runtime profile 后再开启执行。

不能只改服务器 Secret 文件而保留旧指纹。标准轮换流程是：写入新版本 Secret 文件 → 计算新指纹 → 暂存凭据轮换 → 双人审批 → 激活 → 更新 App config → 验证 → 淘汰旧版本。

### 14.3 下架、停用和归档

| 操作 | 影响 | 恢复方式 |
|---|---|---|
| 停止执行 | 仅关闭某个客户部署执行；目录应用仍可保留 | 部署状态仍有效时可再次启用 |
| 下架应用 | 从目录隐藏并禁止新启动；部署 `active` 回到 `verified` | 点击发布 |
| 停用单个部署 | 只影响该客户，撤销其活动授权，执行关闭 | 必须重新验证，再显式启用 |
| 停用应用 | 隐藏全部客户部署并撤销活动授权 | 不支持直接恢复；按治理流程重新建版本/部署 |
| 归档应用 | 只允许从 draft、rejected、unpublished 进入；作为终态保留审计 | 不恢复，重新上架应用 |

紧急事故优先“停用单个部署”或“下架应用”，并填写清晰原因，例如：

```text
provider credential suspected compromised; incident INC-2026-0811
```

## 15. 状态判断速查

### 15.1 目录应用状态

```text
draft -> verifying -> verified -> published -> unpublished
  |          |           |                         |
  +------> rejected -----+----------------------> archived
published/unpublished/draft/rejected -> disabled（按允许的管理动作）
```

- `draft`：仅管理端可见。
- `verifying`：正在调用腾讯；5 分钟内不要重复点击。
- `rejected`：腾讯验证或安全校验失败。
- `verified`：至少一个部署已验证，可发布。
- `published`：满足授权交集的客户用户可见。
- `unpublished`：已下架，可重新发布。
- `disabled` / `archived`：终止或归档状态。

### 15.2 部署状态

- `draft`：未通过可信验证。
- `verified`：可信快照有效，但目录应用未发布或已下架。
- `active`：目录应用已发布，部署可参与目录和启动判断。
- `disabled`：该客户部署已停用，必须重新验证。

`execution_enabled=true` 仍不等于一定可启动；目录应用、部署、Customer App、成员、套餐和 entitlement 必须同时有效。

## 16. 管理 API 参考

推荐日常操作使用新版管理界面。以下 API 用于自动化和排障；所有管理写请求都必须使用同源管理员 Session、双提交 CSRF 和最近重新认证，静态 Bearer token 会被拒绝。

### 16.1 用户侧接口

| 方法 | Endpoint | 用途 |
|---|---|---|
| GET | `/api/workbench/agent-store/status` | 查询功能开关，响应 `{"success":true,"data":{"enabled":true}}` |
| GET | `/api/workbench/agent-store?category=&query=&cursor=` | 当前用户可见目录 |
| GET | `/api/workbench/agent-store/{slug}` | 当前用户可见目录应用详情 |
| POST | `/api/workbench/agent-store/{slug}/launch` | 启动，body 必须为空对象或空 body |

启动成功响应示例：

```json
{
  "success": true,
  "data": {
    "redirect_url": "/workbench/auth/sso?ticket=<one-time-ticket>",
    "expires_at": "2026-08-11T12:31:00Z"
  }
}
```

### 16.2 管理侧接口

| 方法 | Endpoint | 用途 |
|---|---|---|
| GET/POST | `/api/admin/workbench/agent-store/items` | 列表/上架目录应用 |
| GET/PATCH | `/api/admin/workbench/agent-store/items/{item_id}` | 详情/修改元数据 |
| POST | `/api/admin/workbench/agent-store/items/{item_id}/deployments` | 新增客户部署 |
| PATCH | `/api/admin/workbench/agent-store/items/{item_id}/deployments/{deployment_id}` | 更新执行开关和授权 |
| POST | `/api/admin/workbench/agent-store/items/{item_id}/deployments/{deployment_id}/verify` | 可信腾讯验证 |
| POST | `/api/admin/workbench/agent-store/items/{item_id}/deployments/{deployment_id}/disable` | 停用单个部署 |
| POST | `/api/admin/workbench/agent-store/items/{item_id}/publish` | 发布 |
| POST | `/api/admin/workbench/agent-store/items/{item_id}/unpublish` | 下架 |
| POST | `/api/admin/workbench/agent-store/items/{item_id}/disable` | 停用目录应用 |
| POST | `/api/admin/workbench/agent-store/items/{item_id}/archive` | 归档 |
| GET | `/api/admin/workbench/agent-store/items/{item_id}/audits` | 管理和启动审计 |

上架应用请求体示例：

```json
{
  "slug": "reach-nexus-claw",
  "display_name": "REACH NEXUS 智能工作台",
  "summary": "基于腾讯 ADP 的企业智能助手。",
  "description": "面向 REACH NEXUS 成员的内部智能工作台。",
  "avatar_url": "https://gateway.nexus-reach.com/static/llm-brand/logo.png",
  "category": "办公助手",
  "tags": ["claw", "企业助手", "腾讯adp"],
  "sort_order": 10,
  "featured": true,
  "deployment": {
    "customer_id": 1,
    "customer_app_id": 1,
    "execution_enabled": false
  },
  "entitlements": []
}
```

部署验证请求体示例：

```json
{
  "expected_version": 2,
  "expected_deployment_version": 1
}
```

更新授权和执行开关示例：

```json
{
  "expected_deployment_version": 2,
  "execution_enabled": true,
  "entitlements": [
    {
      "subject_type": "user",
      "subject_ref": "1",
      "valid_from": "2026-08-10T16:00:00Z",
      "valid_until": "2026-09-10T16:00:00Z"
    }
  ]
}
```

界面中的北京时间会转换为 ISO-8601 UTC；例如北京时间 `2026-08-11 00:00` 对应 `2026-08-10T16:00:00Z`。

## 17. 常见错误与处理

| 现象 | 常见原因 | 处理方法 |
|---|---|---|
| `/readyz` 提示 provider secret integrity not ready | Secret 目录不可搜索、文件 owner/mode 错、指纹版本旧或 Secret 被直接替换 | 检查目录 `10001:10001/0700`、文件 `10001:10001/0600`，运行 preflight；按轮换或受审计 re-enroll 流程处理 |
| 创建凭据时报 fingerprint mismatch | 文件内容与指纹不是同一版本，或文件带换行 | 重新在服务器计算指纹，不要在线哈希 |
| App 验证失败 | App 未发布、Space/AppKey 不匹配、动态 Claw 缺模板 Agent、AK/SK 无权限 | 回腾讯控制台核对后重新验证；不要伪造 AppMode |
| 目录应用停在 verifying | 正常验证尚未结束，或供应商调用中断 | 5 分钟内等待；超过 5 分钟可再次点击验证触发安全恢复 |
| 不能点击启用执行 | 部署未 verified/active，或 runtime profile 未识别 | 先验证并核对读回结果 |
| 发布时报 verified deployment required | 没有任何通过验证的客户部署 | 对至少一个部署执行验证 |
| 用户目录为空 | 未登录、成员/套餐/entitlement/部署任一无效，或目录应用未发布 | 按“成员 → 套餐 → App → 部署 → entitlement → 目录应用”顺序检查 |
| 用户点击启动返回 403/409 | 配置版本或授权版本变化、部署停用、票据过期或重复使用 | 刷新目录后重新启动；不要重放旧票据 |
| 管理写操作跳回安全验证 | 最近认证超过 15 分钟 | 使用密码、2FA 或 Passkey 完成 step-up |
| API 返回 row version changed | 另一个管理员已修改同一对象 | 刷新页面，核对最新状态后重做操作，不要覆盖版本号 |

## 18. 上线与变更检查清单

### 首次上架前

- [ ] 腾讯 Application 已发布且可运行。
- [ ] Region、SpaceId、AppId、条件化模板 Agent 已确认。
- [ ] AppKey 已通过 HTTPS 管理界面写入加密保险库；AK/SK 已由平台运维人员安全配置。
- [ ] Secret 目录和文件 owner/mode 正确，preflight 通过。
- [ ] credential profile 为 active、fingerprint version 为 1。
- [ ] 客户 active，new-api 用户成员关系 active。
- [ ] Customer App config verified，Customer App active。
- [ ] 套餐周期有效且付款已确认。
- [ ] Agent Store deployment verified，读回 AppMode/profile 正确。
- [ ] 只对已完成真实 E2E 的 profile 打开 execution。
- [ ] 目录应用 published，部署 active。
- [ ] 使用真实客户账号验证目录、启动、对话和历史隔离。
- [ ] 未授权用户看不到目录应用，公网 internal API 返回 404。

### 每次变更后

- [ ] 检查 Agent Store 管理审计和启动审计。
- [ ] 检查 `/api/workbench/agent-store/status` 仍为 enabled。
- [ ] 检查域名和公网 IP 的 `/api/status`。
- [ ] 运行部署 preflight。
- [ ] 创建并校验数据库备份，随后复制到加密、异机、受控存储。
- [ ] 保留旧 Blue/Green 镜像和数据库回滚证据，不执行 volume/image prune。

## 19. 当前 NEXUS-INTERNAL 首个上架应用建议

现有生产示例已经具备客户、用户 1、客户级腾讯凭据和已验证的动态 Claw Application。首次实际上架可直接从第 10 节开始，建议值如下：

```text
Slug: reach-nexus-claw
名称: REACH NEXUS 智能工作台
分类: 办公助手
标签: claw, 企业助手, 腾讯adp
排序: 10
推荐: 是
客户: REACH NEXUS (nexus-internal)
App: REACH NEXUS Claw Workbench
授权: 留空（默认整个客户），或 user / 1 做首轮小范围验收
```

建议先用 `user / 1` 做小范围验收，确认启动、对话、历史隔离和审计后，再把授权改为空列表，让服务端恢复默认客户级授权。

## 20. 完整实例：上架“高考志愿无忧”并交付用户

本节以腾讯 ADP Application **高考志愿无忧** 为例，使用下列已知信息：

| 项目 | 示例值 |
|---|---|
| 腾讯 ADP AppId | `2048342527164967296` |
| 应用显示名称 | `高考志愿无忧` |
| 示例客户 | `REACH NEXUS (NEXUS-INTERNAL)` |
| 首轮验收用户 | new-api 用户 ID `1` |
| Region | `ap-guangzhou`，如果腾讯控制台显示其他地域，以控制台为准 |
| SpaceId | `default_space`，保存前必须在腾讯控制台核实 |

先明确当前新版界面的操作顺序和按钮名称：

```text
腾讯 ADP 发布应用
→ 在客户页配置并验证 Customer App
→ 智能体商店点击“上架应用”
→ 验证客户部署
→ 开启执行
→ 点击“发布”
→ 授权用户在智能体商店看到并启动
```

因此，你在界面中看到的 **上架应用** 是正确入口，手册不再使用界面上不存在的“新建商品”。但是不能把“上架应用”放到 Customer App 配置之前：上架表单只允许从下拉框选择一个已经配置并验证的 Customer App，不提供 AppId 或 AppKey 输入框。客户已有其他 AppId 时，必须使用客户页的 **新增应用**，不能点 **编辑** 后把原 AppId 改掉。**上架应用**创建的是目录草稿；后面的**发布**才会让授权用户看见。

以下三项不能根据 AppId 推算，操作前必须准备：

1. **高考志愿无忧的 AppKey**：从腾讯 ADP 该 Application 的接入配置中取得。它只在 Customer App 表单中输入一次，不要写入目录介绍、聊天记录或工单。
2. **腾讯访问凭据配置**：示例选择平台已经建立的 `nexus-internal-primary`。普通超级管理员只选择配置名称，不需要查看或重新填写 SecretId/SecretKey。
3. **模板 Agent ID（条件必填）**：只有腾讯验证读回为动态 Claw，即 `AppMode 4 / claw_dynamic_v2` 时才需要。AppMode 1、2、3 或静态 Claw 均留空。

### 20.1 第一步：确认腾讯 ADP 应用已发布

登录腾讯 ADP 控制台，打开 AppId `2048342527164967296` 对应的 **高考志愿无忧**，逐项确认：

| 检查项 | 应满足的条件 |
|---|---|
| 应用状态 | 已发布、可通过 AppKey 调用 |
| AppId | `2048342527164967296` |
| AppKey | 已创建并可用 |
| Region | 记录腾讯控制台显示的真实地域 |
| SpaceId | 记录真实值；若显示 `default_space`，后续填写 `default_space` |
| AppMode | 记录腾讯读回模式；平台验证时还会再次核对 |
| 模板 Agent ID | 仅动态 Claw 记录；其他模式不需要 |

运行模式由平台通过腾讯接口验证和固化，管理员不能在 Agent Store 的“上架应用”表单中自行填写或伪造：

| 腾讯应用类型 | 平台运行模式 | 模板 Agent ID |
|---|---|---|
| AppMode 1 | `standard_v2` | 留空 |
| AppMode 2 | `multi_agent_v2` | 留空 |
| AppMode 3 | `workflow_v2` | 留空 |
| AppMode 4，静态 Claw | `claw_static_v2` | 留空 |
| AppMode 4，动态 Claw | `claw_dynamic_v2` | 必填腾讯模板 Agent ID |

### 20.2 第二步：进入客户的 Application 配置

1. 以 new-api 超级管理员登录 `https://gateway.nexus-reach.com`。
2. 打开 **智能工作台管理**；也可以直接访问 `https://gateway.nexus-reach.com/workbench/admin`。
3. 如果系统提示“需要重新验证”，按页面提示使用密码、Passkey 或已启用的二次认证完成验证。普通登录成功不等于已经完成敏感操作的近期验证。
4. 左侧点击 **客户**。
5. 打开 `REACH NEXUS (NEXUS-INTERNAL)`。如果该应用实际属于其他客户，应在这里选择真实客户，不能为了方便复用其他客户的 App 配置。
6. 在客户详情页找到 **应用配置**：
   - 如果客户从未配置过任何应用，点击 **创建** 建立首个主应用；
   - 如果页面已经显示原来的 AppId，点击 **新增应用**，不要点击 **编辑** 去替换原应用。

现有示例客户 ID 为 `1` 时，可直接打开：

```text
https://gateway.nexus-reach.com/workbench/admin#/customers/1
```

页面上两个按钮的含义不同：

| 按钮 | 用途 | 是否创建新应用记录 |
|---|---|---|
| **编辑** | 修改当前主应用的 Region、SpaceId、AppKey、能力或限制；已验证 AppId 不允许原地更换 | 否 |
| **新增应用** | 为同一客户增加另一个腾讯 ADP AppId | 是 |

### 20.3 第三步：通过“新增应用”填写高考志愿无忧

本例客户 `REACH NEXUS` 已有其他应用，所以点击 **新增应用**，按下表填写：

| 界面字段 | 本例填写内容 | 说明 |
|---|---|---|
| 应用别名 | `gaokao-zhiyuan-wuyou` | 3–80 位小写字母、数字和连字符；用于稳定识别该应用 |
| 预期行版本 | `0` | 新增应用时由界面固定为 0，不要修改 |
| 供应商环境 | `china_tencent_adp` | 本例是腾讯 ADP Application |
| Region | `ap-guangzhou` | 如果腾讯控制台不是该地域，填写控制台实际值 |
| Space ID | `default_space` | 必须以腾讯控制台实际值为准 |
| App ID | `2048342527164967296` | 不要填写目录条目 ID 或 Agent ID |
| 模板 Agent ID | 留空，除非确认是动态 Claw | 动态 Claw 时填写腾讯提供的模板 Agent ID |
| 凭据配置 | `nexus-internal-primary` | 选择界面中已存在、环境匹配且有权访问该 App 的配置 |
| AppKey | `<粘贴高考志愿无忧的真实 AppKey>` | 首次必填；使用密码输入框提交，保存后不回显 |
| 应用显示名称 | `高考志愿无忧` | 这是管理端 Customer App 名称 |
| 能力 | 先只勾选 `chat` | 其他能力通过真实验收后再打开 |
| customer_concurrency | `10` | 示例值，可按合同调低 |
| user_concurrency | `2` | 示例值，可按合同调低 |
| max_runtime_seconds | `300` | 单次运行最多 5 分钟的示例值 |
| max_reasoning_rounds | `20` | 示例值 |
| max_output_tokens | `8192` | 示例值 |
| web_search_per_turn | `0` | 未验收联网搜索时保持 0 |
| max_file_bytes | `0` | 未验收文件能力时保持 0 |

点击弹窗底部 **创建**。成功后应看到：

- 原来的主应用仍保持原 AppId 和原状态；
- **附加应用**列表新增 `gaokao-zhiyuan-wuyou`；
- 新记录的 App ID 为 `2048342527164967296`，状态为 `draft`；
- 新记录出现待验证配置版本；
- AppKey 输入框重新显示为空，这是正常的安全设计，不代表 AppKey 丢失；
- 后端只保存加密值，不向浏览器返回明文、Secret 引用或指纹。

新增应用使用独立数据库记录和独立配置版本，不会覆盖原主应用。主应用外层按钮现在显示 **编辑**，弹窗底部的 **保存**仅提交对该主应用的配置修改。

### 20.4 第四步：验证并启用 Customer App

1. 在 **附加应用**区域点击 **验证附加应用**。
2. 选择 `高考志愿无忧 · 2048342527164967296`。
3. **预期行版本**和**配置版本**使用页面自动带出的只读值，不要手工猜测。
4. 提交验证，等待腾讯接口读回结果。
5. 核对读回的腾讯应用名称是 **高考志愿无忧**，AppId 是 `2048342527164967296`。
6. 核对 AppMode 和 runtime profile 与腾讯实际应用一致。
7. 验证成功后，点击 **附加应用操作**，选择该应用和 **启用**，再点击 **保存**。不需要把它设为主应用。

完成标准：附加 Customer App 状态为 `active`，并且存在当前配置版本；原主应用保持不变。此时高考志愿无忧才会出现在 Agent Store **上架应用**表单的 **Customer App** 下拉框中。

常见验证失败原因：

| 现象 | 处理方法 |
|---|---|
| AppId 找不到 | 核对供应商环境、Region、SpaceId 和 AppId |
| AppKey 无效 | 回腾讯 ADP 重新取得该 Application 的 AppKey，然后在配置表单中替换 |
| 无访问权限 | 让运维核对所选凭据配置对应的腾讯账号是否有权访问该 Space/App |
| 动态 Claw 缺模板 Agent | 填写该 Application 的模板 Agent ID 后重新验证 |
| 应用未发布 | 先在腾讯 ADP 发布应用，再回平台验证 |

### 20.5 第五步：确认客户、成员和套餐可用

在 `REACH NEXUS` 客户详情页确认：

1. 客户状态为 `active`。
2. new-api 用户 ID `1` 已添加为有效成员；首轮验收可使用 `owner` 或 `admin` 角色。
3. 客户存在已确认付款、当前时间有效的套餐周期。
4. 套餐至少包含 `chat`，并且套餐限制不高于 Customer App 限制。

如果需要新建一套演示套餐，可使用以下示例，但价格、期限必须按实际商业合同调整：

```text
套餐代码：gaokao-assistant-basic
显示名称：高考志愿无忧基础套餐
月费：299.00 元（仅示例）
能力：chat
周期开始：2026-08-12 00:00（北京时间）
周期结束：2026-09-12 00:00（北京时间）
付款证据引用：manual-payment-gaokao-20260812
```

创建周期后还要点击 **确认付款**。只有创建周期但未确认付款，不满足用户启动条件。

### 20.6 第六步：点击“上架应用”并创建首个客户部署

1. 左侧点击 **智能体商店**。
2. 点击页面右上角 **上架应用**。
3. 在同一个表单中填写目录展示资料，并选择首个客户及 Customer App。

推荐填写内容：

| 界面字段 | 本例填写内容 |
|---|---|
| Slug | `gaokao-zhiyuan-wuyou` |
| 名称 | `高考志愿无忧` |
| 简介 | `面向高考考生和家长的志愿填报辅助智能体。` |
| 详细说明 | `结合用户提供的成绩、地区、选科和专业偏好，辅助整理院校与专业选择。结果仅供信息参考，最终填报请结合官方招生政策与个人情况确认。` |
| 头像 URL | 留空，或填写该应用自有的 HTTPS 图片地址 |
| 分类 | `教育` |
| 标签 | `高考, 志愿填报, 升学规划` |
| 排序 | `20` |
| 推荐 | 首轮验收不勾选；确认稳定后可勾选 |
| 客户 | `REACH NEXUS (NEXUS-INTERNAL)` |
| Customer App | `高考志愿无忧 | 2048342527164967296 | active` |

点击 **创建**。创建动作会同时生成：

- 一个状态为 `draft` 的目录应用；
- 目录应用的第一个客户部署；
- 该部署默认关闭执行；
- 授权列表为空时，服务端创建“该部署所属客户可用”的默认授权。

这里的 **上架应用**并不等于已经对用户发布。AppId 和 AppKey 也不在这个表单中再次填写；目录应用只引用上一阶段已经保存并验证的 Customer App，避免把密钥混入展示信息。

### 20.7 第七步：把首轮访问限制给用户 1

为了先小范围验收，建议不要直接开放给客户全体成员：

1. 在 **高考志愿无忧** 目录应用卡片找到客户部署。
2. 点击 **管理部署**。
3. 删除默认的客户级授权（如果界面当前显示该行）。
4. 点击 **添加授权**，填写：

| 字段 | 填写内容 |
|---|---|
| 类型 | `user` |
| 引用 | `1` |
| 有效开始 | 留空，表示立即生效；或填写 `2026-08-12 00:00` |
| 有效结束 | 留空；需要限期验收时填写实际结束时间 |

5. 点击 **保存**。

这里的 `1` 是 new-api 用户 ID，不是客户 ID、AppId 或 AgentId。以后正式开放时，可改用以下方式之一：

- `customer / <客户ID>`：该客户的所有有效成员可见；
- `role / member`：只对指定客户角色开放；
- `plan / <套餐版本ID>`：只对购买指定套餐版本的成员开放。

多个授权之间是“任一匹配即可”。

### 20.8 第八步：验证部署、开启执行并发布

严格按下面的顺序操作：

1. 在部署卡片点击 **验证**。
2. 等待部署状态变成 `verified`。
3. 核对读回信息：
   - 腾讯应用名称：`高考志愿无忧`；
   - AppId 对应 Customer App：`2048342527164967296`；
   - AppMode 和 runtime profile 与腾讯控制台一致；
   - 配置版本等于当前 Customer App 的已验证版本；
   - 能力至少包含 `chat`。
4. 点击部署的 **启用**，或在 **管理部署** 中勾选执行开关并保存。
5. 确认执行显示为 `enabled`。
6. 点击目录应用级 **发布**。
7. 刷新页面，确认：
   - 目录应用状态：`published`；
   - 部署状态：`active`；
   - 执行状态：`enabled`；
   - 授权列表包含 `user / 1`。

以上四项缺一，用户侧都可能看不到应用或无法启动。当前版本允许对已发布目录应用重新验证部署，验证成功后会保持 `active`；如果重新验证发现腾讯配置发生变化，仍应先核对结果再继续提供服务。

### 20.9 第九步：用授权用户完成端到端验收

1. 退出超级管理员账号，或使用无管理员权限的独立浏览器会话。
2. 使用 new-api 用户 ID `1` 对应的账号登录 `https://gateway.nexus-reach.com`。
3. 打开左侧 **智能体商店**，或直接访问：

```text
https://gateway.nexus-reach.com/agent-store
```

4. 搜索 `高考志愿无忧` 或筛选分类 `教育`。
5. 点击应用卡片查看简介和详细说明。
6. 点击 **启动**。
7. 平台会在服务端重新检查用户、客户成员关系、套餐、Customer App、部署、配置版本、执行开关和 entitlement。
8. 校验通过后，浏览器通过一次性 SSO 票据跳转到智能工作台。无需、也不能让用户填写 AppId、AppKey、SpaceId 或 AgentId。
9. 输入首条验收消息，例如：

```text
我是广东物理类考生，想了解计算机相关专业。请先告诉我还需要提供哪些信息，不要直接承诺录取结果。
```

10. 确认能够收到应用响应，并检查新建会话、继续对话和历史记录是否都只属于当前用户。

端到端完成标准：用户可以在商店看到 **高考志愿无忧**，点击后进入绑定 AppId `2048342527164967296` 的工作台，成功完成一轮对话；另一个未授权用户看不到该应用或启动时被拒绝。

### 20.10 发布后看不到应用的逐项检查

按下列顺序检查，避免反复修改 AppKey：

| 检查位置 | 正确状态 | 错误时的影响 |
|---|---|---|
| 目录应用 | `published` | 非发布状态不会进入用户目录 |
| 客户部署 | `active` | `draft`、`verifying`、`verified`、`rejected` 或 `disabled` 均不可供用户启动 |
| 执行开关 | `enabled` | 目录应用可能存在，但启动会被拒绝 |
| Customer App | `active` 且配置版本未失效 | App 被暂停、禁用或重新配置后，旧部署不可执行 |
| 客户 | `active` | 客户停用后不展示、不启动 |
| 成员 | 用户 ID `1` 的成员关系有效 | entitlement 命中也不能绕过成员校验 |
| 套餐周期 | 已确认付款且当前有效 | 只有套餐定义、没有有效周期仍不可用 |
| 套餐能力 | 包含 `chat` | 能力交集为空时不能运行 |
| Entitlement | `user / 1`，且时间有效 | 引用错 ID 或已过期会导致不可见 |
| 登录账号 | 确实对应 new-api 用户 ID `1` | 使用其他账号测试不会命中授权 |

修改授权、成员、套餐或应用状态后，让测试用户刷新 Agent Store 页面；不要复用旧的 60 秒启动链接。旧票据是单次使用并绑定原浏览器会话，重复打开被拒绝属于正常行为。
