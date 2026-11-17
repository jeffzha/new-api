export const meta = {
  apiVersion: 1,
  key: "oiioii",
  name: "OiiOii Seedance",
  icon: "Doubao.Color",
  description: { en: "Seedance video generation through OiiOii Super2", zh: "通过 OiiOii Super2 生成 Seedance 视频" },
  version: "1.0.1",
  author: { name: "QuantumNous" },
  baseUrl: "https://api.oiioii.tv",
  models: ["doubao-seedance-2-0-260128", "doubao-seedance-2-5-260628"],
  fetchMode: "per_task",
  usageSchema: {
    tokens: { type: "number", unit: "token", description: { en: "Billing token unit price", zh: "计费 Token 单价" } },
    resolution: { enum: ["480p", "720p", "1080p", "4k"], description: { en: "Output video resolution", zh: "输出视频分辨率" } },
    video_input: { enum: ["none", "video"], description: { en: "Reference video input", zh: "参考视频输入" }, enumLabels: { none: { en: "No reference video", zh: "无参考视频" }, video: { en: "With reference video", zh: "有参考视频" } } },
  },
  usageExamples: [{ label: "480p 4s", facts: { tokens: 38430, resolution: "480p", video_input: "none" } }],
  routes: [
    { method: "POST", path: "/oiioii/api/v3/contents/generations/tasks", type: "submit", decode: "createTask", render: "taskCreated" },
    { method: "GET", path: "/oiioii/api/v3/contents/generations/tasks/:task_id", type: "query", render: "taskStatus" },
  ],
  protocols: ["openai_video"],
};

function normalize(body) {
  if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("JSON object required");
  const source = Object.assign({}, body.metadata || {}, body);
  delete source.metadata;
  for (const field of ["input_reference", "image", "image_url", "image_urls", "video", "video_url", "video_urls", "audio_url"]) {
    if (source[field] !== undefined) throw new Error("Send reference media through content[] instead of " + field);
  }
  const model = String(body.model || "").trim();
  if (!model) throw new Error("model is required");
  const content = source.content === undefined ? [] : source.content;
  if (!Array.isArray(content)) throw new Error("content must be an array");
  source.content = content.slice();
  const prompt = String(source.prompt || "").trim();
  if (prompt && !content.some((item) => item && item.type === "text")) source.content.push({ type: "text", text: prompt });
  delete source.prompt;
  if (!source.content.length) throw new Error("content is required");
  for (const item of source.content) {
    if (!item || !["text", "image_url", "video_url", "audio_url"].includes(item.type)) throw new Error("unsupported content type");
  }
  const seconds = Number(source.duration === undefined ? source.seconds === undefined ? 4 : source.seconds : source.duration);
  if (!Number.isInteger(seconds) || seconds < 4 || seconds > 15) throw new Error("duration must be an integer between 4 and 15 seconds");
  source.duration = seconds;
  delete source.seconds;
  const size = String(source.size || "").toLowerCase();
  if (size && !meta.usageSchema.resolution.enum.includes(size)) throw new Error("Use resolution 480p, 720p, 1080p or 4k; pixel dimensions are unsupported");
  source.resolution = String(source.resolution || (["480p", "720p", "1080p", "4k"].includes(size) ? size : "720p")).toLowerCase();
  delete source.size;
  if (!meta.usageSchema.resolution.enum.includes(source.resolution)) throw new Error("unsupported resolution");
  if (model === "doubao-seedance-2-5-260628" && source.resolution === "4k") throw new Error("Seedance 2.5 does not support 4k");
  if (source.ratio !== undefined && !["16:9", "4:3", "1:1", "3:4", "9:16", "21:9", "adaptive"].includes(source.ratio)) throw new Error("unsupported ratio");
  for (const field of ["generate_audio", "watermark"]) {
    if (source[field] !== undefined && typeof source[field] !== "boolean") throw new Error(field + " must be a boolean");
  }
  if (source.callback_url || source.callbackUrl) throw new Error("OiiOii Super2 does not support callbacks; poll the task instead");
  for (const field of ["frames", "seed", "camera_fixed", "service_tier"]) {
    if (source[field] !== undefined) throw new Error("OiiOii Super2 does not support " + field);
  }
  const reference = source.content.some((item) => item && item.type !== "text");
  return { kind: "submit", model: model, action: reference ? "image_to_video" : "text_to_video", requestBody: { model: model, seconds: seconds, metadata: source } };
}

