import { describe, expect, test } from "bun:test";
import { defaultExportForm, exportRequest, exportStatusLabel } from "../contracts";

describe("export filters", () => {
  test("default range uses seven inclusive Shanghai dates across UTC midnight", () => {
    const form = defaultExportForm(new Date("2026-09-01T17:00:00Z"));
    expect(form.startDate).toBe("2026-08-27");
    expect(form.endDate).toBe("2026-09-02");
  });
  test("user IDs retain exact int64 text and the request contains only allowed filters", () => {
    const form = {
      ...defaultExportForm(new Date("2026-09-01T17:00:00Z")),
      userId: "9007199254740993",
      currency: "cny",
      model: "hy3",
    };
    expect(exportRequest(form)).toEqual({
      kind: "usage",
      filter: {
        start_date: "2026-08-27",
        end_date: "2026-09-02",
        user_id: "9007199254740993",
        currency: "CNY",
        model: "hy3",
      },
    });
    expect(exportRequest({ ...form, kind: "topups" }).filter).not.toHaveProperty("model");
  });
  test("invalid calendar days, reversed ranges and overflowing IDs fail before submission", () => {
    const form = defaultExportForm(new Date("2026-09-01T17:00:00Z"));
    expect(() => exportRequest({ ...form, startDate: "2026-02-30" })).toThrow("Choose valid");
    expect(() => exportRequest({ ...form, endDate: "2026-08-01" })).toThrow("end date");
    for (const userId of ["0", "-1", "1.5", "9223372036854775808"])
      expect(() => exportRequest({ ...form, userId })).toThrow("positive user ID");
  });
  test("processing and terminal job statuses have distinct user-facing meanings", () => {
    expect(exportStatusLabel("processing")).toBe("Generating CSV");
    expect(exportStatusLabel("ready")).toBe("Ready to download");
    expect(exportStatusLabel("failed")).toBe("Export failed");
    expect(exportStatusLabel("expired")).toBe("Expired");
  });
});
