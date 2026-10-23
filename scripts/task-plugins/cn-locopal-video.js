const SEEDANCE_MODEL = "doubao-seedance-2-0-fast-260128";
const H3_MODEL = "MiniMax-H3";
const H3_UPSTREAM_MODEL = "h3-video";

const SEEDANCE_USAGE_SCHEMA = {
  tokens: {
    type: "number",
    unit: "token",
    description: { en: "Billing token unit price", zh: "计费 Token 单价" },
  },
  resolution: {
    enum: ["480p", "720p", "1080p", "4k"],
    enumLabels: {
      "480p": { en: "480p", zh: "480p" },
      "720p": { en: "720p", zh: "720p" },
      "1080p": { en: "1080p", zh: "1080p" },
      "4k": { en: "4k", zh: "4k" },
    },
    description: { en: "Output video resolution", zh: "输出视频分辨率" },
  },
  video_input: {
    enum: ["none", "video"],
    enumLabels: {
      none: { en: "No reference video", zh: "无参考视频" },
      video: { en: "With reference video", zh: "有参考视频" },
    },
    description: { en: "Reference video input", zh: "参考视频输入" },
  },
};

const SEEDANCE_USAGE_EXAMPLES = [
  { label: "480p · 5s", facts: { tokens: 48038, resolution: "480p", video_input: "none" } },
  { label: "720p · 5s", facts: { tokens: 108000, resolution: "720p", video_input: "none" } },
  { label: "1080p · 5s", facts: { tokens: 243000, resolution: "1080p", video_input: "none" } },
  { label: "4k · 5s", facts: { tokens: 972000, resolution: "4k", video_input: "none" } },
  { label: "720p · 10s", facts: { tokens: 216000, resolution: "720p", video_input: "none" } },
  { label: "720p · 5s (+4s 输入视频)", facts: { tokens: 194400, resolution: "720p", video_input: "video" } },
];

const H3_USAGE_SCHEMA = {
  seconds: {
    type: "number",
    unit: "second",
    description: { en: "Video generation unit price", zh: "视频生成单价" },
  },
  resolution: {
    enum: ["480P", "512P", "768P", "720P", "1080P", "2K"],
    enumLabels: {
      "480P": { en: "480P", zh: "480P" },
      "512P": { en: "512P", zh: "512P" },
      "768P": { en: "768P", zh: "768P" },
      "720P": { en: "720P", zh: "720P" },
      "1080P": { en: "1080P", zh: "1080P" },
      "2K": { en: "2K", zh: "2K" },
    },
    description: { en: "Output video resolution", zh: "输出视频分辨率" },
  },
  input_images: {
    type: "number",
    unit: "count",
    description: { en: "Input image unit price", zh: "输入图片单价" },
  },
  input_video_seconds: {
    type: "number",
    unit: "second",
    description: { en: "Input video unit price", zh: "输入视频单价" },
  },
};

const H3_USAGE_EXAMPLES = [
  { label: "2.3/02 768P 6s", facts: { seconds: 6, resolution: "768P", input_images: 0, input_video_seconds: 0 } },
  { label: "2.3/02 768P 10s", facts: { seconds: 10, resolution: "768P", input_images: 0, input_video_seconds: 0 } },
  { label: "2.3/02 1080P 6s", facts: { seconds: 6, resolution: "1080P", input_images: 0, input_video_seconds: 0 } },
  { label: "02 512P 6s", facts: { seconds: 6, resolution: "512P", input_images: 0, input_video_seconds: 0 } },
  { label: "02 512P 10s", facts: { seconds: 10, resolution: "512P", input_images: 0, input_video_seconds: 0 } },
  { label: "01-series 720P 6s", facts: { seconds: 6, resolution: "720P", input_images: 0, input_video_seconds: 0 } },
  { label: "H3 768P 5s", facts: { seconds: 5, resolution: "768P", input_images: 0, input_video_seconds: 0 } },
  { label: "H3 2K 5s · 9 images", facts: { seconds: 5, resolution: "2K", input_images: 9, input_video_seconds: 0 } },
  { label: "H3 2K 5s · input video", facts: { seconds: 5, resolution: "2K", input_images: 0, input_video_seconds: 15 } },
];

