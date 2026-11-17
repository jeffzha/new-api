// New API video bridge.
//
// Drives a type-61 "Task Plugin" channel whose Base URL is another new-api
// gateway exposing the host openai_video protocol:
//   submit  POST {base}/v1/videos             {"model": "...", "prompt": "...", "seconds": 5, ...}
//   query   GET  {base}/v1/videos/{task_id}
// Auth: Authorization: Bearer <downstream key issued by that gateway>.
//
// Used to reach providers that are only reachable from another region: the
// upstream gateway owns the vendor channel, and this side re-exposes the same
// unified model name to domestic clients.
export const meta = {
  apiVersion: 1,
  key: "newapi-video",
  name: "New API Video Bridge",
  icon: "text:桥接",
  description: {
    en: "Bridge to another new-api gateway that serves the OpenAI video protocol",
    zh: "桥接到提供 OpenAI 视频协议的 new-api 网关",
  },
  version: "1.2.4",
  author: { name: "NexusReach" },
  models: ["MiniMax-H3-261013", "MiniMax-H3-2K", "MiniMax-H3-Sensitive"],
  fetchMode: "per_task",
  usageSchema: {
    output_credits: {
      type: "number",
      unit: "credit",
      description: { en: "Video output unit price", zh: "视频输出单价" },
    },
    material_credits: {
      type: "number",
      unit: "credit",
      description: { en: "Material unit price", zh: "参考素材单价" },
    },
    input_images: {
      type: "number",
      unit: "count",
      description: { en: "Input image unit price", zh: "输入图片单价" },
    },
    vendor_credits: {
      type: "number",
      unit: "credit",
      description: { en: "Upstream credit unit price", zh: "上游额度单价" },
    },
    mode: {
      enum: ["reference", "text"],
      enumLabels: {
        reference: { en: "Reference image to video", zh: "参考图生视频" },
        text: { en: "Prompt only (fixed 15s 2K)", zh: "纯提示词（固定 15 秒 2K）" },
      },
      description: { en: "Upstream generation path", zh: "上游生成路径" },
    },
    seconds: {
      type: "number",
      unit: "second",
      description: { en: "Video generation unit price", zh: "视频生成单价" },
    },
    resolution: {
      enum: ["480P", "720P", "1080P", "2K"],
      enumLabels: {
        "480P": { en: "480P", zh: "480P" },
        "720P": { en: "720P", zh: "720P" },
        "1080P": { en: "1080P", zh: "1080P" },
        "2K": { en: "2K", zh: "2K" },
      },
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
  },
  usageExamples: [
    {
      label: "H3 480P 4s",
      facts: { material_credits: 0, output_credits: 3.2, input_images: 0, vendor_credits: 3.2, mode: "text", seconds: 4, resolution: "480P" },
    },
    {
      label: "H3 720P 5s",
      facts: { material_credits: 0, output_credits: 7.5, input_images: 0, vendor_credits: 7.5, mode: "text", seconds: 5, resolution: "720P" },
    },
    {
      label: "H3 1080P 5s",
      facts: { material_credits: 0, output_credits: 10, input_images: 0, vendor_credits: 10, mode: "text", seconds: 5, resolution: "1080P" },
    },
    {
      label: "H3 2K 15s",
      facts: { material_credits: 0, output_credits: 45, input_images: 0, vendor_credits: 45, mode: "text", seconds: 15, resolution: "2K" },
    },
  ],
  protocols: ["openai_video"],
};

meta.usageProfiles = [
  {
    models: ["MiniMax-H3-2K"],
    schema: meta.usageSchema,
    examples: [
      {
        label: "H3 2K fixed 15s",
        facts: { material_credits: 0, output_credits: 19, input_images: 0, vendor_credits: 19, mode: "text", seconds: 15, resolution: "2K" },
      },
    ],
  },
];

const MIN_SECONDS = 4;
const MAX_SECONDS = 15;
const DEFAULT_SECONDS = 5;
const RATIOS = ["16:9", "9:16", "1:1", "4:3", "3:2", "2:3"];
const TEXT_MODEL_RATIOS = ["16:9", "9:16", "1:1", "3:4", "4:3"];
const BRIDGE_H3_MODEL = "MiniMax-H3-261013";
const UPSTREAM_H3_MODEL = "MiniMax-H3";
const MODELS = [UPSTREAM_H3_MODEL, BRIDGE_H3_MODEL, "MiniMax-H3-2K", "MiniMax-H3-Sensitive"];
const OUTPUT_CREDITS_PER_SECOND = { "480P": 0.8, "720P": 1.5, "1080P": 2, "2K": 3 };

function trimmed(value) {
  return value === undefined || value === null ? "" : String(value).trim();
}

function firstString(values) {
  for (let index = 0; index < values.length; index++) {
    const value = trimmed(values[index]);
    if (value) return value;
  }
  return "";
}

function metadataOf(request) {
  const metadata = request && request.metadata;
  if (metadata && typeof metadata === "object" && !Array.isArray(metadata)) return metadata;
  return {};
}

function normalizeResolution(value) {
  const raw = trimmed(value).toLowerCase();
  if (!raw) return "";
  if (raw.indexOf("2k") >= 0) return "2K";
  if (raw.indexOf("1080") >= 0) return "1080P";
  if (raw.indexOf("720") >= 0) return "720P";
  if (raw.indexOf("480") >= 0) return "480P";
  return "";
}

function resolutionOf(request) {
  const metadata = metadataOf(request);
  const values = [request && request.resolution, request && request.size, metadata.resolution, metadata.size];
  for (let index = 0; index < values.length; index++) {
    if (!trimmed(values[index])) continue;
    const normalized = normalizeResolution(values[index]);
    if (!normalized) throw new Error("unsupported resolution; bridge supports 480P, 720P, 1080P and 2K");
    return normalized;
  }
  return "480P";
}

function secondsOf(request) {
  const metadata = metadataOf(request);
  const raw = firstString([request && request.seconds, request && request.duration, metadata.seconds, metadata.duration]);
  if (!raw) return DEFAULT_SECONDS;
  const seconds = Number(raw);
  if (!Number.isFinite(seconds) || Math.floor(seconds) !== seconds || seconds < MIN_SECONDS || seconds > MAX_SECONDS) {
    throw new Error("duration must be an integer between " + MIN_SECONDS + " and " + MAX_SECONDS + " seconds");
  }
  return seconds;
}

function ratioOf(request, model) {
  const metadata = metadataOf(request);
  const candidates = [request && request.aspect_ratio, request && request.ratio, metadata.aspect_ratio, metadata.ratio];
  for (let index = 0; index < candidates.length; index++) {
    const value = trimmed(candidates[index]);
    if (!value) continue;
    const allowed = model === "MiniMax-H3-2K" ? TEXT_MODEL_RATIOS : RATIOS;
    if (allowed.indexOf(value) < 0) throw new Error("unsupported aspect_ratio for " + model);
    return value;
  }
  return "16:9";
}

function promptOf(request) {
  const metadata = metadataOf(request);
  return firstString([request && request.prompt, request && request.input, metadata.prompt]);
}

function listOf(value) {
  if (Array.isArray(value)) return value;
  const single = trimmed(value);
  return single ? [single] : [];
}

function collectUrls(values, limit) {
  const urls = [];
  for (let index = 0; index < values.length; index++) {
    let value = values[index];
    if (value && typeof value === "object" && !Array.isArray(value)) {
      value = firstString([value.url, value.image_url, value.video_url, value.audio_url, value.file_url]);
    }
    const url = trimmed(value);
    if (url && urls.indexOf(url) < 0) urls.push(url);
    if (urls.length > limit) throw new Error("reference count exceeds upstream limit");
  }
  return urls;
}

function normalizeRequest(request, model) {
  if (!request || typeof request !== "object" || Array.isArray(request)) {
    throw new Error("request body must be an object");
  }
  const prompt = promptOf(request);
  if (!prompt) throw new Error("prompt is required");
  if (MODELS.indexOf(model) < 0) throw new Error("unsupported bridge model");
  const fixed2K = model === "MiniMax-H3-2K";
  if (prompt.length > (fixed2K ? 7000 : 30000)) throw new Error("prompt exceeds upstream length limit");
  const metadata = metadataOf(request);
  const normalized = {
    prompt: prompt,
    seconds: fixed2K ? 15 : secondsOf(request),
    resolution: fixed2K ? "2K" : resolutionOf(request),
    ratio: ratioOf(request, model),
  };
  const images = collectUrls(listOf(request.images).concat(listOf(request.image)).concat(listOf(request.input_reference)).concat(listOf(metadata.images)), 9);
  const videos = collectUrls(listOf(request.videos).concat(listOf(request.video)).concat(listOf(metadata.videos)), 3);
  const audios = collectUrls(listOf(request.audios).concat(listOf(request.audio)).concat(listOf(metadata.audios)), 3);
  if (images.length) normalized.images = images;
  if (videos.length) normalized.videos = videos;
  if (audios.length) normalized.audios = audios;
  return normalized;
}

function upstreamModelOf(model) {
  return model === BRIDGE_H3_MODEL ? UPSTREAM_H3_MODEL : model;
}

export function buildSubmitRequest(ctx) {
  const model = ctx.upstreamModel || ctx.model;
  const request = normalizeRequest(ctx.requestBody, model);
  const body = {
    model: upstreamModelOf(model),
    prompt: request.prompt,
    seconds: request.seconds,
    resolution: request.resolution,
    size: request.resolution.toLowerCase(),
    ratio: request.ratio,
  };
  if (request.images) body.images = request.images;
  if (request.videos) body.videos = request.videos;
  if (request.audios) body.audios = request.audios;
  return {
    url: ctx.baseUrl + "/v1/videos",
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      Authorization: "Bearer " + ctx.apiKey,
    },
    body: body,
    action: request.images && request.images.length ? "image_to_video" : "text_to_video",
  };
}

