import { describe, expect, it } from "vitest";
import { buildDeployPayload, envMapOf } from "./deployPayload";

// 四源载荷构造锚：互斥字段组、空值缺席（protojson 零值口径）、port/
// protocol 的成对面（"protocol is only meaningful together with a
// declared port"）、env 文本域解析。

const base: Parameters<typeof buildDeployPayload>[0] = {
  appId: "01M4APP",
  source: "image",
  image: "nginx:1.27",
  processName: "",
  port: "",
  protocol: "http",
  envText: "",
  httpProbe: "",
  tcpProbe: "",
  composeYaml: "",
  specJson: "",
  uploadId: "",
  builder: "dockerfile",
  dockerfile: "",
  railpackVersion: "",
  outputDir: "",
  idempotencyKey: "",
  commitSha: "",
  supersede: false,
};

describe("buildDeployPayload", () => {
  it("sends only the image face for the image source", () => {
    const payload = buildDeployPayload({ ...base, port: "8080", protocol: "h2c", envText: "A=1\nB=2", httpProbe: "/healthz" });
    expect(payload).toEqual({
      app_id: "01M4APP",
      image: "nginx:1.27",
      port: 8080,
      protocol: "h2c",
      env: { A: "1", B: "2" },
      http_probe: "/healthz",
    });
    expect("compose_yaml" in payload).toBe(false);
    expect("spec_file" in payload).toBe(false);
    expect("upload_id" in payload).toBe(false);
  });

  it("omits protocol without a declared port (server-side pairing rule)", () => {
    const payload = buildDeployPayload(base);
    expect("port" in payload).toBe(false);
    expect("protocol" in payload).toBe(false);
  });

  it("carries the compose face verbatim", () => {
    const payload = buildDeployPayload({ ...base, source: "compose", composeYaml: "services:\n  web:\n    image: nginx:1.27\n" });
    expect(payload).toEqual({ app_id: "01M4APP", compose_yaml: "services:\n  web:\n    image: nginx:1.27\n" });
  });

  it("carries the spec file face verbatim", () => {
    const payload = buildDeployPayload({ ...base, source: "spec", specJson: '{"processes":[]}' });
    expect(payload).toEqual({ app_id: "01M4APP", spec_file: '{"processes":[]}' });
  });

  it("shapes the upload face (builder default omitted, railpack version passed through)", () => {
    const payload = buildDeployPayload({
      ...base,
      source: "upload",
      uploadId: "01M4UP",
      builder: "railpack",
      railpackVersion: "0.39.0",
      dockerfile: "Dockerfile.dev",
    });
    expect(payload).toEqual({
      app_id: "01M4APP",
      upload_id: "01M4UP",
      builder: "railpack",
      dockerfile: "Dockerfile.dev",
      railpack_version: "0.39.0",
    });
  });

  it("passes through admission flags only when set", () => {
    const flagged = buildDeployPayload({ ...base, idempotencyKey: "k1", commitSha: "deadbeef", supersede: true });
    expect(flagged.idempotency_key).toBe("k1");
    expect(flagged.commit_sha).toBe("deadbeef");
    expect(flagged.supersede).toBe(true);
    expect("supersede" in buildDeployPayload(base)).toBe(false);
  });
});

describe("envMapOf", () => {
  it("parses KEY=VALUE lines and skips blanks, comments and malformed lines", () => {
    expect(envMapOf("A=1\n\n# comment\nBAD\nB=x=y")).toEqual({ A: "1", B: "x=y" });
  });
});
