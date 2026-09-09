# Seedance 2.0 公网 IP 端到端验证报告

## 1. 验证结论

- 验证入口：`https://124.174.0.221`
- 验证日期：2026-07-18（Asia/Shanghai）
- 付费全链路后端实现：`d7542aa4`
- 随后部署并完成零费用复验的文档/素材代理实现：`453829ad`
- 当前生产后端与对外文档实现：`700948c4`
- 当前对外文档版本：`2026-07-19.2`
- 真人认证回调安全配置基线：`22be42a4`
- 鉴权方式：`Authorization: Bearer <Nexus Reach API Key>`
- Seedance 国内客户契约接口数量：9 个（服务器保留的其他供应商通用 new-api 路由不属于本契约）
- 付费视频生成次数：1 次
- 付费验证规格：720p、4 秒、无输入视频、无音频、使用素材库图片作为参考图
- 视频任务：`task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj`
- 结果：9 个公网 operation 均有真实路由证据，其中 8 个已完成业务成功分支 E2E；视频创建、两种状态查询、完整下载、Range 下载、素材组创建、图片素材创建和素材查询均通过。
- 唯一未完成的业务成功分支：`GetVisualValidateResult` 要求已授权本人在 120 秒内完成人工活体认证；当前仅验证了会话创建成功和无效 BytedToken 的业务失败分支，不能把预检表述为成功分支通过。

公网 IP 使用受信任的 IP SAN HTTPS 证书，客户端不需要 `-k`。域名 `gateway.nexus-reach.com` 的应用路由和证书配置已完成，但当前仍可能受备案/网络放行状态影响；在该问题解除前，以公网 IP HTTPS 入口为准。

部署后的零费用复验结果：`GetAsset` 返回 HTTP 200、`state=1`、`Status=Active`；100×100 图片的 `CreateAsset` 负例返回 HTTP 400 和 `InvalidParameter.WidthTooSmall`；两条素材响应均没有供应商 `Set-Cookie`；已有视频的 1-byte Range 请求返回 HTTP 206、`Content-Range: bytes 0-0/1684544` 和 `Cache-Control: private, no-store`。

`22be42a4` 部署后的零费用复验结果：真人认证回调返回 HTTP 200、`Cache-Control: no-store`、`Referrer-Policy: no-referrer`、`X-Content-Type-Options: nosniff` 和限制第三方网络访问/嵌入的 `Content-Security-Policy`，不返回 `Set-Cookie`，也不把查询参数中的探测令牌反射到响应正文；线上 OpenAPI 版本为 `2026-07-18.3`，包含且只包含 9 个 Seedance 国内客户契约 operation，未公开 `ListSplitBillDetail`。既有 `GetAsset`、国内格式视频查询、OpenAI 格式查询别名和 1-byte Range 下载均再次通过，未创建新素材或新视频，未产生视频生成费用。

## 2. 接口覆盖矩阵

| 顺序 | 公开 API | 验证内容 | 结果 |
| ---: | --- | --- | --- |
| 1 | `POST /api/v3/open/CreateVisualValidateSession` | 创建真人认证会话 | 成功分支通过 |
| 2 | `POST /api/v3/open/GetVisualValidateResult` | 无效 BytedToken 的 HTTP 200 业务错误 | 负例通过；成功分支未执行 |
| 3 | `POST /api/v3/open/CreateAssetGroup` | 创建素材组 | 通过 |
| 4 | `POST /api/v3/open/CreateAsset` | 从公网图片创建素材 | 通过 |
| 5 | `POST /api/v3/open/GetAsset` | 查询素材直至 `Active` | 通过 |
| 6 | `POST /v1/video/generations` | 使用素材库参考图创建最低预估/预扣规格视频 | 通过 |
| 7 | `GET /v1/video/generations/{task_id}` | 国内格式状态轮询 | 通过 |
| 8 | `GET /v1/videos/{task_id}` | OpenAI 格式查询别名 | 通过 |
| 9 | `GET /v1/videos/{task_id}/content` | 完整下载和 Range 下载 | 通过 |