export const meta = {
  apiVersion: 1,
  key: "cn-locopal-video",
  name: "LocoPal Video",
  icon: "text:LocoPal",
  description: {
    en: "LocoPal Seedance and MiniMax H3 video generation",
    zh: "LocoPal Seedance 与 MiniMax H3 视频生成",
  },
  version: "1.0.3",
  author: { name: "NexusReach" },
  models: [SEEDANCE_MODEL, H3_MODEL],
  fetchMode: "per_task",
  usageSchema: H3_USAGE_SCHEMA,
  usageExamples: H3_USAGE_EXAMPLES,
  usageProfiles: [{ models: [SEEDANCE_MODEL], schema: SEEDANCE_USAGE_SCHEMA, examples: SEEDANCE_USAGE_EXAMPLES }],
  protocols: ["openai_video"],
};

const H3_MIN_SECONDS = 4;
const H3_MAX_SECONDS = 15;
const H3_DEFAULT_SECONDS = 5;
const H3_MAX_IMAGES = 9;
const H3_MAX_INPUT_VIDEO_SECONDS = 15;
const H3_RATIOS = ["adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"];

function trimmed(value) {
  return value === undefined || value === null ? "" : String(value).trim();
}

function objectOf(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function firstString(values) {
  for (const value of values) {
    const text = trimmed(value);
    if (text) return text;
  }
  return "";
}

function upstreamModel(ctx) {
  return trimmed(ctx && (ctx.upstreamModel || ctx.model));
}

function isH3Model(model) {
  return model === H3_MODEL || model === H3_UPSTREAM_MODEL;
}

function promptOf(request) {
  const input = request && request.input;
  return firstString([request && request.prompt, typeof input === "string" ? input : ""]);
}

function durationOf(request, fallback) {
  const metadata = objectOf(request && request.metadata);
  const raw = firstString([request && request.seconds, request && request.duration, metadata.duration]);
  if (!raw) return fallback;
  const seconds = Number(raw);
  if (!Number.isInteger(seconds) || seconds <= 0 || seconds > 3600) throw new Error("duration must be an integer between 1 and 3600 seconds");
  return seconds;
}

function normalizeSeedanceResolution(value) {
  const resolution = trimmed(value).toLowerCase() || "720p";
  if (resolution !== "480p" && resolution !== "720p") {
    throw new Error(SEEDANCE_MODEL + " supports only 480p and 720p on LocoPal");
  }
  return resolution;
}

function seedanceRequest(request, model) {
  const metadata = Object.assign({}, objectOf(request.metadata));
  const prompt = promptOf(request);
  const content = Array.isArray(metadata.content) ? metadata.content : Array.isArray(request.content) ? request.content : [];
  if (!prompt && content.length === 0) throw new Error("prompt or content is required");
  const resolution = normalizeSeedanceResolution(firstString([metadata.resolution, request.resolution, request.size]));
  const seconds = durationOf(request, 15);
  metadata.resolution = resolution;
  if (content.length) metadata.content = content;
  const body = Object.assign({}, request, {
    model: model,
    prompt: prompt,
    seconds: seconds,
    duration: seconds,
    resolution: resolution,
    size: resolution,
    metadata: metadata,
  });
  return { body: body, seconds: seconds, resolution: resolution, content: content };
}

function h3Resolution(request) {
  const metadata = objectOf(request.metadata);
  const size = trimmed(request.size);
  const raw = firstString([metadata.resolution, request.resolution, H3_RATIOS.includes(size) ? "" : size]) || "768P";
  const upper = raw.toUpperCase();
  if (upper.indexOf("2K") >= 0) return "2K";
  if (upper.indexOf("768") >= 0) return "768P";
  throw new Error(H3_MODEL + " resolution must be 768P or 2K");
}

function h3Content(request) {
  const metadata = objectOf(request.metadata);
  return Array.isArray(metadata.content) ? metadata.content : Array.isArray(request.content) ? request.content : [];
}

function mediaCount(request, type) {
  const metadata = objectOf(request.metadata);
  const content = h3Content(request);
  let count = content.filter(function (item) {
    return item && typeof item === "object" && item.type === type;
  }).length;
  if (type === "image_url") {
    const images = Array.isArray(request.images) ? request.images : [];
    count += images.length;
    const imageUrls = Array.isArray(request.image_urls) ? request.image_urls : [];
    count += imageUrls.length;
    if (trimmed(request.image) || trimmed(request.input_reference)) count += 1;
    if (metadata.first_frame_image) count += 1;
    if (metadata.last_frame_image) count += 1;
    const references = Array.isArray(metadata.reference_image) ? metadata.reference_image : metadata.reference_image ? [metadata.reference_image] : [];
    count += references.length;
  }
  if (type === "video_url") {
    const references = Array.isArray(metadata.reference_video) ? metadata.reference_video : metadata.reference_video ? [metadata.reference_video] : [];
    count += references.length;
  }
  return count;
}

function h3Request(request, model) {
  const metadata = Object.assign({}, objectOf(request.metadata));
  const prompt = promptOf(request);
  const content = h3Content(request);
  const hasText = content.some(function (item) {
    return item && typeof item === "object" && item.type === "text" && trimmed(item.text);
  });
  if (!prompt && !hasText) throw new Error(H3_MODEL + " requires a prompt or a text content item");
  const seconds = durationOf(request, H3_DEFAULT_SECONDS);
  if (seconds < H3_MIN_SECONDS || seconds > H3_MAX_SECONDS) {
    throw new Error(H3_MODEL + " duration must be an integer between " + H3_MIN_SECONDS + " and " + H3_MAX_SECONDS + " seconds");
  }
  const resolution = h3Resolution(request);
  const inputImages = mediaCount(request, "image_url");
  if (inputImages > H3_MAX_IMAGES) throw new Error(H3_MODEL + " accepts at most " + H3_MAX_IMAGES + " input images");
  const hasVideo = mediaCount(request, "video_url") > 0;
  const video = objectOf(request.video);
  const size = trimmed(request.size);
  const ratio = firstString([metadata.ratio, request.ratio, request.aspect_ratio, video.aspect, H3_RATIOS.includes(size) ? size : ""]) || (inputImages > 0 || hasVideo ? "adaptive" : "16:9");
  if (H3_RATIOS.indexOf(ratio) < 0) throw new Error(H3_MODEL + " ratio must be one of " + H3_RATIOS.join(", "));
  if (ratio === "adaptive" && inputImages === 0 && !hasVideo) throw new Error(H3_MODEL + " ratio adaptive requires an image or video input");
  metadata.resolution = resolution;
  metadata.ratio = ratio;
  if (content.length) metadata.content = content;
  const body = Object.assign({}, request, {
    model: model,
    prompt: prompt,
    seconds: seconds,
    duration: seconds,
    resolution: resolution,
    ratio: ratio,
    metadata: metadata,
  });
  return {
    body: body,
    seconds: seconds,
    resolution: resolution,
    inputImages: inputImages,
    inputVideoSeconds: hasVideo ? H3_MAX_INPUT_VIDEO_SECONDS : 0,
  };
}

function normalizeRequest(request, model) {
  if (!request || typeof request !== "object" || Array.isArray(request)) throw new Error("request body must be an object");
  if (isH3Model(model)) return h3Request(request, model);
  if (model === SEEDANCE_MODEL) return seedanceRequest(request, model);
  throw new Error("unsupported LocoPal video model");
}

function seedanceHasVideo(content) {
  return content.some(function (item) {
    return item && typeof item === "object" && (item.type === "video_url" || Object.prototype.hasOwnProperty.call(item, "video_url"));
  });
}

function estimateSeedanceTokens(seconds, resolution) {
  const dimensions = resolution === "480p" ? [854, 480] : [1280, 720];
  return (seconds * dimensions[0] * dimensions[1] * 24) / 1024;
}

export function buildSubmitRequest(ctx) {
  const model = upstreamModel(ctx);
  const normalized = normalizeRequest(ctx.requestBody, model);
  return {
    url: ctx.baseUrl + "/v1/videos",
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      Authorization: "Bearer " + ctx.apiKey,
    },
    body: normalized.body,
    action: isH3Model(model)
      ? normalized.inputImages > 0 || normalized.inputVideoSeconds > 0
        ? "image_to_video"
        : "text_to_video"
      : seedanceHasVideo(normalized.content)
        ? "video_to_video"
        : normalized.content.length > 0
          ? "image_to_video"
          : "text_to_video",
  };
}