function upstreamTaskId(body) {
  if (!body || typeof body !== "object" || Array.isArray(body)) return "";
  const nested = body.data && typeof body.data === "object" && !Array.isArray(body.data) ? body.data : {};
  return firstString([body.id, body.task_id, body.taskId, nested.id, nested.task_id, nested.taskId]);
}

export function parseSubmitResponse(ctx, resp) {
  const body = resp && resp.body;
  const taskId = upstreamTaskId(body);
  if (!taskId) throw new Error("bridge gateway returned no task id");
  const quoted = extractUsage(ctx);
  return { taskId: taskId, taskData: body, state: { quotedOutputCredits: quoted.output_credits } };
}

export function extractUsage(ctx) {
  if (ctx && ctx.usagePurpose === "billing_ratios") return null;
  const model = ctx.upstreamModel || ctx.model;
  const request = normalizeRequest(ctx.requestBody, model);
  const outputCredits = model === "MiniMax-H3-2K" ? 19 : OUTPUT_CREDITS_PER_SECOND[request.resolution] * request.seconds;
  return {
    output_credits: outputCredits,
    material_credits: 0,
    input_images: (request.images || []).length,
    vendor_credits: outputCredits,
    mode: request.images && request.images.length ? "reference" : "text",
    seconds: request.seconds,
    resolution: request.resolution,
  };
}

