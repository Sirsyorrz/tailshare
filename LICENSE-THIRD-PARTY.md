# Third-party components

tailshare is MIT licensed (see `LICENSE`). It also uses software it did not
write, each under its own licence.

## Bundled with the desktop app

### rclone (MIT)

Mounts shares as drives. Shipped as a separate executable next to the app and
run as a child process.

- Source and licence: <https://github.com/rclone/rclone>

## Downloaded on request, not bundled

### WinFsp (GPLv3 with a FLOSS exception)

Windows filesystem driver needed for drives. When it's missing, the app can
download the official, unmodified installer from the WinFsp releases and run
it.

- Source and licence: <https://github.com/winfsp/winfsp>

### FFmpeg (LGPL v2.1 or later)

Makes video and HEIC/AVIF thumbnails. When turned on, the app downloads an
LGPL build (BtbN FFmpeg-Builds, `lgpl-shared`) and the server runs it as a
separate program; tailshare doesn't link against it. The files sit in the
app's data folder under `ffmpeg/` and can be replaced with any compatible
FFmpeg build.

- Source and licence: <https://ffmpeg.org>
- Build scripts: <https://github.com/BtbN/FFmpeg-Builds>

## Compiled into the server and app

The Go server and the Rust/TypeScript app are built from open-source
libraries, mostly under MIT, Apache 2.0, BSD and ISC, with a few Rust crates
under MPL 2.0. The main ones are Tailscale's `tsnet` (BSD-3-Clause,
<https://github.com/tailscale/tailscale>) and Tauri (MIT or Apache 2.0,
<https://github.com/tauri-apps/tauri>). The full dependency lists are
`server/go.mod`, `app/src-tauri/Cargo.lock` and `app/package-lock.json`; each
library's licence is in its source.