function upstreamTaskId(body) {
  const data = objectOf(body && body.data);
  return firstString([body && body.id, body && body.task_id, body && body.taskId, data.id, data.task_id, data.taskId]);
}

export function parseSubmitResponse(_ctx, response) {
  const body = response && response.body;
  const taskId = upstreamTaskId(body);
  if (!taskId) throw new Error("LocoPal returned no task id");
  return { taskId: taskId, taskData: body };
}

export function extractUsage(ctx) {
  if (ctx && ctx.usagePurpose === "billing_ratios") return null;
  const model = upstreamModel(ctx);
  const normalized = normalizeRequest(ctx.requestBody, model);
  if (isH3Model(model)) {
    return {
      seconds: normalized.seconds,
      resolution: normalized.resolution,
      input_images: normalized.inputImages,
      input_video_seconds: normalized.inputVideoSeconds,
    };
  }
  return {
    tokens: estimateSeedanceTokens(normalized.seconds, normalized.resolution),
    resolution: normalized.resolution,
    video_input: seedanceHasVideo(normalized.content) ? "video" : "none",
  };
}

export function buildQueryRequest(ctx) {
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId),
    method: "GET",
    headers: { Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
  };
}

function taskEnvelope(body) {
  const data = objectOf(body && body.data);
  const task = objectOf(body && body.task);
  return Object.keys(task).length ? task : Object.keys(data).length ? data : objectOf(body);
}