export function extractUsageOnComplete(ctx, result, body) {
  if (String(result && result.status).toUpperCase() !== "SUCCESS") return null;
  const usage = body && typeof body.usage === "object" ? body.usage : null;
  if (!usage) return null;
  const cost = usage.vendor_credits;
  if (typeof cost !== "number" || !Number.isFinite(cost) || cost < 0 || cost > 2147483647) return null;
  const quoted = ctx && ctx.state && ctx.state.quotedOutputCredits;
  const output = typeof quoted === "number" && Number.isFinite(quoted) && quoted >= 0 ? Math.min(quoted, cost) : cost;
  return { vendor_credits: cost, output_credits: output, material_credits: cost - output };
}

export function buildQueryRequest(ctx) {
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId),
    method: "GET",
    headers: {
      Accept: "application/json",
      Authorization: "Bearer " + ctx.apiKey,
    },
  };
}

function videoUrlOf(body) {
  if (!body || typeof body !== "object") return "";
  const content = body.content && typeof body.content === "object" ? body.content : {};
  return firstString([content.video_url, body.result_url, body.url, body.video_url]);
}

export function parseTaskResult(_ctx, body) {
  const status = trimmed(body && body.status).toLowerCase();
  if (!status) return { status: "IN_PROGRESS" };
  if (status === "completed" || status === "succeeded" || status === "success" || status === "done") {
    const result = { status: "SUCCESS", progress: "100%" };
    const url = videoUrlOf(body);
    if (!url) return { status: "IN_PROGRESS", progress: "99%" };
    result.url = url;
    return result;
  }
  if (status === "failed" || status === "error" || status === "cancelled" || status === "canceled") {
    const error = body && body.error && typeof body.error === "object" ? body.error : {};
    return { status: "FAILURE", reason: firstString([error.message, body && body.fail_reason]) || "generation failed" };
  }
  if (status === "queued" || status === "pending" || status === "submitted" || status === "not_start") {
    return { status: "QUEUED" };
  }
  if (status === "in_progress" || status === "processing" || status === "running") {
    return { status: "IN_PROGRESS" };
  }
  return { status: "UNKNOWN", reason: "unknown bridge status: " + status };
}

