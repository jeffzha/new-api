import { expect, test } from "bun:test";
import { buildSubmitRequest, extractUsage, extractUsageOnComplete, meta, parseTaskResult, protocols } from "./cn-locopal-video.js";

function submit(requestBody: Record<string, unknown>, upstreamModel = "h3-video") {
  return buildSubmitRequest({ baseUrl: "https://locopalai.com", apiKey: "test-only", upstreamModel, requestBody });
}

test("H3 ordinary image input reaches upstream content exactly once", () => {
  expect(meta.key).toBe("cn-locopal-video");
  expect(meta.version).toBe("1.0.6");
  const requestBody = { model: "MiniMax-H3", prompt: "a city", images: ["https://example.test/frame.png"], seconds: 5, metadata: { ratio: "adaptive" } };
  const request = submit(requestBody);
  expect(request.url).toBe("https://locopalai.com/v1/videos");
  expect(request.action).toBe("image_to_video");
  expect(request.body.content).toEqual([{ type: "image_url", role: "first_frame", image_url: { url: "https://example.test/frame.png" } }]);
  expect(request.body.images).toBeUndefined();
  expect(request.body.ratio).toBe("adaptive");
  expect(extractUsage({ upstreamModel: "h3-video", requestBody })).toMatchObject({ input_images: 1, input_video_seconds: 0, seconds: 5 });
  const decoded = protocols.openai_video.decodeRequest({ model: "MiniMax-H3", upstreamModel: "h3-video", body: { kind: "json", value: requestBody } });
  expect(submit(decoded.requestBody).body.content).toEqual(request.body.content);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: decoded.requestBody }).input_images).toBe(1);
});

test("H3 image aliases always map to content and duplicate URLs bill once", () => {
  const request = submit({ prompt: "p", image_urls: ["a.png"], metadata: { ratio: "adaptive" } });
  expect(request.body.content).toEqual([{ type: "image_url", role: "first_frame", image_url: { url: "a.png" } }]);
  expect(request.body.image_urls).toBeUndefined();
  const multiple = submit({ prompt: "p", image_urls: ["a.png", "b.png"], metadata: { ratio: "adaptive" } });
  expect(multiple.body.content).toEqual([
    { type: "image_url", role: "first_frame", image_url: { url: "a.png" } },
    { type: "image_url", role: "last_frame", image_url: { url: "b.png" } },
  ]);
  expect(multiple.body.image_urls).toBeUndefined();
  const mixed = { prompt: "p", images: ["same.png"], image: "same.png", metadata: { ratio: "adaptive", first_frame_image: "same.png" } };
  expect(submit(mixed).body.content).toEqual([{ type: "image_url", role: "first_frame", image_url: { url: "same.png" } }]);
  expect(submit(mixed).body.images).toBeUndefined();
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: mixed }).input_images).toBe(1);
  const last = submit({ prompt: "p", metadata: { ratio: "adaptive", last_frame_image: "last.png" } });
  expect(last.body.content).toEqual([{ type: "image_url", role: "last_frame", image_url: { url: "last.png" } }]);
  const references = submit({ prompt: "p", images: ["a.png", "b.png", "c.png"], metadata: { ratio: "adaptive" } });
  expect(references.body.content).toHaveLength(3);
});

test("H3 explicit content and reference media merge into one canonical content list", () => {
  const explicit = { prompt: "p", content: [{ type: "image_url", role: "reference_image", image_url: { url: "reference.png" } }], images: ["legacy.png"], metadata: { ratio: "adaptive" } };
  expect(submit(explicit).body.content).toEqual(explicit.content.concat([{ type: "image_url", role: "first_frame", image_url: { url: "legacy.png" } }]));
  expect(submit(explicit).body.images).toBeUndefined();
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: explicit }).input_images).toBe(2);
  const reference = { prompt: "p", metadata: { reference_video: "clip.mp4", ratio: "adaptive" } };
  expect(submit(reference).body.content).toEqual([{ type: "video_url", role: "reference_video", video_url: { url: "clip.mp4" } }]);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: reference }).input_video_seconds).toBe(15);
  const decoded = protocols.openai_video.decodeRequest({ model: "MiniMax-H3", upstreamModel: "h3-video", body: { kind: "json", value: reference } });
  expect(submit(decoded.requestBody).body.content).toEqual(submit(reference).body.content);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: decoded.requestBody }).input_video_seconds).toBe(15);
});

