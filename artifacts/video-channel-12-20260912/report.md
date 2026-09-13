# 渠道 12 视频生成测试

日期：2026-09-12。仅提交一次生成请求，无配置修改。

- 渠道：12 / uzoom-Seedance，OpenAI 类型。
- 上游地址：https://api.uzoomtech.com
- 模型：doubao-seedance-2-0-260128，无映射。
- 规格：请求 5 秒、720p、16:9、无音频。采用低规格；未取得上游完整价目表，不能保证绝对最低价。
- 请求入口：POST https://gateway.nexus-reach.com/v1/video/generations
- 使用现有管理员测试令牌定向渠道 12；密钥省略。

```json
{
  "model": "doubao-seedance-2-0-260128",
  "prompt": "A small blue cube rotates slowly on a plain white background. Fixed camera, no people, no text, no audio.",
  "seconds": "5",
  "duration": 5,
  "size": "1280x720",
  "resolution": "720p",
  "generate_audio": false,
  "metadata": {"ratio": "16:9", "generate_audio": false}
}
```

## 结果

- 提交 HTTP 200，请求 ID：202609120434468364085498268d9d6quq2NZdj。
- 本网站任务：task_6JvsbMlsnXxruTpFDoZNEG6d7FMvuVuy。
- 上游任务：task_eEsqct0szm6isicmqjNQthSEOdgjrbNS。
- 上游最终状态：completed，progress=100。
- 视频链接经范围读取返回 HTTP 206，内容为 video/mp4，包含 MP4 ftyp 标记。
- 未读取媒体时长元数据，5 秒为本次请求参数。

视频 URL（带时效签名）：

https://ark-acg-cn-beijing.tos-cn-beijing.volces.com/doubao-seedance-2-0/02178918769099500000000000000000000ffffac192edaae281e.mp4?X-Tos-Algorithm=TOS4-HMAC-SHA256&X-Tos-Credential=AKLTYWJkZTExNjA1ZDUyNDc3YzhjNTM5OGIyNjBhNDcyOTQ%2F20260912%2Fcn-beijing%2Ftos%2Frequest&X-Tos-Date=20260912T043633Z&X-Tos-Expires=86400&X-Tos-Signature=e0c937eded1e5c8c17fa267acf7594c3b3ddfb37b7bf3ad301049449be0accfb&X-Tos-SignedHeaders=host

## 状态及计费异常

本网站首次轮询时，上游返回 status=unknown。本网站随后标记 FAILURE，错误为 upstream returned unrecognized message，并退回预扣。直接查询上游同一任务，后续状态为 in_progress，最终 completed。

本网站日志 4833 预扣 24,437,500 quota；日志 4834 退回相同额度。按 500,000 quota/单位和显示汇率 7.3 换算，预扣展示约为 ¥356.7875，不能视为上游实际费用。上游已完成生成，是否扣费及实际金额须以上游账单为准。此次未修改任务状态、余额或计费配置。
