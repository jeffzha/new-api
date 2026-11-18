# 模型广场接口补充说明

版本：2026-11-18，V1.8。补齐模型广场组合筛选、筛选项统计和价格展示配置。本次仅完成本地开发和验证，尚未发布到国内 /test、联通或产品环境。

## 1. 接口分工

| 接口 | 用途 | 返回内容 |
| --- | --- | --- |
| `GET /v1/models` | SDK、模型选择器使用的可用模型目录 | 保留已有响应；支持原有 `model_type` 和 `include=pricing` |
| `GET /v1/model-marketplace` | 模型广场卡片和筛选栏 | 分页模型、描述、标签、供应商、类型、端点和标准标价规则 |
| `GET /v1/model-marketplace/detail?model=模型ID` | 用户打开某个模型的详情 | 一个模型的完整公开价格规则、适用的用量示例和材料定价 |

`/v1/models` 包含当前 Key 可见的全部模型类型，并非只有 LLM。`POST /v1/embeddings` 是向量调用接口，不是模型目录接口。

原文档中 `/v1/models?include=pricing` 的 `pricing.sales_multiplier` 仍是平台默认销售系数。本补充接口的 `pricing` 是独立结构，表示标准标价，不能把两个结构混用。

## 2. 地址和认证

下面路径均以 `/v1` 开始。部署后，国内项目环境在域名后加 `/test`；联通项目环境按已有入口访问，不加 `/test`。本次未部署，因此这些新增路径目前不能作为线上验收结果。

```http
GET /v1/model-marketplace?page=1&page_size=20&model_type=embedding
Authorization: Bearer <API_KEY>
```

使用与原 `/v1/models` 相同的 API Key 和认证中间件。无须申请另一种模型广场密钥。密钥只能由业务系统后端安全保存，不应放在公开网页代码中。

## 3. 可见模型保持一致

在相同环境、相同配置状态和相同 Key 下，未筛选的模型广场全部分页的 `data[].id` 集合与 `/v1/models` 相同。排序和响应字段不要求相同。

可见性依据启用渠道、分组、Key 的模型范围、平台计费配置及模型公开状态共同判定。仅创建模型资料而未配置可用渠道，不会使模型出现在目录。已有自用模式和上游直接计费等例外继续遵循原规则。隐藏模型同时从这两个目录排除。

新增或修改配置后按现有渠道及模型资料缓存机制更新；模型资料缓存通常约一分钟，不承诺不同实例在修改瞬间同时刷新。调用权限仍由实际调用接口再次校验，目录可见不代表绕过余额或路由检查。

## 4. 分页列表

### 请求参数

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `page` | 否 | 从 1 开始，默认 1，最大 1000000 |
| `page_size` | 否 | 默认 20，最大 100 |
| `q` | 否 | 模型 ID、展示名、描述、供应商、标签、端点类型或路径包含匹配，大小写不敏感，最长 200 字节 |
| `model_type` | 否 | 单个类型，大小写不敏感 |
| `vendor` | 否 | 供应商名称精确匹配，大小写不敏感，最长 200 字节 |
| `group` | 否 | 当前 Key 可见的路由分组，精确匹配且区分大小写，最长 200 字节 |
| `tag` | 否 | 单个模型标签精确匹配，大小写不敏感，最长 200 字节 |
| `pricing_type` | 否 | `token` 按量、`request` 按次、`task` 任务、`unavailable` 尚无可分类价格资料 |
| `endpoint_type` | 否 | 单个端点类型精确匹配，大小写不敏感，最长 200 字节；选项取自 `facets.endpoint_types` |
| `sort` | 否 | `name` 默认按模型 ID 升序；`name_desc` 降序 |

类型值：`llm`、`rerank`、`embedding`、`image`、`video`、`speech2text`、`tts`、`audio`。支持图片输入的对话模型仍属于 `llm`。未知自定义模型默认识别为 `llm`，应配置正确端点。

所有已传参数必须只有一个非空值。重复参数、未知参数、越界页码、非法类型均返回 400。未选择筛选条件时省略参数，不要传 `q=`。查询值按 URL 编码；中文长度限制按 UTF-8 字节计算。