附加安全验证：公网 `POST /api/v3/open/ListSplitBillDetail` 返回 HTTP 404。该接口仅供网关内部结算使用，不是对外 API，也未写入 OpenAPI 文档。

## 3. 逐接口请求、响应与上游映射

以下所有公开 URL 均以 `https://124.174.0.221` 为基础地址。示例已删除 API Key、BytedToken、H5Link、上游 RequestId 和临时签名查询参数。

### 3.0 完整公开参数索引

本表给出每个 operation 的完整参数入口和成功响应字段；字段的类型、必填/可选、默认值、枚举、范围、nullable、错误响应以及可直接复制的示例，以线上 OpenAPI `2026-07-19.2` 为唯一客户契约。下文保存的是本次真实 E2E 的具体请求和响应，不能用单个样例替代完整 schema。

| Operation | 请求参数 | 成功响应 |
| --- | --- | --- |
| `POST /api/v3/open/CreateVisualValidateSession` | JSON：`CallbackURL`（string/URI，必填） | `state`、`data.ResponseMetadata`、`data.Result.BytedToken`、`data.Result.H5Link`、`data.Result.CallbackURL`、`error` |
| `POST /api/v3/open/GetVisualValidateResult` | JSON：`BytedToken`（string，必填） | `state`、`data.GroupId`、`error`；业务失败可能仍是 HTTP 200 |
| `POST /api/v3/open/CreateAssetGroup` | JSON：`Name`（string，必填，最大 64）、`Description`（string，可选，最大 300） | `state`、`data.Id`、`error` |
| `POST /api/v3/open/CreateAsset` | JSON：`GroupId`、`URL`、`AssetType`（必填），`Name`（可选）；`AssetType=Image/Video/Audio` | `state`、`data.Id`、`error`，以及 `X-New-Api-Asset-Namespace` 响应头 |
| `POST /api/v3/open/GetAsset` | JSON：`Id`（string，必填） | `state`、`data.Id/Name/URL/AssetType/GroupId/Status/Moderation/Error/CreateTime/UpdateTime/ProjectName`、`error` |
| `POST /v1/video/generations` | JSON：`model`；`content` 或 `prompt/image/images`；`audio_status/generate_audio`；`resolution=720p/1080p`；`ratio`；`dur=4～15/-1` | `id`、`task_id`、`object`、`model`、`status`、`progress`、`created_at` |
| `GET /v1/video/generations/{task_id}` | Path：`task_id`（必填） | `code` 与 `data.task_id/status/fail_reason/result_url/submit_time/start_time/finish_time/progress` |
| `GET /v1/videos/{task_id}` | Path：`task_id`（必填） | OpenAI 格式：`id/task_id/object/model/status/progress/created_at/completed_at/metadata/error`；状态含 `unknown` |
| `GET /v1/videos/{task_id}/content` | Path：`task_id`；可选请求头 `Range`、`If-Range` | HTTP 200/206 二进制流，仅转发 `Content-Type/Content-Length/Content-Range/Accept-Ranges/ETag/Last-Modified/Content-Disposition` 资源头，并固定 `Cache-Control: private, no-store`；上游 Cookie 和服务端标识不会返回给客户 |

统一鉴权为 `Authorization: Bearer <Nexus Reach API Key>`。素材代理在 API Key 有效但没有匹配的上游路由时返回 HTTP 404、`error.code=no_upstream_route`。公开视频下载还支持 Nexus Reach 控制台的已登录浏览器会话，但该方式不属于客户 OpenAPI。

上游证据口径如下：五个 `/api/v3/open/*` 接口由专用代理修改鉴权头和目标 URL，但不改成功 JSON body；因此下文标注为“公网/上游相同”的 JSON 同时代表供应商 body 与客户 body，本次产物没有再保存一份包含供应商密钥的重复原始副本。视频创建会规范化请求并隐藏供应商任务 ID；国内查询由后台调用 `generate-info`；OpenAI 查询别名只读取同一网关任务记录；内容下载对应的是对保存结果 URL 的资源 GET，而不是供应商同名 API。

