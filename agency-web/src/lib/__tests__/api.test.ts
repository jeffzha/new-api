import { describe, expect, test } from "bun:test";
import { bodyHash, canonicalBody } from "../api";

describe("verification payload canonicalization", () => {
  test("sorts nested keys, omits absent fields, and preserves explicit zero and null", () => {
    expect(canonicalBody({ z: [{ c: null, b: false, a: 0 }], a: undefined })).toBe(
      '{"z":[{"a":0,"b":false,"c":null}]}',
    );
    expect(canonicalBody({ payment_lease_token: "1789000000123456789" })).toBe(
      '{"payment_lease_token":"1789000000123456789"}',
    );
  });
  test("matches Go HTML escaping for values and object keys", () => {
    expect(canonicalBody({ "<key>": "a&b\u2028\u2029", text: "中文" })).toBe(
      '{"\\u003ckey\\u003e":"a\\u0026b\\u2028\\u2029","text":"中文"}',
    );
  });
  test("rejects imprecise numbers instead of signing a rounded or null value", () => {
    for (const amount of [Number.MAX_SAFE_INTEGER + 1, NaN, Infinity, -Infinity, 0.1]) {
      expect(() => canonicalBody({ amount })).toThrow("Use an exact integer or a decimal string.");
    }
  });
  test("hashes the exact sorted wire payload", async () => {
    expect(await bodyHash({})).toBe(
      "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
    );
    expect(await bodyHash({ b: 2, a: 1 })).toBe(await bodyHash({ a: 1, b: 2 }));
    expect(await bodyHash({ a: 0 })).not.toBe(await bodyHash({ a: null }));
  });
});