function videoUrlOf(body) {
  const envelope = taskEnvelope(body);
  const content = objectOf(envelope.content);
  const result = objectOf(envelope.result);
  const metadata = objectOf(envelope.metadata);
  return firstString([
    content.video_url,
    content.url,
    result.video_url,
    result.url,
    metadata.video_url,
    metadata.url,
    envelope.result_url,
    envelope.video_url,
    envelope.url,
    body && body.result_url,
    body && body.video_url,
    body && body.url,
  ]);
}

export function parseTaskResult(_ctx, body) {
  const envelope = taskEnvelope(body);
  const status = trimmed(envelope.status || (body && body.status)).toLowerCase();
  if (["completed", "succeeded", "success", "done"].includes(status)) {
    const url = videoUrlOf(body);
    if (!url) return { status: "IN_PROGRESS", progress: "99%" };
    return { status: "SUCCESS", progress: "100%", url: url };
  }
  if (["failed", "error", "expired", "cancelled", "canceled"].includes(status)) {
    const error = objectOf(envelope.error);
    return { status: "FAILURE", progress: "100%", reason: firstString([error.message, envelope.fail_reason, body && body.message]) || "generation failed" };
  }
  if (["queued", "pending", "submitted", "not_start", "preparing", "queueing"].includes(status)) {
    return { status: "QUEUED", progress: "10%" };
  }
  if (["in_progress", "processing", "running"].includes(status)) {
    return { status: "IN_PROGRESS", progress: "50%" };
  }
  return { status: "UNKNOWN", reason: "unrecognized status: " + status };
}