### 3.1 创建真人认证会话

- 公开 URL：`POST https://124.174.0.221/api/v3/open/CreateVisualValidateSession`
- 上游 URL：`POST https://api.laomandi.com/asset/SdToolApi/CreateVisualValidateSession`
- 转发：请求 JSON 原样转发；客户 `Authorization` 不发送给上游，代理改用服务端保存的 `lmd-key`。

公网请求与实际上游请求 body（相同）：

```json
{
  "CallbackURL": "https://124.174.0.221/apidocs/apidocs-example-callback.html"
}
```

实际上游响应与实际公开响应 body（相同，HTTP 200）：

```json
{
  "state": 1,
  "data": {
    "ResponseMetadata": {
      "RequestId": "<redacted>",
      "Action": "CreateVisualValidateSession",
      "Version": "2024-01-01",
      "Service": "ark",
      "Region": "cn-beijing"
    },
    "Result": {
      "BytedToken": "<redacted>",
      "H5Link": "<redacted>",
      "CallbackURL": "https://124.174.0.221/apidocs/apidocs-example-callback.html"
    }
  },
  "error": null
}
```

### 3.2 获取真人认证结果

- 公开 URL：`POST https://124.174.0.221/api/v3/open/GetVisualValidateResult`
- 上游 URL：`POST https://api.laomandi.com/asset/SdToolApi/GetVisualValidateResult`
- 转发：请求和供应商业务响应原样转发。

本次自动化负例的公网请求与实际上游请求 body（相同）：

```json
{
  "BytedToken": "invalid-e2e-20260718T145325Z"
}
```

实际上游响应与实际公开响应 body（相同，HTTP 200）：

```json
{
  "state": 0,
  "data": null,
  "error": ["BytedToken信息有误"]
}
```

调用方不能只判断 HTTP 状态；业务成功条件是 `state === 1`、`data` 非空且 `error === null`。真人认证成功分支需在创建会话后的 120 秒内打开 H5Link 并由用户完成人工活体认证。

### 3.3 创建素材组

- 公开 URL：`POST https://124.174.0.221/api/v3/open/CreateAssetGroup`
- 上游 URL：`POST https://api.laomandi.com/asset/SdToolApi/CreateAssetGroup`
- 转发：请求和响应原样转发。

公网请求与实际上游请求 body（相同）：

```json
{
  "Name": "nexus-ip-e2e-20260718T145325Z",
  "Description": "Public IP Seedance 2.0 E2E validation"
}
```

实际上游响应与实际公开响应 body（相同，HTTP 200）：

```json
{
  "state": 1,
  "data": {
    "Id": "group-20260718225334-bwxch"
  },
  "error": null
}
```

### 3.4 创建图片素材

- 公开 URL：`POST https://124.174.0.221/api/v3/open/CreateAsset`
- 上游 URL：`POST https://api.laomandi.com/asset/SdToolApi/CreateAsset`
- 转发：请求转发给与素材组绑定的同一供应商渠道；成功响应增加 `X-New-Api-Asset-Namespace: seedance-domestic`。

公网请求与实际上游请求 body（相同）：

```json
{
  "GroupId": "group-20260718225334-bwxch",
  "URL": "https://images.clipsafari.com/6rhxknsi0s4gqoqot0z9u2593bf6?filename=cartoon-woman.png",
  "Name": "nexus-ip-e2e-virtual-avatar-20260718T145325Z",
  "AssetType": "Image"
}
```

实际上游响应与实际公开响应 body（相同，HTTP 200）：

```json
{
  "state": 1,
  "data": {
    "Id": "asset-20260718225340-wx6cz"
  },
  "error": null
}
```

已知的供应商素材业务错误由网关转换为 HTTP 400，并保留具体 `error.code` 和 `error.message`，避免改写成丢失原因的通用 502。

