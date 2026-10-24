import { expect, test } from "bun:test";
import { buildSubmitRequest, extractUsage, meta, protocols } from "./cn-locopal-video.js";

function submit(requestBody: Record<string, unknown>, upstreamModel = "h3-video") {
  return buildSubmitRequest({ baseUrl: "https://locopalai.com", apiKey: "test-only", upstreamModel, requestBody });
}

test("H3 ordinary image input reaches upstream content exactly once", () => {
  expect(meta.key).toBe("cn-locopal-video");
  expect(meta.version).toBe("1.0.5");
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

test("H3 adaptive image aliases map only when the source is unambiguous", () => {
  const request = submit({ prompt: "p", image_urls: ["a.png"], metadata: { ratio: "adaptive" } });
  expect(request.body.content).toEqual([{ type: "image_url", role: "first_frame", image_url: { url: "a.png" } }]);
  expect(request.body.image_urls).toBeUndefined();
  const multiple = submit({ prompt: "p", image_urls: ["a.png", "b.png"], metadata: { ratio: "adaptive" } });
  expect(multiple.body.content).toBeUndefined();
  expect(multiple.body.image_urls).toEqual(["a.png", "b.png"]);
  const mixed = { prompt: "p", images: ["ignored.png"], metadata: { ratio: "adaptive", first_frame_image: "selected.png" } };
  expect(submit(mixed).body.content).toBeUndefined();
  expect(submit(mixed).body.images).toEqual(mixed.images);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: mixed }).input_images).toBe(2);
  const last = submit({ prompt: "p", metadata: { ratio: "adaptive", last_frame_image: "last.png" } });
  expect(last.body.content).toEqual([{ type: "image_url", role: "last_frame", image_url: { url: "last.png" } }]);
  const references = submit({ prompt: "p", images: ["a.png", "b.png", "c.png"], metadata: { ratio: "adaptive" } });
  expect(references.body.content).toBeUndefined();
});

test("H3 explicit content and reference media retain their request shape", () => {
  const explicit = { prompt: "p", content: [{ type: "image_url", role: "reference_image", image_url: { url: "reference.png" } }], images: ["legacy.png"], metadata: { ratio: "adaptive" } };
  expect(submit(explicit).body.content).toEqual(explicit.content);
  expect(submit(explicit).body.images).toEqual(explicit.images);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: explicit }).input_images).toBe(2);
  const reference = { prompt: "p", metadata: { reference_video: "clip.mp4", ratio: "adaptive" } };
  expect(submit(reference).body.content).toEqual([{ type: "video_url", role: "reference_video", video_url: { url: "clip.mp4" } }]);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: reference }).input_video_seconds).toBe(15);
  const decoded = protocols.openai_video.decodeRequest({ model: "MiniMax-H3", upstreamModel: "h3-video", body: { kind: "json", value: reference } });
  expect(submit(decoded.requestBody).body.content).toEqual(submit(reference).body.content);
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: decoded.requestBody }).input_video_seconds).toBe(15);
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
  expect(extractUsage({ upstreamModel: "h3-video", requestBody: explicit }).input_images).toBe(2);
});

test("H3 text-only and Seedance requests are unchanged", () => {
  const text = submit({ prompt: "p", seconds: 5 });
  expect(text.action).toBe("text_to_video");
  expect(text.body.ratio).toBe("16:9");
  expect(text.body.content).toBeUndefined();
  expect(() => submit({ prompt: "p", metadata: { ratio: "adaptive" } })).toThrow("ratio adaptive requires an image or video input");
  const portrait = { prompt: "p", images: ["frame.png"], ratio: "9:16" };
  expect(submit(portrait).body.content).toBeUndefined();
  expect(submit(portrait).body.images).toEqual(portrait.images);
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
  expect(request.body.metadata.aigc_watermark).toBe("true");
  const decoded = protocols.openai_video.decodeRequest({ model: "MiniMax-H3", upstreamModel: "h3-video", body: { kind: "json", value: text } });
  expect(submit(decoded.requestBody).body.aigc_watermark).toBe(false);
  expect(submit({ prompt: "p", aigc_watermark: true }).body.aigc_watermark).toBe(true);
  expect(submit({ prompt: "p", aigc_watermark: "yes" }).body.aigc_watermark).toBe("yes");
  expect(submit({ prompt: "p", aigc_watermark: "false" }, "doubao-seedance-2-0-fast-260128").body.aigc_watermark).toBe("false");
});
