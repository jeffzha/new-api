import { describe, expect, test } from "bun:test";
import { agencySignInURL } from "../navigation";

describe("agency sign-in navigation", () => {
  test("uses automatic SSO for the initial Agency Center entry", () => {
    expect(agencySignInURL("https://platform.example", "https://agency.example")).toBe(
      "https://platform.example/sign-in?mode=agency",
    );
  });

  test("skips automatic SSO when the user chooses manual agency sign-in", () => {
    expect(agencySignInURL("https://platform.example", "https://agency.example", true)).toBe(
      "https://platform.example/sign-in?mode=agency&agency_signed_out=true",
    );
  });

  test("falls back to the current origin when no platform URL is configured", () => {
    expect(agencySignInURL("", "https://agency.example", true)).toBe(
      "https://agency.example/sign-in?mode=agency&agency_signed_out=true",
    );
  });
});
