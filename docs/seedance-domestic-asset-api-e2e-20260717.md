# Seedance 2.0 国内素材接口生产验证报告

## 1. 验证范围

- 验证时间：2026-07-17 11:41～11:44（Asia/Shanghai）
- 客户 Base URL：`https://gateway.nexus-reach.com`
- 服务器回环：`--resolve gateway.nexus-reach.com:443:127.0.0.1`
- 覆盖链路：域名证书、Caddy、客户 API Key 鉴权、素材代理、国内供应商接口
- 测试命名：`nexus-seedance-doc-e2e-20260717114105`
- 测试素材组：`group-2026…hvgzx`
- 测试素材：`asset-2026…lwvfn`

报告不记录真实 API Key、BytedToken、H5Link 或签名 URL 查询参数。

## 2. 验证结果

| 接口 | 场景 | HTTP / 业务结果 | 耗时 |
| --- | --- | --- | ---: |
| `POST /api/v3/open/CreateAssetGroup` | 缺少 API Key | `401`，`token_not_provided` | 0.020s |
| `POST /api/v3/open/CreateAssetGroup` | 无效 API Key | `401`，`token_invalid` | 0.027s |
| `POST /api/v3/open/CreateVisualValidateSession` | 创建真人认证会话 | `200`，`state=1` | 0.771s |
| `POST /api/v3/open/GetVisualValidateResult` | 无效一次性令牌 | `200`，`state=0` | 0.098s |
| `POST /api/v3/open/CreateAssetGroup` | 创建测试素材组 | `200`，返回 `data.Id` | 0.488s |
| `POST /api/v3/open/CreateAsset` | 上传合规虚拟人物 PNG | `200`，返回 `data.Id` | 2.552s |
| `POST /api/v3/open/GetAsset` | 查询素材状态 | `200`，首次查询即 `Active` | 0.411s |

## 3. 已确认的响应契约

### CreateVisualValidateSession

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
      "CallbackURL": "https://gateway.nexus-reach.com/apidocs/apidocs-example-callback.html"
    }
  },
  "error": null
}
```

认证字段位于 `data.Result`，不是供应商文字示例所示的 JSON 根级。

### GetVisualValidateResult 无效令牌

```json
{
  "state": 0,
  "data": null,
  "error": ["BytedToken信息有误"]
}
```

该接口的业务失败仍使用 HTTP 200，调用方必须同时检查 `state`、`data` 和 `error`。

### CreateAssetGroup / CreateAsset

```json
{
  "state": 1,
  "data": {
    "Id": "<group-or-asset-id>"
  },
  "error": null
}
```

因此 CreateAsset 路由必须保持 `affinity_response_field: data.Id`。

### GetAsset

```json
{
  "state": 1,
  "data": {
    "Id": "asset-2026…lwvfn",
    "Name": "nexus-seedance-doc-e2e-…-virtual-avatar",
    "URL": "https://ark-media-asset.tos-cn-beijing.volces.com/...?<redacted>",
    "AssetType": "Image",
    "GroupId": "group-2026…hvgzx",
    "Status": "Active",
    "Moderation": {
      "Strategy": "Default"
    },
    "CreateTime": "2026-07-17T03:43:56Z",
    "UpdateTime": "2026-07-17T03:43:58Z"
  },
  "error": null
}
```

- `CreateTime`、`UpdateTime` 是 ISO 8601 UTC。
- 成功响应可能不包含 `Error`。
- `URL` 是带签名的临时地址，客户应在 12 小时内下载并保存。

## 4. 测试素材

正例使用 744×1052、97,161 bytes 的 CC0 卡通虚拟人物 PNG：

```text
https://images.clipsafari.com/6rhxknsi0s4gqoqot0z9u2593bf6?filename=cartoon-woman.png
```

该文件在服务器侧下载结果为 HTTP 200、`Content-Type: image/png`，符合 300～6000 像素、0.4～2.5 宽高比和 30MB 上限。

## 5. 发现的问题

### CreateAsset 参数错误透传（本地已修复，待部署复测）

使用 256×256 图片时，上游实际返回：

```json
{
  "state": 1,
  "data": {
    "Code": "InvalidParameter.WidthTooSmall",
    "Message": "Width must be between 300px and 6000px.",
    "Data": null
  },
  "error": null
}
```

修复前，代理因为响应中不存在 `data.Id`，把该业务错误改写成 HTTP 502：

```json
{
  "error": {
    "code": "upstream_error",
    "message": "upstream response did not contain the configured asset identifier"
  }
}
```

本地代码现已在亲和 ID 缺失时识别字符串类型的 `data.Code` 和 `data.Message`，不写入素材亲和关系，并返回 HTTP 400：

```json
{
  "error": {
    "code": "InvalidParameter.WidthTooSmall",
    "message": "Width must be between 300px and 6000px."
  }
}
```

若 HTTP 2xx 响应既没有素材 ID，也没有可识别的业务错误码，仍返回原来的通用 502，避免把格式异常的成功响应误判成客户参数错误。该修复已增加本地回归测试，部署后还需通过域名重跑 256×256 负例确认线上结果。

### GetVisualValidateResult 成功分支需要人工完成

成功分支必须由真人打开 H5Link 完成活体认证，并在 120 秒内消费回调中的一次性 BytedToken。本次自动验证未采集或处理真人生物识别数据，因此只覆盖了创建会话和无效令牌负例。

### 公网 TLS 仍不稳定

服务器回环的全部测试均成功。本机直接访问公网域名连续 5 次均在 TLS 握手阶段被重置，HTTP 状态为 `000`，成功率 0/5。业务接口验证通过不代表公网入口已经达到生产稳定性要求。

## 6. 测试数据清理说明

本次测试产生一个素材组和一个 `Active` 素材。当前公开接口没有删除素材或素材组的端点，因此测试数据保留在线上，并使用明确的 `nexus-seedance-doc-e2e-*` 名称便于后续识别。
