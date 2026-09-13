// The public endpoint can also return JSON/HTML errors. Never offer those
// responses as a misleading .png download, even when HTTP status is 200.
export async function loadInvitationPNG(url: string, signal: AbortSignal): Promise<Blob> {
  const response = await fetch(url, {
    signal,
    credentials: "omit",
    cache: "no-store",
  });
  if (
    !response.ok ||
    response.headers.get("Content-Type")?.split(";", 1)[0].trim().toLowerCase() !== "image/png"
  )
    throw new Error("The invitation QR code could not be loaded. Try again.");
  const bytes = new Uint8Array(await response.arrayBuffer());
  const signature = [137, 80, 78, 71, 13, 10, 26, 10];
  if (bytes.length < signature.length || signature.some((value, index) => bytes[index] !== value))
    throw new Error("The invitation QR code could not be loaded. Try again.");
  return new Blob([bytes], { type: "image/png" });
}