export const native = {
  createTask(ctx) {
    if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
    return normalize(ctx.body.value);
  },
  taskCreated(ctx, task) { return Object.assign({}, task.data || {}, { id: task.task_id }); },
  taskStatus(ctx, task) { return Object.assign({}, task.data || {}, { id: task.task_id }); },
  error(ctx, error) { return { error: { code: error.code, message: error.message } }; },
};

export const protocols = {
  openai_video: {
    decodeRequest(ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("OiiOii Super2 requires a JSON request");
      return normalize(ctx.body.value);
    },
    render(ctx, task) { return Object.assign({}, task.data || {}); },
  },
};

export function buildSubmitRequest(ctx) {
  const body = Object.assign({}, ctx.requestBody.metadata, { model: ctx.upstreamModel || ctx.model });
  return { url: ctx.baseUrl + "/api/v3/contents/generations/tasks", method: "POST", headers: { "Content-Type": "application/json", Authorization: "Bearer " + ctx.apiKey }, body: body, rewriteModel: body.model };
}

export function parseSubmitResponse(ctx, response) {
  if (!response.body || !response.body.id) throw new Error("upstream task id is empty");
  return { taskId: response.body.id, taskData: response.body };
}

export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  const body = ctx.requestBody.metadata;
  const dimensions = { "480p": [854, 480], "720p": [1280, 720], "1080p": [1920, 1080], "4k": [3840, 2160] }[body.resolution];
  const videos = body.content.filter((item) => item && item.type === "video_url").length;
  if (videos > 3) throw new Error("at most 3 reference videos are supported");
  // Reserve the maximum reference length; completion replaces this estimate.
  const seconds = body.duration + videos * 15;
  return { tokens: seconds * dimensions[0] * dimensions[1] * 24 / 1024, resolution: body.resolution, video_input: videos ? "video" : "none" };
}

export function buildQueryRequest(ctx) {
  return { url: ctx.baseUrl + "/api/v3/contents/generations/tasks/" + encodeURIComponent(ctx.taskId), method: "GET", headers: { Authorization: "Bearer " + ctx.apiKey } };
}

export function parseTaskResult(ctx, body) {
  if (body.status === "queued" || body.status === "pending") return { status: "QUEUED", progress: "10%" };
  if (body.status === "running" || body.status === "processing") return { status: "IN_PROGRESS", progress: "50%" };
  if (body.status === "succeeded") {
    const usage = body.usage || {};
    return { status: "SUCCESS", progress: "100%", url: (body.content || {}).video_url || "", completionTokens: usage.completion_tokens === undefined ? usage.total_tokens || 0 : usage.completion_tokens, totalTokens: usage.total_tokens || 0 };
  }
  if (["failed", "expired", "cancelled"].includes(body.status)) return { status: "FAILURE", progress: "100%", reason: (body.error || {}).message || body.status };
  return { status: "UNKNOWN", reason: "unrecognized task status" };
}

export function extractUsageOnComplete(ctx, result, body) {
  if (!body || body.status !== "succeeded") return {};
  const usage = body.usage || {};
  const tokens = Number(usage.completion_tokens === undefined ? usage.total_tokens : usage.completion_tokens);
  const facts = {};
  if (Number.isFinite(tokens) && tokens >= 0) facts.tokens = tokens;
  const resolution = String((body.content || {}).resolution || body.resolution || "").toLowerCase();
  if (meta.usageSchema.resolution.enum.includes(resolution)) facts.resolution = resolution;
  return facts;
}

export function listArtifacts(task) {
  return task.status === "SUCCESS" && (task.data || {}).content && task.data.content.video_url ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  const url = ((ctx.data || {}).content || {}).video_url;
  if (ctx.artifactKey !== "video" || !url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}
