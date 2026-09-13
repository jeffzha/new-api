import { afterEach, describe, expect, mock, spyOn, test } from "bun:test";
import { loadInvitationPNG } from "../png";

afterEach(() => {
  mock.restore();
});

describe("invitation QR download", () => {
  test("retains the exact PNG bytes and forwards cancellation without credentials", async () => {
    const png = Uint8Array.from(
      atob(
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aO4sAAAAASUVORK5CYII=",
      ),
      (char) => char.charCodeAt(0),
    );
    const fetchMock = spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(png, { headers: { "Content-Type": "image/png" } }),
    );
    const controller = new AbortController();
    const url = "https://gateway.example/partners/api/v1/public/invitations/OWNINVITE/qr";
    const blob = await loadInvitationPNG(url, controller.signal);
    expect(blob.type).toBe("image/png");
    expect(new Uint8Array(await blob.arrayBuffer())).toEqual(png);
    expect(fetchMock).toHaveBeenCalledWith(url, {
      signal: controller.signal,
      credentials: "omit",
      cache: "no-store",
    });
  });

  test.each([
    [503, "application/json", '{"error":"unavailable"}'],
    [200, "text/html", "<html>sign in</html>"],
    [200, "image/png", "not a PNG"],
    [200, "image/png", ""],
  ])("rejects an invalid download (%s, %s)", async (status, contentType, body) => {
    spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(body, {
        status,
        headers: { "Content-Type": contentType },
      }),
    );
    await expect(
      loadInvitationPNG("https://gateway.example/qr", new AbortController().signal),
    ).rejects.toThrow("The invitation QR code could not be loaded. Try again.");
  });

  test("propagates a cancelled request instead of producing a download", async () => {
    const controller = new AbortController();
    controller.abort();
    spyOn(globalThis, "fetch").mockRejectedValue(new DOMException("Aborted", "AbortError"));
    await expect(
      loadInvitationPNG("https://gateway.example/qr", controller.signal),
    ).rejects.toMatchObject({ name: "AbortError" });
  });
});
