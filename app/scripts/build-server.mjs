// Builds the Go server as a Tauri sidecar. Tauri wants it named with the
// Rust target triple; it strips the suffix again when bundling.
import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const triple =
  process.env.TAURI_ENV_TARGET_TRIPLE ||
  execFileSync("rustc", ["-vV"], { encoding: "utf8" }).match(/^host: (\S+)$/m)[1];

const goos = triple.includes("windows") ? "windows" : triple.includes("apple") ? "darwin" : "linux";
const goarch = triple.startsWith("aarch64") ? "arm64" : "amd64";
const ext = goos === "windows" ? ".exe" : "";
const out = join(here, "..", "src-tauri", "binaries", `tailshare-server-${triple}${ext}`);
const version = process.env.npm_package_version || "dev";
// GUI subsystem on Windows: as a console program, the background logon task
// would open a terminal window. Piped stdout still works for the app.
const ldflags = `-s -w -X main.version=${version}` + (goos === "windows" ? " -H windowsgui" : "");

mkdirSync(dirname(out), { recursive: true });
execFileSync("go", ["build", "-trimpath", "-ldflags", ldflags, "-o", out, "./cmd/tailshare-server"], {
  cwd: join(here, "..", "..", "server"),
  stdio: "inherit",
  env: { ...process.env, GOOS: goos, GOARCH: goarch, CGO_ENABLED: "0" },
});
console.log("built", out);