部署修复后的真实尺寸负例使用同一公网 endpoint。公网请求与实际上游请求 body 相同：

```json
{
  "GroupId": "group-20260718225334-bwxch",
  "URL": "https://httpbin.org/image/png",
  "Name": "cookie-strip-negative-453829ad",
  "AssetType": "Image"
}
```

供应商判定该图片只有 100×100 px。专用代理读取供应商业务错误后返回 HTTP 400，实际公开响应为：

```json
{
  "error": {
    "code": "InvalidParameter.WidthTooSmall",
    "message": "Width must be between 300px and 6000px."
  }
}
```

该产物同时确认响应没有供应商 `Set-Cookie`。测试未创建素材记录，也没有产生视频生成费用；供应商原始错误 envelope 没有作为客户证据持久化，报告不把规范化后的 body 误称为未修改的上游原文。

### 3.5 查询素材

- 公开 URL：`POST https://124.174.0.221/api/v3/open/GetAsset`
- 上游 URL：`POST https://api.laomandi.com/asset/SdToolApi/GetAsset`
- 转发：请求和业务响应原样转发；供应商 `Set-Cookie` 不向客户暴露。

公网请求与实际上游请求 body（相同）：

```json
{
  "Id": "asset-20260718225340-wx6cz"
}
```

实际上游响应与实际公开响应 body（相同，HTTP 200；临时 URL 已移除签名参数）：

```json
{
  "state": 1,
  "data": {
    "Id": "asset-20260718225340-wx6cz",
    "Name": "nexus-ip-e2e-virtual-avatar-20260718T145325Z",
    "URL": "https://ark-media-asset.tos-cn-beijing.volces.com/<redacted>",
    "AssetType": "Image",
    "GroupId": "group-20260718225334-bwxch",
    "Status": "Active",
    "Moderation": {
      "Strategy": "Default"
    },
    "CreateTime": "2026-07-18T14:53:40Z",
    "UpdateTime": "2026-07-18T14:53:42Z",
    "ProjectName": "fzjktest6"
  },
  "error": null
}
```

只有 `Status=Active` 的素材可以用 `asset://<AssetId>` 参与视频生成。

### 3.6 创建视频任务

- 公开 URL：`POST https://124.174.0.221/v1/video/generations`
- 上游 URL：`POST https://api.laomandi.com/asset/SdToolApi/generate`
- 公开格式：国内 Seedance JSON 协议。
- 不支持：OpenAI/Sora 的 `POST /v1/videos` multipart 创建协议。

公开请求：

```json
{
  "model": "doubao-seedance-2-0-260128",
  "content": [
    {
      "type": "image_url",
      "image_url": {
        "url": "asset://asset-20260718225340-wx6cz"
      },
      "role": "reference_image"
    },
    {
      "type": "text",
      "text": "图片1中的虚拟人物自然眨眼并轻微呼吸，镜头固定，无文字。"
    }
  ],
  "audio_status": 0,
  "resolution": "720p",
  "ratio": "16:9",
  "dur": 4
}
```

实际公开创建响应（HTTP 200）：

```json
{
  "id": "task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj",
  "task_id": "task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj",
  "object": "video",
  "model": "doubao-seedance-2-0-260128",
  "status": "queued",
  "progress": 0,
  "created_at": 1784386426
}
```

上游请求会移除仅供网关选路的 `model` 字段，其余规范化内容保持一致：

```json
{
  "content": [
    {
      "type": "image_url",
      "image_url": {
        "url": "asset://asset-20260718225340-wx6cz"
      },
      "role": "reference_image"
    },
    {
      "type": "text",
      "text": "图片1中的虚拟人物自然眨眼并轻微呼吸，镜头固定，无文字。"
    }
  ],
  "audio_status": 0,
  "resolution": "720p",
  "ratio": "16:9",
  "dur": 4
}
```

公开响应返回网关生成的安全任务 ID。上游实际任务 ID 为 `2347`。由于网关创建时立即替换上游 ID，且后续轮询更新了保存的原始响应，本次没有为捕获上游原始创建响应而再次生成付费视频；根据实际保存的上游任务 ID，其创建成功响应契约为：

