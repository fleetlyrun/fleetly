// 确定性 ustar 构建器（F3.1 上传面）：镜像 CLI tarDir 的确定性纪律
//（cmd/fleetly/cmd/verbs_uploads.go——路径字典序、固定 mtime/uid/gid、
// 仅权限位、.git 整棵不进上传）。浏览器 File 面无 mode 信息，文件恒
// 0644/目录恒 0755——与 CLI tar 的 digest 不同源（CLI 用真实权限位），
// 内容寻址去重按"同工具重传同目录"生效，跨工具不承诺同 digest。
//
// 输入来自 <input webkitdirectory>：webkitRelativePath 首段是所选目录名
// （CLI 的 tar 根），剥除后与 CLI 的相对路径形态对齐。目录条目由文件
// 路径派生合成（FileList 不含目录项）。

const BLOCK = 512;

// octal 写八进制定长字段（NUL 结尾）。
function octal(value: number, length: number): string {
  return value.toString(8).padStart(length - 1, "0") + "\0";
}

// header 铸单文件 ustar 头块（mtime/uid/gid 固定 0——确定性锚）。
function header(name: string, size: number, isDir: boolean): Uint8Array {
  const block = new Uint8Array(BLOCK);
  const encoder = new TextEncoder();
  const typeflag = isDir ? "5" : "0";
  const mode = isDir ? 0o755 : 0o644;
  const fixed = [
    name.padEnd(100, "\0"),
    octal(mode, 8),
    octal(0, 8),
    octal(0, 8),
    octal(size, 12),
    octal(0, 12),
    "        ", // checksum 占位（空格）先行求和
    typeflag,
    "".padEnd(100, "\0"),
    "ustar\0",
    "00",
    "".padEnd(32, "\0"),
    "".padEnd(32, "\0"),
    octal(0, 8),
    octal(0, 8),
    "".padEnd(155, "\0"),
  ].join("");
  block.set(encoder.encode(fixed));
  let sum = 0;
  for (const byte of block) sum += byte;
  // checksum 形态：6 位八进制 + NUL + 空格。
  block.set(encoder.encode(sum.toString(8).padStart(5, "0") + "\0 "), 148);
  return block;
}

function padToBlock(length: number): Uint8Array {
  const remainder = length % BLOCK;
  return new Uint8Array(remainder === 0 ? 0 : BLOCK - remainder);
}

export interface TarEntryInput {
  // path 是剥除根目录后的 POSIX 相对路径（无首段斜杠）。
  path: string;
  content: ArrayBuffer;
}

// buildTar 输出确定性 tar 字节流：条目 = 目录条目（去重合成）+ 文件条目
// 的字典序总排序（目录先于其子文件——路径序天然保证）。
export async function buildTar(files: readonly File[], rootPrefix: string): Promise<Uint8Array> {
  const entries: Array<{ path: string; content?: ArrayBuffer }> = [];
  const dirs = new Set<string>();
  for (const file of files) {
    const relative = relativePath(file, rootPrefix);
    if (relative === null) continue;
    if (relative.split("/").includes(".git")) continue; // VCS 元数据整棵不进上传
    if (relative.length > 99) throw new Error(`path too long for ustar: ${relative}`);
    for (const dir of ancestorDirs(relative)) dirs.add(dir);
    entries.push({ path: relative, content: await file.arrayBuffer() });
  }
  for (const dir of dirs) entries.push({ path: `${dir}/` });
  entries.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));

  const chunks: Uint8Array[] = [];
  for (const entry of entries) {
    const isDir = entry.path.endsWith("/");
    const size = entry.content?.byteLength ?? 0;
    chunks.push(header(entry.path, isDir ? 0 : size, isDir));
    if (entry.content) {
      chunks.push(new Uint8Array(entry.content));
      chunks.push(padToBlock(size));
    }
  }
  chunks.push(new Uint8Array(BLOCK * 2)); // 结束标记：两零块
  let total = 0;
  for (const chunk of chunks) total += chunk.length;
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

// relativePath 剥除所选目录名首段；文件直接选中（无目录输入）时按裸名。
function relativePath(file: File, rootPrefix: string): string | null {
  const raw = (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name;
  const stripped = raw === "" ? file.name : raw;
  if (rootPrefix !== "" && stripped.startsWith(`${rootPrefix}/`)) return stripped.slice(rootPrefix.length + 1);
  return stripped === "" ? null : stripped;
}

// ancestorDirs 列出相对路径的全部祖先目录（含中间层）。
function ancestorDirs(path: string): string[] {
  const parts = path.split("/");
  const dirs: string[] = [];
  for (let i = 1; i < parts.length; i++) dirs.push(parts.slice(0, i).join("/"));
  return dirs;
}

// rootPrefixOf 从 FileList 的首个 webkitRelativePath 取所选目录名（空 =
// 裸文件选择形态）。
export function rootPrefixOf(files: readonly File[]): string {
  const first = files[0] as (File & { webkitRelativePath?: string }) | undefined;
  const raw = first?.webkitRelativePath ?? "";
  const slash = raw.indexOf("/");
  return slash === -1 ? "" : raw.slice(0, slash);
}