不同条件之间按 AND 组合，每个条件一次选择一个值。例如从筛选项取得值后，查询 `?vendor=Example&group=default&model_type=embedding&tag=embedding&pricing_type=token&endpoint_type=embeddings&page=1&page_size=20`。未知供应商、分组、标签或端点值返回 200 空列表；无权分组同样为空，不扩大 Key 权限。全部选项用省略参数表示，不发送字面值 `all`。切换筛选时页码重置为 1。

### 响应示例

以下是项目环境命中 OPS 标价时的格式示例；价格和模型名仅用于说明结构。

```json
{
  "success": true,
  "object": "list",
  "page": 1,
  "page_size": 20,
  "total": 1,
  "has_more": false,
  "facets": {
    "model_types": {"embedding": 1},
    "vendors": {"Example": 1},
    "groups": {"default": 1},
    "tags": {"embedding": 1},
    "pricing_types": {"token": 1},
    "endpoint_types": {"embeddings": 1}
  },
  "display_options": {
    "default_price_mode": "standard",
    "price_modes": ["standard", "recharge"],
    "default_token_unit": "1M",
    "token_units": ["1M", "1K"],
    "token_unit_divisors": {"1M": 1, "1K": 1000},
    "recharge_available": true,
    "recharge_factor": 1,
    "sort_options": ["name", "name_desc"]
  },
  "data": [{
    "id": "example-embedding",
    "object": "model",
    "display_name": "example-embedding",
    "model_type": "embedding",
    "description": "示例向量模型",
    "icon": "",
    "tags": ["embedding"],
    "groups": ["default"],
    "pricing_type": "token",
    "vendor": {"id": 7, "name": "Example"},
    "endpoints": [{"type": "embeddings", "path": "/v1/embeddings", "method": "POST"}],
    "pricing": {
      "scope": "standard",
      "source": "ops",
      "status": "available",
      "currency": "CNY",
      "usd_exchange_rate": 7.3,
      "ratio_usd_per_million": 2,
      "version": "example-price-version",
      "stale": false,
      "display_rows": [{"when": {}, "prices": {"input_per_million": "0.5", "output_per_million": "0"}}]
    }
  }]
}
```

列表默认按模型 ID 字典序排列，支持 `sort=name_desc`。`total` 是本次筛选后总量，`has_more` 指示后续是否还有页面；没有结果或超过最后一页时返回 200 和 `data: []`。配置更新期间跨页查询不提供数据库快照保证，需要严格核对集合时在稳定配置窗口执行。

`facets` 是当前 Key 全部可见模型、应用本次任何筛选之前的计数，包含 `model_types`、`vendors`、`groups`、`tags`、`pricing_types`、`endpoint_types` 六项。首次列表请求即可构建全部筛选栏，无须拉全量分页或逐项查询。不是当前页数量，也不是其他条件联动后的剩余数量；未出现的选项可不显示，固定 UI 选项可补 0。

标签计数键统一小写，每个模型的同义大小写标签只计一次；模型 `tags` 保留首个展示写法。分组仅返回当前 Key 的有效路由范围与该模型启用分组的交集，自动分组 Key 按其实际允许组展开；内部组不对外展示。与登录管理页面相比，API Key 通常只允许其中一个组，因此可选组数量可能更少。多分组、多端点、多标签会使对应计数之和大于模型总数。

`display_options` 是标准价格的展示配置，列表和详情均返回。标准、充值以及 1M、1K 是前端展示开关，不是模型筛选；不向本接口传 `price_mode` 或 `token_unit` 参数。默认展示标准价与每百万 Token 单价。充值换算系数为聚合公开充值费率除以美元人民币汇率；充值展示价为同币种标准价乘 `recharge_factor`。费率无效时仅返回 `price_modes: ["standard"]`、`recharge_available: false`、`recharge_factor: null`，页面禁用充值选项，不能当作零元。这里只是平台默认充值费率的等价展示，不包含支付渠道、充值金额优惠、代理商或客户专属折扣。

