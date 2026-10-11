// Wangpai AI (wangpaiai.com) video generation task plugin.
//
// Vendor contract:
//   submit  POST {base}/api/v1/generations        {"modelId": "...", "inputs": {...}}
//   query   GET  {base}/api/v1/generations/{id}
// Auth: Authorization: Bearer <channel key>.
//
// The plugin drives type-61 "Task Plugin" channels pointed at wangpaiai.com and
// exposes them on the host openai_video protocol (POST /v1/videos,
// GET /v1/videos/:task_id). Downstream model names stay unified: clients ask for
// MiniMax-H3 and the plugin maps it onto the vendor shelf id.
export const meta = {
  apiVersion: 1,
  key: "wangpaiai",
  name: "Wangpai AI",
  icon: "text:王牌",
  description: {
    en: "Wangpai AI video generation (MiniMax H3 multimodal reference-to-video)",
    zh: "王牌AI 视频生成（MiniMax H3 多模态参考生视频）",
  },
  version: "1.0.5",
  author: { name: "NexusReach" },
  baseUrl: "https://wangpaiai.com",
  models: ["MiniMax-H3", "MiniMax-H3-2K"],
  fetchMode: "per_task",
  usageSchema: {
    mode: {
      enum: ["reference", "text"],
      enumLabels: {
        reference: { en: "Reference image to video", zh: "参考图生视频" },
        text: { en: "Prompt only (fixed 15s 2K)", zh: "纯提示词（固定 15 秒 2K）" },
      },
      description: { en: "Vendor path selected for the request", zh: "实际使用的上游生成路径" },
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
    { label: "H3 reference 480P 4s", facts: { mode: "reference", seconds: 4, resolution: "480P" } },
    { label: "H3 reference 720P 5s", facts: { mode: "reference", seconds: 5, resolution: "720P" } },
    { label: "H3 reference 1080P 5s", facts: { mode: "reference", seconds: 5, resolution: "1080P" } },
    { label: "H3 reference 2K 15s", facts: { mode: "reference", seconds: 15, resolution: "2K" } },
    { label: "H3 prompt only (15s 2K)", facts: { mode: "text", seconds: 15, resolution: "2K" } },
  ],
  protocols: ["openai_video"],
};

// Unified downstream model name -> wangpaiai shelf id. The shelf id is stable
// on the vendor side; swap the value here when the vendor rotates the shelf.
const UPSTREAM_MODEL_ID = {
  "MiniMax-H3": "h3-1",
  "MiniMax-H3-2K": "wp_a4f5de6f0c3a",
};

// h3-1/h3-vip are the multimodal (reference-to-video) shelves: the vendor
// rejects them without at least one reference image. Pure prompt requests are
// served by the fixed-price 2K H3 shelf, so one downstream model name covers
// both request shapes.
const H3_REFERENCE_MODEL_ID = "h3-1";
// Fallback for hooks whose context omits the channel Base URL (artifact fetch
// runs from a stored task). Points at the impersonating relay fronting the
// vendor on this deployment.
const RELAY_BASE_URL = "http://172.30.250.1:8788";
const H3_TEXT_MODEL_ID = "wp_a4f5de6f0c3a";
const H3_TEXT_DURATION = "15";
const TEXT_MODEL_RATIOS = ["16:9", "9:16", "1:1", "3:4", "4:3"];

const H3_MODE = "多模态";
const H3_MIN_SECONDS = 4;
const H3_MAX_SECONDS = 15;
const H3_DEFAULT_SECONDS = 5;
const MAX_IMAGES = 9;
const MAX_VIDEOS = 3;
const MAX_AUDIOS = 3;

const RATIOS = ["16:9", "9:16", "1:1", "4:3", "3:2", "2:3"];
const QUALITY_BY_RESOLUTION = {
  "480P": "Pro 480p",
  "720P": "Pro 720p",
  "1080P": "Pro 1080p",
  "2K": "Pro 2k",
};

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

// wangpaiai.com sits behind a Cloudflare managed challenge, so the gateway's own
// HTTP client cannot download generated media from the vendor host. The channel
// Base URL therefore points at a local impersonating relay (see relay/relay.py).
// Only the server-side artifact fetch is rewritten; API responses keep the
// vendor URL so client facing links stay public.
function vendorHostOf(url) {
  const match = /^https?:\/\/([^/?#]+)/i.exec(trimmed(url));
  if (!match) return "";
  return match[1].toLowerCase().split(":")[0];
}

function isVendorMediaUrl(url) {
  const host = vendorHostOf(url);
  if (!host) return false;
  return host === "wangpaiai.com" || host.endsWith(".wangpaiai.com");
}

let relayBaseUrl = "";

function rememberRelayBase(ctx) {
  const base = trimmed(ctx && ctx.baseUrl);
  if (base) relayBaseUrl = base;
  return base || relayBaseUrl || RELAY_BASE_URL;
}

function relayedMediaUrl(ctx, url) {
  const value = trimmed(url);
  if (!value || !isVendorMediaUrl(value)) return value;
  const base = rememberRelayBase(ctx);
  if (!base) return value;
  return base.replace(/\/+$/, "") + "/_fetch?url=" + encodeURIComponent(value);
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
  return (
    normalizeResolution(request && request.resolution) ||
    normalizeResolution(request && request.size) ||
    normalizeResolution(metadata.resolution) ||
    normalizeResolution(metadata.size) ||
    "480P"
  );
}

function secondsOf(request) {
  const metadata = metadataOf(request);
  const raw = firstString([request && request.seconds, request && request.duration, metadata.seconds, metadata.duration]);
  if (!raw) return H3_DEFAULT_SECONDS;
  const seconds = Number(raw);
  if (!Number.isFinite(seconds) || Math.floor(seconds) !== seconds || seconds < H3_MIN_SECONDS || seconds > H3_MAX_SECONDS) {
    throw new Error("duration must be an integer between " + H3_MIN_SECONDS + " and " + H3_MAX_SECONDS + " seconds");
  }
  return seconds;
}

function ratioOf(request) {
  const metadata = metadataOf(request);
  const candidates = [request && request.aspect_ratio, request && request.ratio, metadata.aspect_ratio, metadata.ratio];
  for (let index = 0; index < candidates.length; index++) {
    const value = trimmed(candidates[index]);
    if (RATIOS.indexOf(value) >= 0) return value;
  }
  return "16:9";
}

function collectUrls(values, limit) {
  const urls = [];
  for (let index = 0; index < values.length && urls.length < limit; index++) {
    let value = values[index];
    if (value && typeof value === "object" && !Array.isArray(value)) {
      value = firstString([value.url, value.image_url, value.video_url, value.audio_url, value.file_url]);
    }
    const url = trimmed(value);
    if (url && urls.indexOf(url) < 0) urls.push(url);
  }
  return urls;
}

function listOf(value) {
  if (Array.isArray(value)) return value;
  const single = trimmed(value);
  return single ? [single] : [];
}

function imagesOf(request) {
  const metadata = metadataOf(request);
  return collectUrls(
    listOf(request && request.images)
      .concat(listOf(request && request.image))
      .concat(listOf(request && request.input_reference))
      .concat(listOf(metadata.images))
      .concat(listOf(metadata.image_url)),
    MAX_IMAGES
  );
}

function videosOf(request) {
  const metadata = metadataOf(request);
  return collectUrls(
    listOf(request && request.videos)
      .concat(listOf(request && request.video))
      .concat(listOf(metadata.videos))
      .concat(listOf(metadata.video_url)),
    MAX_VIDEOS
  );
}

function audiosOf(request) {
  const metadata = metadataOf(request);
  return collectUrls(
    listOf(request && request.audios)
      .concat(listOf(request && request.audio))
      .concat(listOf(metadata.audios))
      .concat(listOf(metadata.audio_url)),
    MAX_AUDIOS
  );
}

function promptOf(request) {
  const metadata = metadataOf(request);
  return firstString([request && request.prompt, request && request.input, metadata.prompt]);
}

// Normalizes any accepted client shape into the driver request body the submit
// hook consumes. Throws when the vendor contract cannot be satisfied.
function normalizeRequest(request) {
  if (!request || typeof request !== "object" || Array.isArray(request)) {
    throw new Error("request body must be an object");
  }
  const prompt = promptOf(request);
  if (!prompt) throw new Error("prompt is required");
  const normalized = {
    prompt: prompt,
    seconds: secondsOf(request),
    resolution: resolutionOf(request),
    ratio: ratioOf(request),
  };
  const images = imagesOf(request);
  const videos = videosOf(request);
  const audios = audiosOf(request);
  if (images.length) normalized.images = images;
  if (videos.length) normalized.videos = videos;
  if (audios.length) normalized.audios = audios;
  return normalized;
}

function upstreamModelId(request, model) {
  const name = trimmed(model);
  const shelf = UPSTREAM_MODEL_ID[name] || H3_REFERENCE_MODEL_ID;
  const images = (request && request.images) || [];
  if (shelf !== H3_TEXT_MODEL_ID && !images.length) {
    throw new Error((name || H3_REFERENCE_MODEL_ID) + " requires at least one reference image; use MiniMax-H3-2K for prompt-only requests");
  }
  return shelf;
}

function vendorInputs(request, modelId) {
  if (modelId === H3_TEXT_MODEL_ID) {
    // Fixed-price shelf: 15s 2K, no mode/quality knobs.
    return {
      prompt: request.prompt,
      aspect_ratio: TEXT_MODEL_RATIOS.indexOf(request.ratio) >= 0 ? request.ratio : "16:9",
      duration: H3_TEXT_DURATION,
    };
  }
  const inputs = {
    prompt: request.prompt,
    h3_mode: H3_MODE,
    duration: String(request.seconds),
    quality: QUALITY_BY_RESOLUTION[request.resolution] || QUALITY_BY_RESOLUTION["480P"],
    aspect_ratio: request.ratio,
  };
  const images = request.images || [];
  for (let index = 0; index < images.length; index++) {
    inputs[index === 0 ? "image_url" : "image_url_" + (index + 1)] = images[index];
  }
  const videos = request.videos || [];
  for (let index = 0; index < videos.length; index++) {
    inputs[index === 0 ? "video_url" : "video_url_" + (index + 1)] = videos[index];
  }
  const audios = request.audios || [];
  for (let index = 0; index < audios.length; index++) {
    inputs[index === 0 ? "audio_url" : "audio_url_" + (index + 1)] = audios[index];
  }
  return inputs;
}

export function buildSubmitRequest(ctx) {
  const request = normalizeRequest(ctx.requestBody);
  rememberRelayBase(ctx);
  const modelId = upstreamModelId(request, ctx.model || ctx.upstreamModel);
  return {
    url: ctx.baseUrl + "/api/v1/generations",
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      Authorization: "Bearer " + ctx.apiKey,
    },
    body: {
      modelId: modelId,
      inputs: vendorInputs(request, modelId),
    },
    action: request.images && request.images.length ? "image_to_video" : "text_to_video",
  };
}

export function parseSubmitResponse(_ctx, resp) {
  const body = resp && resp.body;
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    throw new Error("wangpaiai returned an invalid submit response");
  }
  const generation = body.generation && typeof body.generation === "object" ? body.generation : body;
  const taskId = firstString([generation.id, body.id, body.task_id]);
  if (!taskId) throw new Error("wangpaiai returned no generation id");
  return { taskId: taskId, taskData: body };
}

// Billing facts feed the tiered billing expression configured for MiniMax-H3.
// The price table itself lives on the gateway, never in the plugin.
export function extractUsage(ctx) {
  if (ctx && ctx.usagePurpose === "billing_ratios") return null;
  const request = (ctx && ctx.requestBody) || {};
  try {
    return {
      mode: imagesOf(request).length ? "reference" : "text",
      seconds: secondsOf(request),
      resolution: resolutionOf(request),
    };
  } catch (e) {
    return null;
  }
}

export function buildQueryRequest(ctx) {
  rememberRelayBase(ctx);
  return {
    url: ctx.baseUrl + "/api/v1/generations/" + encodeURIComponent(ctx.taskId),
    method: "GET",
    headers: {
      Accept: "application/json",
      Authorization: "Bearer " + ctx.apiKey,
    },
  };
}

function generationOf(body) {
  if (!body || typeof body !== "object") return {};
  if (body.generation && typeof body.generation === "object") return body.generation;
  return body;
}

export function parseTaskResult(ctx, body) {
  const generation = generationOf(body);
  const status = trimmed(generation.status).toLowerCase();
  if (!status) return { status: "IN_PROGRESS" };
  if (status === "done" || status === "completed" || status === "succeeded") {
    const result = { status: "SUCCESS", progress: "100%" };
    const url = firstString([generation.resultUrl, generation.result_url, generation.url]);
    // Store the relay-backed URL: the gateway's own /content proxy fetches the
    // stored result server-side and cannot pass the vendor's Cloudflare check.
    // Client facing responses keep the public vendor URL (see render).
    if (url) result.url = relayedMediaUrl(ctx, url);
    const cost = Number(generation.cost);
    if (Number.isFinite(cost) && cost > 0) result.totalTokens = cost;
    return result;
  }
  if (status === "failed" || status === "error" || status === "canceled" || status === "cancelled") {
    return {
      status: "FAILURE",
      reason: firstString([generation.errorMessage, generation.error]) || "generation failed",
    };
  }
  if (status === "queued" || status === "pending" || status === "created" || status === "submitted") {
    return { status: "QUEUED" };
  }
  if (status === "processing" || status === "in_progress" || status === "running") {
    return { status: "IN_PROGRESS" };
  }
  return { status: "UNKNOWN", reason: "unknown wangpaiai status: " + status };
}

function videoUrlOf(data) {
  const generation = generationOf(data);
  return firstString([generation.resultUrl, generation.result_url, generation.url]);
}

export function listArtifacts(task) {
  return task && String(task.status).toUpperCase() === "SUCCESS" && videoUrlOf(task.data) ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  const url = videoUrlOf(ctx.data);
  if (!url) throw new Error("artifact_not_found");
  return {
    url: relayedMediaUrl(ctx, url),
    method: ctx.clientRequest.method,
    credentialless: true,
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
      const normalized = normalizeRequest(request);
      normalized.model = ctx.model || trimmed(request.model);
      return {
        kind: "submit",
        model: ctx.model || trimmed(request.model),
        action: normalized.images && normalized.images.length ? "image_to_video" : "text_to_video",
        requestBody: normalized,
      };
    },
    render: function (ctx, task) {
      const data = (task && task.data) || {};
      const generation = generationOf(data);
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
      const thumbnail = firstString([generation.thumbUrl, generation.thumb_url]);
      if (thumbnail) output.thumbnail_url = thumbnail;
      if (taskStatus === "FAILURE") {
        output.error = {
          code: firstString([generation.error]) || "generation_failed",
          message: firstString([task && task.fail_reason, generation.errorMessage, generation.error]) || "generation failed",
        };
      }
      return output;
    },
  },
};