```json
{
  "state": 1,
  "data": {
    "id": 2347
  },
  "error": null
}
```

### 3.7 查询视频任务（国内格式）

- 公开 URL：`GET https://124.174.0.221/v1/video/generations/task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj`
- 数据来源：网关任务数据库。
- 后台上游查询：`POST https://api.laomandi.com/asset/SdToolApi/generate-info`
- 后台上游请求：`{"id":2347}`

实际上游完成响应：

```json
{
  "state": 1,
  "data": {
    "id": 2347,
    "status": 2,
    "video_url": "https://s3.laomandi.com/file/<redacted>.mp4",
    "message": null,
    "created_at": "2026-07-18 22:53:46"
  },
  "error": null
}
```

实际公开完成响应：

```json
{
  "code": "success",
  "data": {
    "task_id": "task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj",
    "status": "SUCCESS",
    "fail_reason": "",
    "result_url": "https://s3.laomandi.com/file/<redacted>.mp4",
    "submit_time": 1784386426,
    "start_time": 1784386440,
    "finish_time": 1784386590,
    "progress": "100%"
  }
}
```

公开响应不会暴露数据库自增 ID、用户 ID、渠道 ID、额度字段或供应商私有属性。

### 3.8 查询视频任务（OpenAI 格式别名）

- 公开 URL：`GET https://124.174.0.221/v1/videos/task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj`
- 数据来源：与国内格式查询相同的网关任务数据库。
- 上游调用：此 GET 本身不直接调用供应商；后台轮询负责更新任务。

实际公开响应：

```json
{
  "id": "task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj",
  "task_id": "task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj",
  "object": "video",
  "model": "doubao-seedance-2-0-260128",
  "status": "completed",
  "progress": 100,
  "created_at": 1784386426,
  "completed_at": 1784386590,
  "metadata": {
    "url": "https://s3.laomandi.com/file/<redacted>.mp4"
  }
}
```

该接口只是查询响应格式别名，不表示系统支持 OpenAI/Sora multipart 创建协议。

### 3.9 下载视频内容

- 公开 URL：`GET https://124.174.0.221/v1/videos/task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj/content`
- 实现来源：Nexus Reach 本地代理接口，上游供应商没有同名 API。
- 上游访问：网关校验任务归属和成功状态后，流式读取任务保存的结果 URL。

完整下载结果：

- HTTP 200
- `Content-Type: text/plain`（供应商资源的响应头标注；文件内容实际为 MP4）
- `Cache-Control: private, no-store`
- 文件大小：1,684,544 bytes
- SHA-256：`7e9d720c0449ab495bfd9fd90d0bea1916db0ca0a9404dd9a30504e6f1a2d9f7`
- 文件头：`000000206674797069736f6d00000200`，包含 MP4 `ftyp/isom` 标识
- 视频：H.264，1254×720，24 fps，4.041667 秒，无音轨

Range 请求 `Range: bytes=0-1023` 的结果：

- HTTP 206
- 1,024 bytes
- `Content-Range: bytes 0-1023/1684544`
- `Cache-Control: private, no-store`
- SHA-256：`a481f9c146318539d9175f98c2bc646c4443e56b658f468a50ea8dbc13bb0108`

对应的上游不是 JSON API，而是保存的 `https://s3.laomandi.com/file/<redacted>.mp4` 资源。网关发起 `GET <result_url>` 并转发客户端的 `Range` 与 `If-Range`。2026-07-19 对该资源直接执行 `Range: bytes=0-0` 的零费用复验结果为 HTTP 206、`Content-Length: 1`、`Content-Type: text/plain`、`Content-Range: bytes 0-0/1684544`、`Accept-Ranges: bytes`；公网代理的同一范围也返回 HTTP 206 和相同总长度。供应商没有与 `/v1/videos/{task_id}/content` 同名的 endpoint。

## 4. 计费验证