function numberInRange(value, minimum, maximum) {
  const number = Number(value);
  return Number.isFinite(number) && number >= minimum && number <= maximum ? number : null;
}

export function extractUsageOnComplete(ctx, result, body) {
  if (String(result && result.status).toUpperCase() !== "SUCCESS") return null;
  const model = upstreamModel(ctx);
  const envelope = taskEnvelope(body);
  const usage = objectOf(envelope.usage);
  if (isH3Model(model)) {
    const facts = {};
    const seconds = numberInRange(usage.output_seconds || usage.seconds || envelope.duration, H3_MIN_SECONDS, H3_MAX_SECONDS);
    const images = numberInRange(usage.input_image_count || usage.input_images, 0, H3_MAX_IMAGES);
    const inputSeconds = numberInRange(usage.input_seconds || usage.input_video_seconds, 0, H3_MAX_INPUT_VIDEO_SECONDS);
    const resolution = trimmed(envelope.resolution || usage.resolution).toUpperCase();
    if (seconds !== null) facts.seconds = seconds;
    if (images !== null && Number.isInteger(images)) facts.input_images = images;
    if (inputSeconds !== null) facts.input_video_seconds = inputSeconds;
    if (resolution === "768P" || resolution === "2K") facts.resolution = resolution;
    return Object.keys(facts).length ? facts : null;
  }
  let tokens = numberInRange(usage.completion_tokens, 0, 2147483647);
  if (tokens === null || tokens === 0) tokens = numberInRange(usage.total_tokens, 0, 2147483647);
  const resolution = trimmed((objectOf(envelope.content).resolution || envelope.resolution || usage.resolution)).toLowerCase();
  const facts = {};
  if (tokens !== null && tokens > 0) facts.tokens = tokens;
  if (resolution === "480p" || resolution === "720p") facts.resolution = resolution;
  return Object.keys(facts).length ? facts : null;
}

export function listArtifacts(task) {
  return task && String(task.status).toUpperCase() === "SUCCESS" && videoUrlOf(task.data) ? [{ key: "video", type: "video", mimeType: "video/mp4" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  const url = videoUrlOf(ctx.data);
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export const protocols = {
  openai_video: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const request = ctx.body.value;
      const model = upstreamModel(ctx) || trimmed(request && request.model);
      const normalized = normalizeRequest(request, model);
      return {
        kind: "submit",
        model: ctx.model || trimmed(request.model),
        action: isH3Model(model)
          ? normalized.inputImages > 0 || normalized.inputVideoSeconds > 0
            ? "image_to_video"
            : "text_to_video"
          : seedanceHasVideo(normalized.content)
            ? "video_to_video"
            : normalized.content.length > 0
              ? "image_to_video"
              : "text_to_video",
        requestBody: normalized.body,
      };
    },
    render: function (_ctx, task) {
      const statuses = {
        NOT_START: "queued",
        SUBMITTED: "queued",
        QUEUED: "queued",
        IN_PROGRESS: "in_progress",
        SUCCESS: "completed",
        FAILURE: "failed",
      };
      const status = String((task && task.status) || "").toUpperCase();
      const output = {
        id: task && task.task_id,
        object: "video",
        model: task && task.properties ? task.properties.origin_model_name || "" : "",
        status: statuses[status] || "unknown",
        progress: Number(String((task && task.progress) || "0").replace("%", "")),
        created_at: task && task.created_at,
      };
      if (task && task.updated_at) output.completed_at = task.updated_at;
      const url = videoUrlOf(task && task.data);
      if (url) {
        output.result_url = url;
        output.url = url;
        output.content = { video_url: url };
      }
      if (status === "FAILURE") output.error = { message: (task && task.fail_reason) || "generation failed" };
      return output;
    },
  },
};