test("H3 preserves explicit frame roles while aliases do not add another charge", () => {
  const requestBody = {
    prompt: "p",
    content: [
      { type: "image_url", role: "first_frame", image_url: { url: "same.png" } },
      { type: "image_url", role: "last_frame", image_url: { url: "same.png" } },
    ],
    image: "same.png",
    ratio: "9:16",
  };
  expect(submit(requestBody).body.content).toEqual(requestBody.content);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody }).input_images).toBe(2);
});

test("H3 reference audio joins the canonical content list", () => {
  const request = submit({ prompt: "p", metadata: { reference_audio: ["voice.mp3"] } });
  expect(request.body.content).toEqual([{ type: "audio_url", role: "reference_audio", audio_url: { url: "voice.mp3" } }]);
  expect(request.body.metadata.reference_audio).toBeUndefined();
});

test("H3 fallback images stay stable across decode, submit, and billing", () => {
  const original = { prompt: "p", image_urls: ["a.png"], metadata: { ratio: "adaptive" } };
  const decoded = protocols.openai_video.decodeRequest({ model: "MiniMax-H3", upstreamModel: "h3-video", body: { kind: "json", value: original } });
  const once = submit(original);
  const twice = submit(decoded.requestBody);
  expect(twice.body.content).toEqual(once.body.content);
  expect(once.body.content.map((item: { role: string }) => item.role)).toEqual(["first_frame"]);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: decoded.requestBody }).input_images).toBe(1);
  const explicit = { prompt: "p", content: [{ type: "text", text: "p" }, { type: "image_url", role: "reference_image", image_url: { url: "a.png" } }], images: ["a.png"] };
  expect(submit(explicit).body.content).toEqual(explicit.content);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: explicit }).input_images).toBe(1);
});

test("H3 text-only and Seedance requests are unchanged", () => {
  const text = submit({ prompt: "p", seconds: 5 });
  expect(text.action).toBe("text_to_video");
  expect(text.body.ratio).toBe("16:9");
  expect(text.body.content).toBeUndefined();
  expect(() => submit({ prompt: "p", metadata: { ratio: "adaptive" } })).toThrow("ratio adaptive requires an image or video input");
  const portrait = { prompt: "p", images: ["frame.png"], ratio: "9:16" };
  expect(submit(portrait).body.content).toEqual([{ type: "image_url", role: "first_frame", image_url: { url: "frame.png" } }]);
  expect(submit(portrait).body.images).toBeUndefined();
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: portrait }).input_images).toBe(1);
  const seedance = submit({ prompt: "p", duration: 5, size: "720p" }, "doubao-seedance-2-0-fast-260128");
  expect(seedance.body.content).toBeUndefined();
  expect(seedance.body.resolution).toBe("720p");
  expect(seedance.action).toBe("text_to_video");
});

test("H3 watermark strings become booleans without changing existing booleans or Seedance", () => {
  const text = { prompt: "p", aigc_watermark: "false", metadata: { aigc_watermark: "true" } };
  const request = submit(text);
  expect(request.body.aigc_watermark).toBe(false);
  expect(request.body.metadata.aigc_watermark).toBeUndefined();
  const decoded = protocols.openai_video.decodeRequest({ model: "MiniMax-H3", upstreamModel: "h3-video", body: { kind: "json", value: text } });
  expect(submit(decoded.requestBody).body.aigc_watermark).toBe(false);
  expect(submit({ prompt: "p", aigc_watermark: true }).body.aigc_watermark).toBe(true);
  expect(submit({ prompt: "p", aigc_watermark: "yes" }).body.aigc_watermark).toBe("yes");
  expect(submit({ prompt: "p", aigc_watermark: "false" }, "doubao-seedance-2-0-fast-260128").body.aigc_watermark).toBe("false");
});

