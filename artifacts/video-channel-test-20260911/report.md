# 渠道 1、2、3 视频生成实测

测试日期：2026-09-11（北京时间）。每个渠道仅提交一次生成请求。

## 配置与费用口径

- 使用现有管理员测试令牌 ID 6（JeffDefaultTest），扣费归属用户 ID 1，分组 default，倍率 1。
- 参数：4 秒、720p、16:9、无音频、纯文生视频。内容为白色背景下缓慢旋转的蓝色方块。
- 按已核实的 Seedance 2.0 价格选择低成本规格；未穷举供应商全部优惠、未验证的分辨率或其他模型价格，因此不作全平台绝对最低价承诺。
- 当前价格：无视频参考输入 46 元/百万视频 Token；估算 86,400 Token，预计每条 ¥3.9744。
- 系统 quota_per_unit 为 500,000 时，人民币显示金额 = quota / 500,000 × 7.3。实际结算以任务最终用量为准，不把预扣当成供应商实际账单。
- 本报告的下游指调用 New API 的客户端，上游指对应供应商。

| 渠道 | 名称 | 请求模型 | 提交 HTTP | New API 任务 ID | 上游任务 ID |
|---|---|---|---|---|---|
| 1 | seedance-domestic-production | doubao-seedance-2-0-260128 | 200 | task_rzk2EthPPn1QllMjftsWN7v7OVLmT8i5 | 3228 |
| 2 | hwdramamobile | doubao-seedance-2-0-260128 | 200 | task_NDknYMegP1cR3ZMCUVgC3gwiZkVrJ2pW | cgt-20260911181629-b5mrp |
| 3 | mobile-cloud-seedance-production | doubao-seedance-2.0 | 500 | 未返回 | 未返回 |

## 下游请求：实际提交参数

三个渠道均调用：

```http
POST https://gateway.nexus-reach.com/v1/video/generations
Content-Type: application/json
Authorization: Bearer <管理员 API Key>-<渠道ID>
```

渠道后缀为管理员定向测试功能；密钥不写入报告。渠道 1、2 的请求正文完全一致：

```json
{
  "model": "doubao-seedance-2-0-260128",
  "prompt": "A small blue cube slowly rotates on a plain white background. Fixed camera, simple studio lighting, no people, no text, no audio.",
  "duration": 4,
  "resolution": "720p",
  "metadata": {
    "ratio": "16:9",
    "generate_audio": false
  }
}
```

渠道 3 仅将 model 改成 `doubao-seedance-2.0`。

## 下游提交响应：实际返回

渠道 1（HTTP 200）：

```json
{
  "id": "task_rzk2EthPPn1QllMjftsWN7v7OVLmT8i5",
  "task_id": "task_rzk2EthPPn1QllMjftsWN7v7OVLmT8i5",
  "object": "video",
  "model": "doubao-seedance-2-0-260128",
  "status": "queued",
  "progress": 0,
  "created_at": 1789121766
}
```

渠道 2（HTTP 200）：

```json
{
  "id": "task_NDknYMegP1cR3ZMCUVgC3gwiZkVrJ2pW",
  "task_id": "task_NDknYMegP1cR3ZMCUVgC3gwiZkVrJ2pW",
  "object": "video",
  "model": "doubao-seedance-2-0-260128",
  "status": "queued",
  "progress": 0,
  "created_at": 1789121789
}
```

渠道 3（HTTP 500）：

```json
{
  "code": "do_request_failed",
  "message": "create Mobile Cloud Seedance task: Failed to create video generation task: {\"error\":{\"message\":\"当前账号处未订购seedance2.0模型资费包，或资费包已到期，请先订购后才能使用\",\"type\":\"invalid_authentication_error\"}}",
  "data": null
}
```

渠道 3 错误明确来自移动云资费订购校验。需核对该渠道实际绑定账号的 Seedance 2.0 资费包及有效期；不能据此判断 API Key 本身错误。此次日志为错误日志，quota=0，未找到该请求的消费日志；本次没有订购资费包或更换账号。

## 上游地址与请求结构

以下请求正文根据本次输入、渠道配置与本地适配器转换规则还原，不是生产网络抓包。上游提交瞬间的原始响应未单独截获，任务编号来自本次实际任务记录；不得把还原示例当成完整原始响应。

### 渠道 1

```http
POST https://api.laomandi.com/asset/SdToolApi/generate
Content-Type: application/json
lmd-key: <渠道密钥，省略>
```

```json
{
  "content": [
    {"type": "text", "text": "A small blue cube slowly rotates on a plain white background. Fixed camera, simple studio lighting, no people, no text, no audio."}
  ],
  "audio_status": 0,
  "resolution": "720p",
  "ratio": "16:9",
  "dur": 4
}
```