### 4.1 对客户的人民币价格

| 分辨率 | 无输入视频 | 含输入视频 |
| --- | ---: | ---: |
| 720p | ¥46 / 百万 tokens | ¥28 / 百万 tokens |
| 1080p | ¥51 / 百万 tokens | ¥31 / 百万 tokens |
| 4K | ¥26 / 百万 tokens（计划价） | ¥16 / 百万 tokens（计划价） |

预扣估算公式为：

```text
估算 tokens = ceil((输出秒数 + 15 × 输入视频数量) × 输出像素数 × 24 ÷ 1024)
估算人民币 = 估算 tokens ÷ 1,000,000 × 对应规格单价
预扣 quota = round(估算人民币 ÷ 7.3 × 500,000)
```

输出像素映射为：720p=`1280×720=921,600`，1080p=`1920×1080=2,073,600`，4K=`3840×2160=8,294,400`。预扣阶段无法可信读取远程输入视频时长，因此每个输入视频都按最大 15 秒保守估算。以下统一使用 4 秒输出、16:9、分组倍率 1，分别覆盖“无输入视频”和“1 个输入视频”两个计费分支：

| 分辨率 | 输入视频 | 计费时长 | 估算 tokens | 单价 | 估算人民币 | 预扣 quota | 证据类型 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| 720p | 无 | 4 秒 | 86,400 | ¥46 | ¥3.974400 | 272,219 | 真实付费 E2E + 回归测试 |
| 720p | 1 个 | 19 秒 | 410,400 | ¥28 | ¥11.491200 | 787,068 | 回归测试/公式，不另生成视频 |
| 1080p | 无 | 4 秒 | 194,400 | ¥51 | ¥9.914400 | 679,068 | 回归测试/公式，不另生成视频 |
| 1080p | 1 个 | 19 秒 | 923,400 | ¥31 | ¥28.625400 | 1,960,644 | 回归测试/公式，不另生成视频 |
| 4K | 无 | 4 秒 | 777,600 | ¥26 | ¥20.217600 | 1,384,767 | 计划价回归测试，生产禁用 |
| 4K | 1 个 | 19 秒 | 3,693,600 | ¥16 | ¥59.097600 | 4,047,781 | 计划价回归测试，生产禁用 |

由表可见，虽然“含输入视频”的百万 token 单价更低，但保守计费时长增加 15 秒，因此 720p、4 秒、无输入视频才是这些组合中的最低预扣规格。本轮遵循“只生成最便宜的一个视频”的要求，只对第一行执行真实付费 E2E；其他组合由确定性计费回归测试覆盖，不能表述为已生成视频验证。

4K 的转发、像素和报价代码已保留，但生产默认 `SEEDANCE_DOMESTIC_4K_ENABLED=false`，请求在到达供应商前即返回 HTTP 400。4K 尚未完成供应商渠道开通和端到端验证，因此没有加入公开 `resolution` 枚举，客户当前只能提交 720p 或 1080p。

上述六种组合由以下确定性回归测试覆盖，执行 `go test ./relay/channel/task/seedancedomestic` 已通过：`TestEstimateTaskBillingMatchesOfficial720pExample`、`TestEstimateTaskBillingUsesVideoInputPriceAndConservativeDuration`、`TestEstimateTaskBillingMatchesOfficial1080pTiers`、`TestEstimateTaskBillingMatchesOfficial4KTiers`。其中 4K 测试只验证计划价计算，不代表生产能力已开放。

通用公式：

```text
人民币费用 = 最终 total_tokens ÷ 1,000,000 × 对应规格的人民币单价
```

本次输入只有参考图片，没有输入视频，因此使用“720p、无输入视频”单价 ¥46 / 百万 tokens。参考图片不会触发“含输入视频”价格。

### 4.2 预扣与最终结算

预估：

```text
估算 tokens = 86,400
估算人民币费用 = 86,400 ÷ 1,000,000 × 46
                 = ¥3.974400
预扣 quota = round(3.974400 ÷ 7.3 × 500,000)
            = 272,219
```

