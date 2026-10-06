import { describe, expect, it } from "vitest";
import { buildTar, rootPrefixOf } from "./tar";

// 确定性 tar 构建器锚：头块 checksum/字段位、目录条目合成、.git 剔除、
// 字典序、结束双零块、同输入同字节（内容寻址去重的前提）。

// fakeFile 造测试 File（webkitRelativePath 模拟目录选择面）。
function fakeFile(path: string, content: string): File {
  const file = new File([content], path.split("/").pop() ?? path);
  Object.defineProperty(file, "webkitRelativePath", { value: path });
  return file;
}

function readString(bytes: Uint8Array, offset: number, length: number): string {
  return String.fromCharCode(...bytes.slice(offset, offset + length));
}

// ustarChecksum 独立复算校验和（与实现不同路径求和——真校验不是自洽）。
function ustarChecksum(block: Uint8Array): number {
  let sum = 0;
  for (let i = 0; i < 512; i++) sum += i >= 148 && i < 156 ? 32 : block[i];
  return sum;
}

describe("buildTar", () => {
  it("writes deterministic ustar bytes with synthesized dir entries", async () => {
    const files = [fakeFile("proj/Dockerfile", "FROM alpine:3.20\n"), fakeFile("proj/web/index.html", "hi\n")];
    const first = await buildTar(files, "proj");
    const second = await buildTar([files[1], files[0]], "proj"); // 输入序无关
    expect(second).toEqual(first);

    // 首块 = Dockerfile（字典序："Dockerfile" < "web/"）。
    expect(readString(first, 0, 10)).toBe("Dockerfile");
    expect(parseInt(readString(first, 100, 8), 8)).toBe(0o644); // mode
    expect(parseInt(readString(first, 124, 12), 8)).toBe(17); // size（"FROM alpine:3.20\n"）
    // checksum：占位区（空格）参与求和后写回 6 位八进制。
    const stored = readString(first, 148, 8);
    expect(parseInt(stored.slice(0, 6), 8)).toBe(ustarChecksum(first.slice(0, 512) as Uint8Array));
    expect(readString(first, 257, 6)).toBe("ustar\0");

    // 第二块 = Dockerfile 数据（17 字节 + 512 对齐填充）。
    expect(readString(first, 512, 4)).toBe("FROM");

    // 第三块 = web/ 目录条目（合成，名尾斜杠、mode 0755、size 0）。
    expect(readString(first, 1024, 4)).toBe("web/");
    expect(parseInt(readString(first, 1024 + 100, 8), 8)).toBe(0o755);

    // 第四块 = web/index.html 头 + 数据块。
    expect(readString(first, 1536, 14)).toBe("web/index.html");

    // 结束双零块 + 尾部对齐：总长 = 5 数据块 + 2 零块。
    expect(first.length).toBe(7 * 512);
    expect(first.slice(5 * 512)).toEqual(new Uint8Array(1024));
  });

  it("skips .git entirely", async () => {
    const files = [fakeFile("proj/app.txt", "x"), fakeFile("proj/.git/config", "git"), fakeFile("proj/.git/objects/ab", "y")];
    const bytes = await buildTar(files, "proj");
    const text = String.fromCharCode(...bytes);
    expect(text).toContain("app.txt");
    expect(text).not.toContain(".git");
  });

  it("rejects ustar-overflowing paths", async () => {
    const deep = `proj/${"a".repeat(100)}/file.txt`;
    await expect(buildTar([fakeFile(deep, "x")], "proj")).rejects.toThrow(/path too long/);
  });
});

describe("rootPrefixOf", () => {
  it("takes the first path segment of a directory selection", () => {
    expect(rootPrefixOf([fakeFile("mydir/a.txt", "x"), fakeFile("mydir/b/c.txt", "y")])).toBe("mydir");
  });
  it("returns empty for bare file selections", () => {
    expect(rootPrefixOf([new File(["x"], "a.txt")])).toBe("");
  });
});