export function listArtifacts(task) {
  return task && String(task.status).toUpperCase() === "SUCCESS" && videoUrlOf(task.data) ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  const taskId = trimmed(ctx.upstreamTaskId);
  if (!taskId) throw new Error("artifact_not_found");
  return {
    url: ctx.baseUrl.replace(/\/+$/, "") + "/v1/videos/" + encodeURIComponent(taskId) + "/content",
    method: ctx.clientRequest.method,
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

export const protocols = {
  openai_video: {
    decodeRequest: function (ctx) {
      if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) {
        throw new Error("JSON body required");
      }
      if (ctx.body.kind !== "json") throw new Error("multipart requests are not supported");
      const request = ctx.body.value;
      const model = ctx.upstreamModel || ctx.model || trimmed(request.model);
      const normalized = normalizeRequest(request, model);
      normalized.model = model;
      return {
        kind: "submit",
        model: ctx.model || trimmed(request.model),
        action: normalized.images && normalized.images.length ? "image_to_video" : "text_to_video",
        requestBody: normalized,
      };
    },
    render: function (ctx, task) {
      const data = (task && task.data) || {};
      const statusMap = {
        NOT_START: "queued",
        SUBMITTED: "queued",
        QUEUED: "queued",
        IN_PROGRESS: "in_progress",
        SUCCESS: "completed",
        FAILURE: "failed",
      };
      const taskStatus = String((task && task.status) || "").toUpperCase();
      const output = {
        id: task && task.task_id,
        object: "video",
        model: "",
        status: statusMap[taskStatus] || "unknown",
        progress: Number(String((task && task.progress) || "0").replace("%", "")),
        created_at: task && task.created_at,
        completed_at: task && task.updated_at,
      };
      const url = videoUrlOf(data);
      if (url) {
        output.result_url = url;
        output.url = url;
        output.content = { video_url: url };
      }
      const usage = data && typeof data.usage === "object" ? data.usage : null;
      const actualCost = usage && usage.vendor_credits;
      if (typeof actualCost === "number" && Number.isFinite(actualCost) && actualCost >= 0) {
        output.usage = { vendor_credits: actualCost };
      }
      if (taskStatus === "FAILURE") {
        const error = data && data.error && typeof data.error === "object" ? data.error : {};
        output.error = {
          code: firstString([error.code]) || "generation_failed",
          message: firstString([task && task.fail_reason, error.message]) || "generation failed",
        };
      }
      return output;
    },
  },
};