供应商最终账单：

```json
{
  "id": 2347,
  "resolution": "720p",
  "ratio": "16:9",
  "dur": 4,
  "expense_time": "2026-07-18 22:53:46",
  "total_tokens": 87277,
  "price": "46.000000",
  "original_price": "4.0",
  "discount": "1.00",
  "amount_paid": "4.0"
}
```

最终结算：

```text
最终人民币费用 = 87,277 ÷ 1,000,000 × 46
                 = ¥4.014742
最终 quota = round(4.014742 ÷ 7.3 × 500,000)
           = 274,982
差额 quota = 274,982 - 272,219
           = +2,763
```

任务结算状态为 `settled`，系统按最终账单补扣 2,763 quota。对外报价和客户费用均使用人民币；`7.3` 只参与当前 new-api 内部美元锚定 quota 单位换算，不改变人民币价格公式。若后续把账户 quota 直接锚定人民币，可去除该内部换算层，但必须同步迁移充值、余额展示、日志和历史账务口径。

2026-07-19 对生产数据库执行只读联表复核，得到以下持久化结算证据：

```json
{
  "public_task_id": "task_zzxWmWISHZRwfoTt7Mb4Mp66m9aRVsuj",
  "task_status": "SUCCESS",
  "task_final_quota": 274982,
  "upstream_task_id": "2347",
  "reconciliation_status": "settled",
  "attempts": 5,
  "total_tokens": 87277,
  "supplier_price": "46.000000",
  "supplier_discount": "1.00",
  "supplier_amount_paid": "4.0",
  "expense_time": "2026-07-18 22:53:46",
  "pre_consumed_quota": 272219,
  "actual_quota": 274982,
  "quota_delta": 2763,
  "billing_reconciliation_pending": false
}
```

该记录直接证明最终账单已落库并完成客户额度差额结算，不再只依赖文字叙述。供应商 `amount_paid=4.0` 是供应商成本账单自身的显示/舍入口径，不是客户最终收费字段；客户精确公式仍是 `87277 ÷ 1,000,000 × 46 = ¥4.014742`。由于 new-api quota 必须是整数，实际落库 274,982 quota 按内部汇率反算为 `274982 × 7.3 ÷ 500000 = ¥4.0147372`，与精确人民币金额相差 ¥0.0000048，属于单个 quota 取整误差。

### 4.3 内部账单查询窗口和时区

任务成功后，后台使用供应商私有接口：

```text
POST https://api.laomandi.com/asset/SdToolApi/ListSplitBillDetail
```

本次最终匹配任务 2347 的查询窗口为：

```json
{
  "expense_date_start": "2026-07-18 21:53:46",
  "expense_date_end": "2026-07-18 23:58:00",
  "page": 0,
  "size": 1000
}
```

窗口不是严格的“创建时刻到完成时刻”，而是在任务提交时间前和当前时间后各留出一小时余量，以覆盖供应商账单落库延迟。供应商 `expense_time` 和查询参数统一按 Asia/Shanghai（UTC+8）构造，避免把 UTC 时间直接发送给使用北京时间的供应商接口。

## 5. 鉴权与错误行为

- 四个视频接口缺少或使用无效 Token：HTTP 401，响应为 `new_api_error` 格式。
- 五个真人认证/素材接口缺少 API Key：HTTP 401，`error.code=token_not_provided`；无效 Key 使用 `token_invalid`。
- 真人认证和素材直通接口的业务失败可能返回 HTTP 200；必须检查 `state/data/error`。
- `CreateAsset` 的已知供应商业务错误会转为 HTTP 400，并保留具体业务错误码和消息。
- `GET /v1/videos/{task_id}/content` 只允许当前 API Key 所属账号读取自己的成功任务，固定返回 `Cache-Control: private, no-store`，并通过资源响应头白名单剥离供应商 `Set-Cookie`、`Server` 等非资源头。
- 供应商 `Set-Cookie` 响应头由素材代理剥离，不会泄漏 PHP 会话标识给客户。