1K Token 的展示单价等于 1M 单价除以 1000。例如 2 元/1M 显示为 0.002 元/1K。只换算按百万 Token 声明的价格项；按张、秒、次、积分和原始用量单位的规则不变，上下文分档阈值不变。表达式、币种和 `provider_pricing` 原始结构保持不变，不能直接对整个表达式或所有数值统一除以 1000。卡片/表格视图由前端控制，无须再次请求。

### 模型字段

| 字段 | 说明 |
| --- | --- |
| `id` | 调用接口正文中的 `model` 值；仍是模型字符串标识，本次不新增永久数字 ID |
| `display_name` | 当前以模型 ID 作为展示名称 |
| `model_type` | 与筛选参数相同的类型值 |
| `description`、`icon`、`tags` | 已配置的公开模型资料；缺少描述或图标可为空字符串，标签为数组 |
| `vendor` | 公开供应商资料；没有专门配置时使用渠道提供方名称作为回退，其数字 ID 可以为 0 |
| `endpoints` | 公开支持端点，含 `type`、`path`、`method`；可能为空数组 |
| `groups` | 当前 Key 可见且模型已启用的分组字符串数组；不含其他组及组内折扣 |
| `pricing_type` | 与筛选参数相同，按量、按次、任务互斥；存在自定义用量字段时归为任务，否则按现有按量/按次类型归类 |
| `pricing` | 见下一节的标准标价结构 |

`endpoints[].type` 按实际端点配置返回，不能仅凭上例推断所有类型枚举。`path` 是网关公开相对路径，客户端需按环境补齐域名与 `/test` 前缀。不会返回上游服务器地址、渠道密钥、渠道 ID、内部插件标识、客户折扣和结算成本。

本接口不返回实时延迟、成功率等监控数据；无需为展示静态模型卡片持续轮询调用监控接口。

## 5. 标价字段与回退规则

| 字段 | 说明 |
| --- | --- |
| `scope` | 固定 `standard`，标准标价，不含代理商或客户销售折扣 |
| `source` | `ops`：OPS 缓存标价；`gateway`：聚合本地标价 |
| `status` | `available` 或 `unavailable`；不可用不能当成免费 |
| `currency` | 当前主价格结构使用的货币，`CNY` 或 `USD` |
| `usd_exchange_rate` | 当前聚合配置的美元兑人民币汇率，1 USD 对应的 CNY |
| `ratio_usd_per_million` | 旧倍率模型的输入价格换算因子，当前为 `1000000 / quota_per_unit` |
| `version` | 可选，OPS 价格快照版本；不代表本地计费配置版本 |
| `stale` | OPS 最近刷新失败或快照检查时间超出既定阈值时为 true，提示缓存状态 |
| `display_rows` | 命中 OPS 时返回的人民币标价行，价格为十进制字符串 |
| `config` | 聚合本地标价规则；命中 OPS 时不返回该字段，避免同一对象混用不同币种 |

产品分支默认使用本地标价。项目分支先读取现有 OPS 官方标价内存快照；该模型缺项、歧义、规则暂不支持或换算配置不一致时回退本地。OPS 刷新失败时保留最近成功快照并标记 `stale`，请求不会等待 OPS 网络返回。后台刷新周期沿用已有 OPS 配置。

本地也没有可用标价时，返回 `status: unavailable` 并省略价格规则，页面显示“暂未提供价格”。只有明确价格项为 `0` 才表示该项免费。价格读取与展示不会改写数据库价格、消费金额或余额。

### OPS 的 display_rows

每行由 `when` 和 `prices` 组成。`when: {}` 是默认规则。条件行已按 OPS 匹配优先级排序；价格继承默认项，返回的每行包含合并后的价格项。前端应完整展示条件，不能只取第一行当所有场景的统一价格。

支持的条件包括 `inputMax`（上下文长度上限）、`referenceVideo`（是否含参考视频）、`resolution`、`size`、`quality`、`operation`、`period`。条件必须一起满足；`inputMax` 按小于等于判断。

