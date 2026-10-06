import { describe, expect, it } from "vitest";

import { buildTemplateValues, inputKindOf } from "./templateForm";

const decls = [
  { name: "title", type: "string", description: "", default: "", required: false },
  { name: "admin_password", type: "secret", description: "", default: "", required: false },
  { name: "host", type: "domain", description: "", default: "", required: true },
];

describe("inputKindOf", () => {
  it("secret variables never take form input (platform-generated)", () => {
    expect(inputKindOf(decls[1])).toBe("none");
    expect(inputKindOf(decls[0])).toBe("text");
    expect(inputKindOf(decls[2])).toBe("text");
  });
});

describe("buildTemplateValues", () => {
  it("omits empty values so defaults/required server semantics take over", () => {
    expect(buildTemplateValues(decls, { title: "  ", host: "demo.example.org" })).toEqual({
      host: "demo.example.org",
    });
  });
  it("never carries secret variables into values", () => {
    const values = buildTemplateValues(decls, { title: "hi", admin_password: "x", host: "h.example.org" });
    expect(values).toEqual({ title: "hi", host: "h.example.org" });
  });
});
