// Downloads the official rclone build and places it as a Tauri sidecar.
// The hashes are pinned from rclone's SHA256SUMS, whose PGP signature was
// checked against the release key (FBF737EC...54FA) when they were added.
// To update: bump VERSION, check the new SHA256SUMS signature, paste hashes.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync, chmodSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const VERSION = "v1.75.2";
const SHA256 = {
  "linux-amd64": "349ac8fba6ff65d6247043f1750cdcb518ec5d500ef91463a10d37c0ccdf3702",
  "linux-arm64": "7e1e8d69654941b7b7df84ee74c5f7fb09cce7d5496947fb7bf16b55ff427d10",
  "windows-amd64": "8a9db2bad1dc79a65b3348fd857069598e2aab7cf4d118b77b1df23055aff818",
};

const here = dirname(fileURLToPath(import.meta.url));
const triple =
  process.env.TAURI_ENV_TARGET_TRIPLE ||
  execFileSync("rustc", ["-vV"], { encoding: "utf8" }).match(/^host: (\S+)$/m)[1];
const os = triple.includes("windows") ? "windows" : triple.includes("apple") ? "osx" : "linux";
const arch = triple.startsWith("aarch64") ? "arm64" : "amd64";
const platform = `${os}-${arch}`;
const ext = os === "windows" ? ".exe" : "";
const out = join(here, "..", "src-tauri", "binaries", `rclone-${triple}${ext}`);
const stamp = out + ".version";

if (existsSync(out) && existsSync(stamp) && readFileSync(stamp, "utf8") === VERSION) process.exit(0);
const want = SHA256[platform];
if (!want) throw new Error(`no pinned rclone build for ${platform}`);

const name = `rclone-${VERSION}-${platform}`;
const url = `https://downloads.rclone.org/${VERSION}/${name}.zip`;
console.log("downloading", url);
const res = await fetch(url);
if (!res.ok) throw new Error(`${url}: ${res.status}`);
const zip = Buffer.from(await res.arrayBuffer());
const got = createHash("sha256").update(zip).digest("hex");
if (got !== want) throw new Error(`rclone checksum mismatch: got ${got}, want ${want}`);

const tmp = mkdtempSync(join(tmpdir(), "rclone-"));
try {
  const zipPath = join(tmp, "r.zip");
  writeFileSync(zipPath, zip);
  // Windows 10+ ships bsdtar, which reads zips; Linux runners have unzip.
  if (os === "windows") execFileSync("tar", ["-xf", zipPath, "-C", tmp]);
  else execFileSync("unzip", ["-q", zipPath, "-d", tmp]);
  mkdirSync(dirname(out), { recursive: true });
  // Copy, not rename: the temp dir is often on another filesystem.
  copyFileSync(join(tmp, name, "rclone" + ext), out);
  if (os !== "windows") chmodSync(out, 0o755);
  writeFileSync(stamp, VERSION);
} finally {
  rmSync(tmp, { recursive: true, force: true });
}
console.log("placed", out);
