export function agencySignInURL(
  platformBaseURL: string,
  currentOrigin: string,
  signedOut = false,
): string {
  const target = new URL("/sign-in?mode=agency", platformBaseURL || currentOrigin);
  if (signedOut) target.searchParams.set("agency_signed_out", "true");
  return target.href;
}