查询上游结果：

```http
POST https://api.laomandi.com/asset/SdToolApi/generate-info
Content-Type: application/json
lmd-key: <渠道密钥，省略>

{"id":3228}
```

### 渠道 2

```http
POST https://ai.hwdrama.com/api/v3/contents/generations/tasks
Authorization: Bearer <渠道密钥，省略>
Content-Type: application/json
```

```json
{
  "model": "doubao-seedance-2-0-260128",
  "content": [
    {"type": "text", "text": "A small blue cube slowly rotates on a plain white background. Fixed camera, simple studio lighting, no people, no text, no audio."}
  ],
  "generate_audio": false,
  "resolution": "720p",
  "ratio": "16:9",
  "duration": 4
}
```

查询上游结果：

```http
GET https://ai.hwdrama.com/api/v3/contents/generations/tasks/cgt-20260911181629-b5mrp
Authorization: Bearer <渠道密钥，省略>
```

### 渠道 3

逻辑创建接口：`https://zhenze-huhehaote.cmecloud.cn/api/v3/contents/generations/tasks`。

业务参数结构与渠道 2 相同，model 为 `doubao-seedance-2.0`。该渠道使用移动云官方安全 SDK，实际传输还包含 SDK 安全协议处理，不应把逻辑业务参数直接当成可用的普通 Bearer/curl 请求。

上游错误内容由 SDK 透传到 New API：

```json
{
  "error": {
    "message": "当前账号处未订购seedance2.0模型资费包，或资费包已到期，请先订购后才能使用",
    "type": "invalid_authentication_error"
  }
}
```

## 下游结果查询地址

```http
GET https://gateway.nexus-reach.com/v1/video/generations/task_rzk2EthPPn1QllMjftsWN7v7OVLmT8i5
GET https://gateway.nexus-reach.com/v1/video/generations/task_NDknYMegP1cR3ZMCUVgC3gwiZkVrJ2pW
Authorization: Bearer <本次测试用户的 API Key>
```

视频完成后可通过 `/v1/videos/<task_id>/content` 访问网关视频内容接口，需要相应身份凭据；上游视频链接可能有有效期。

## 最终结果

两条任务均 SUCCESS，且已用 HTTP GET Range 实际读取视频前 64 字节：均返回 206，包含 MP4 ftyp 文件头。

| 渠道 | 最终状态 | 视频 Token | 系统最终 quota | 按当前汇率折合人民币 |
|---|---|---:|---:|---:|
| 1 | SUCCESS | 87277 | 274982 | ¥4.0147372 |
| 2 | SUCCESS | 87300 | 275055 | ¥4.0158030 |
| 3 | 资费包校验失败 | 未返回 | 错误日志 quota=0 | 未记录消费 |

两条成功任务合计系统扣费约 **¥8.0305**。这是 New API 用户侧扣费，不代表上游采购账单。

- [渠道 1 视频](https://s3.laomandi.com/file/ce/b6/ceb609d8c031cb60988b0a3eb786c5af.mp4)：2,681,747 字节。上游 Content-Type 为 text/plain，但已验证文件头为 MP4。
- [渠道 2 视频](https://ark-acg-cn-beijing.tos-cn-beijing.volces.com/doubao-seedance-2-0/02178912178971300000000000000000000ffffac19310d3d4c04.mp4?X-Tos-Algorithm=TOS4-HMAC-SHA256&X-Tos-Credential=AKLTYWJkZTExNjA1ZDUyNDc3YzhjNTM5OGIyNjBhNDcyOTQ%2F20260911%2Fcn-beijing%2Ftos%2Frequest&X-Tos-Date=20260911T101948Z&X-Tos-Expires=86400&X-Tos-Signature=863f9aaae6b0a09afe1eabc7d782de373ecf8d15319f4b634485890b7bfdb79d&X-Tos-SignedHeaders=host)：890,418 字节。签名 URL 标注 86400 秒有效期，自 2026-09-11 18:19:48（北京时间）起计，实际以存储服务校验为准。
- [完整实测响应 JSON](./results.json)：包含下游最终响应、上游直接查询响应、任务计费快照和查询请求参数。此前渠道 2 的 HEAD 校验被拒绝，后续真实 GET Range 验证成功，不能把 HEAD 报错当成视频不可下载。

下游视频内容地址（需登录或提供对应 API Key）：

```text
https://gateway.nexus-reach.com/v1/videos/task_rzk2EthPPn1QllMjftsWN7v7OVLmT8i5/content
https://gateway.nexus-reach.com/v1/videos/task_NDknYMegP1cR3ZMCUVgC3gwiZkVrJ2pW/content
```

本次只执行了三次生成提交、对应任务查询和视频文件头读取，没有修改渠道、价格或服务器部署配置。