test("H3 validates LocoPal 2K ratios before submitting upstream", () => {
  expect(() => submit({ prompt: "p", resolution: "2K", image_urls: ["frame.png"] })).toThrow("2K requires ratio 16:9, 9:16, or 1:1");
  expect(submit({ prompt: "p", resolution: "2K", ratio: "9:16", image_urls: ["frame.png"] }).body.ratio).toBe("9:16");
});

test("H3 rejects bare Base64 images before billing or upstream submission", () => {
  const bareBase64 = "A".repeat(160);
  expect(() => submit({ prompt: "p", image_urls: [bareBase64] })).toThrow("bare Base64 is not supported");
  expect(submit({ prompt: "p", image_urls: ["data:image/png;base64," + bareBase64] }).body.content).toHaveLength(1);
});

test("H3 completion only overrides reference video seconds when upstream confirms them", () => {
  const facts = extractUsageOnComplete(
    { upstreamModel: "h3-video" },
    { status: "SUCCESS" },
    { status: "succeeded", resolution: "768P", duration: 8, usage: { output_seconds: 8 } },
  );
  expect(facts).toEqual({ seconds: 8, resolution: "768P" });
});

test("H3 completed tasks without a video URL fail after a bounded grace period", () => {
  const first = parseTaskResult({}, { status: "completed" });
  expect(first).toMatchObject({ status: "IN_PROGRESS", progress: "99%", state: { missingResultPolls: 1 } });
  let current = first;
  for (let count = 2; count < 20; count++) current = parseTaskResult({ state: current.state }, { status: "completed" });
  expect(current).toMatchObject({ status: "IN_PROGRESS", state: { missingResultPolls: 19 } });
  const terminal = parseTaskResult({ state: current.state }, { status: "completed" });
  expect(terminal).toMatchObject({ status: "FAILURE", reason: "LocoPal reported completion but did not return a video URL" });
});

test("H3 multipart image files use the same canonical content contract", () => {
  const decoded = protocols.openai_video.decodeRequest({
    model: "MiniMax-H3",
    upstreamModel: "h3-video",
    body: {
      kind: "multipart",
      fields: { prompt: ["p"], seconds: ["5"], ratio: ["9:16"] },
      files: [{ ref: "request_file:input_reference", field: "input_reference", filename: "frame.png", mimeType: "image/png", size: 123 }],
    },
  });
  expect(decoded.requestBody.content).toEqual([{
    type: "image_url",
    role: "first_frame",
    image_url: { url: { __fileRef: "request_file:input_reference", encoding: "dataUrl", mimeType: "image/png" } },
  }]);
  expect(submit(decoded.requestBody).body.content).toEqual(decoded.requestBody.content);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: decoded.requestBody }).input_images).toBe(1);
});

test("H3 multipart rejects ambiguous multiple reference videos", () => {
  expect(() => protocols.openai_video.decodeRequest({
    model: "MiniMax-H3",
    upstreamModel: "h3-video",
    body: {
      kind: "multipart",
      fields: { prompt: ["p"] },
      files: [
        { ref: "request_file:first", field: "first", filename: "first.mp4", mimeType: "video/mp4", size: 123 },
        { ref: "request_file:second", field: "second", filename: "second.mp4", mimeType: "video/mp4", size: 456 },
      ],
    },
  })).toThrow("at most one reference video file");
});

test("H3 JSON decoder keeps strict object validation", () => {
  expect(() => protocols.openai_video.decodeRequest({
    model: "MiniMax-H3",
    upstreamModel: "h3-video",
    body: { kind: "json", value: [] },
  })).toThrow("request body must be an object");
});