## 6. 最终结论与未覆盖项

公网 IP HTTPS 入口已能完整承载本文件列出的 9 个 Seedance 国内客户契约 API。服务器还保留供其他供应商使用的通用 new-api 路由，例如 `POST /v1/videos` 和 `POST /v1/videos/{video_id}/remix`；它们不属于国内 Seedance 特殊格式契约，也不应据此推断国内 Seedance 支持 OpenAI/Sora multipart 创建协议。客户可使用素材库图片创建视频，轮询国内格式或 OpenAI 兼容格式的任务状态，并通过网关代理完整或分段下载结果。

当前唯一未完成的成功路径是必须由真人交互的活体认证结果获取。若需要把该分支也标记为端到端通过，测试人员需在创建会话后的 120 秒内打开 H5Link、完成人工认证，并立即使用创建响应中仅保留在内存的原始 BytedToken 调用 `GetVisualValidateResult`；该步骤不会生成视频费用。

已新增 `scripts/test-seedance-visual-validation-e2e.ps1` 作为该分支的交互式验证工具。其零费用 `-PreflightOnly` 模式已通过，确认公网回调安全头和无效 BytedToken 契约正常，且没有创建活体会话、素材或视频。实际成功模式要求显式传入 `-AuthorizedPersonReady`，只在进程内存保留创建响应中的 BytedToken 和 H5Link；Edge InPrivate 通过只含随机路径的一次性本机回环导航桥打开 H5Link，浏览器进程命令行不携带供应商 URL 或凭证。授权人员确认回调成功后，脚本才调用结果接口；持久化证据只记录 GroupId 存在性和字符长度，不保存值、预览或哈希。该成功模式尚未在没有授权真人准备好的情况下执行，因此预检不能替代真人成功分支的实际端到端证据。

## 2026-07-19 对外文档与契约安全更新、部署复验

- OpenAPI 已升级为 `2026-07-19.2`：明确 9 个 operation 中 8 个已有业务成功 E2E；补全视频任务裸 `TaskError`、素材路由 404、OpenAI `unknown` 状态、控制台会话边界和真实请求字段约束。
- 已明确国内 Seedance 的唯一创建入口、两种查询响应格式和网关内容代理；查询与下载不重复计费，对外任务响应暂不返回最终 `total_tokens` 或单任务人民币金额，`ListSplitBillDetail` 仍只用于网关内部结算。
- 4K 仍是未开放的计划能力。生产后端显式设置 `SEEDANCE_DOMESTIC_4K_ENABLED=false`，即使客户绕过文档提交 4K，也会在创建任务、预扣费和请求供应商之前返回 HTTP 400；公开枚举和可调用报价仍只有 720p、1080p。
- 视频内容代理改为资源响应头白名单，只转发下载所需资源头并剥离供应商 `Set-Cookie`、`Server` 等非资源头；`Cache-Control` 固定为 `private, no-store`。
- 提交 `700948c4` 已推送并部署。后端使用不可变镜像 `new-api-seedance:700948c4`，只重建 `new-api`；素材代理容器 ID、镜像和启动时间均未变化。静态文档 bundle 为 `static/js/index.e53e0802d3.js`，只重建 `api-docs`；Caddy、PostgreSQL、Redis 均未重启。
- 部署后零费用复验通过：文档版本 `2026-07-19.2`、9 个 paths、回调页安全头、既有素材 `Active`、国内任务 `SUCCESS`、OpenAI 查询别名 `completed`、1 字节 Range 下载 HTTP 206 且无 `Set-Cookie`。
- 4K 门禁探针返回 HTTP 400、`code=invalid_request`；720p/1080p 使用非法时长的控制探针同样在本地返回 HTTP 400。探针前后用户剩余额度、已用额度和任务数量完全一致，确认没有创建供应商任务或产生视频费用。
- 本次复验没有创建活体会话、素材或视频。真人认证成功分支仍需等待已授权本人和摄像头准备好后执行。