| prices 键 | 人民币单位 |
| --- | --- |
| `input_per_million`、`output_per_million` | 元 / 百万输入、输出 Token |
| `cache_read_per_million`、`cache_write_per_million` | 元 / 百万缓存读取、写入 Token |
| `audio_input_per_million`、`audio_output_per_million` | 元 / 百万音频输入、输出 Token |
| `per_image`、`per_reference_image` | 元 / 张生成图、参考图 |
| `per_second`、`per_reference_video_second` | 元 / 秒生成视频、参考视频 |
| `per_million_tokens` | 元 / 百万对应计费 Token |
| `per_output_credit`、`per_material_credit` | 元 / 输出积分、材料积分 |
| `per_request` | 元 / 次 |

### 本地的 config

沿用聚合模型广场的原价格规则，保留分档表达式、音频、缓存、图片和视频等结构，不把复杂规则强行压成一个单价。

- `billing_mode: tiered_expr` 时，`billing_expr` 是完整规则。Token 变量系数使用 USD / 百万 Token；`fixed()` 使用 USD / 次；自定义 `u()` 用量按 `billing_usage_schema` 声明的原始单位计量。不得把整个表达式统一再除以一百万。
- 旧倍率规则中，输入 USD / 百万 Token 为 `model_ratio × ratio_usd_per_million`，文本输出再乘 `completion_ratio`；缓存、图片输入和音频输入分别乘 `cache_ratio`、`image_ratio`、`audio_ratio`。音频输出是输入基价再乘 `audio_ratio × audio_completion_ratio`。缺失项和显式 0 必须区分。
- `quota_type: 1` 的旧按次规则中，`model_price` 为 USD / 次；复杂规则仍优先按其模式解析。
- `provider_pricing` 自带 `currency`、`unit`、`kind`、`tiers`，必须使用其自身币种及计量单位。不能强行套用外层 USD。
- 详情可能包含 `billing_material_pricing`，同样按自身币种和材料单位展示；`billing_plugin_variants` 是多提供方规则变体，不是另收一次费用。

此接口用于价格展示。实际扣款、代理商或客户专属销售系数及最终费用，仍走现有计费与费用回执流程。调用方不应拿模型广场显示值覆盖财务账本。

## 6. 单模型详情

```http
GET /v1/model-marketplace/detail?model=example-embedding
Authorization: Bearer <API_KEY>
```

只接受 `model` 参数，精确匹配 ID，区分大小写；名称带 `/` 时请进行 URL 编码。返回 `success: true`、`object: model`、`data: {模型对象}`，同级返回 `display_options`。字段与列表相同，本地规则增加完整用量示例、材料定价及适用的多提供方规则。OPS 仍使用其完整条件标价行。

不存在或当前 Key 无权查看的模型，统一返回 404 `model_not_found`。参数不合法返回 400 `invalid_catalog_query`。不通过错误信息区分“存在但无权查看”和“不存在”。

## 7. 缓存与调用建议

返回 `ETag`、`Cache-Control: private, no-cache`。保存每个请求 URL、每个 Key 对应的响应及 ETag，下一次发相同请求时带：

```http
If-None-Match: "上一次响应中的ETag"
```

内容未变返回 304，无正文，调用方继续使用原缓存；内容变化返回 200 和新内容。304 之前仍执行认证与可见性检查，不会绕过 Key 权限。ETag 已向浏览器跨域响应暴露。网关仍需完成权限检查与数据组装，ETag 主要减少重复响应传输，不能理解为零数据库开销。

推荐首次取一页列表，使用响应内 facets 构建筛选项；搜索加防抖，翻页只取对应页；打开详情时才取该模型完整信息。列表和详情都无需等待 OPS 实时取价，不需为了卡片每隔几秒轮询。若需要全量导入，按 `has_more` 顺序获取所有页并按 ID 去重。Key 变化或退出登录后清除对应缓存，禁止跨用户共用私有结果。

## 8. 状态码

| HTTP 状态 | 含义 |
| --- | --- |
| 200 | 查询成功，无匹配时为空列表 |
| 304 | ETag 匹配，使用原缓存 |
| 400 | 参数不合法，`invalid_catalog_query` |
| 401 | Key 缺失或无效，沿用原认证错误格式 |
| 404 | 详情模型不存在或不可见，`model_not_found` |
| 503 | 模型目录依赖暂不可用，`catalog_unavailable`；价格缺项本身不会触发 503 |

其他认证限制继续沿用已有网关中间件。
