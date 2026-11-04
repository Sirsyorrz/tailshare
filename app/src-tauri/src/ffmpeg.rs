//! Optional ffmpeg for video and HEIC/AVIF thumbnails, downloaded on request
//! instead of bundled (it would more than double the installer). The server
//! is always told where it would be and looks there before PATH, so an
//! install or removal takes effect without restarting hosting.

use anyhow::{bail, Context};
use serde::Serialize;
use std::path::{Path, PathBuf};
use std::time::Duration;
use tauri::{AppHandle, Emitter};

/// BtbN's LGPL shared builds of a stable release. Month-end autobuilds are
/// kept for about two years; daily ones only for weeks, so pin a month-end
/// one. Hashes are the ones GitHub publishes for the release assets.
struct Build {
    url: &'static str,
    sha256: &'static str,
    size: u64,
}

const VERSION: &str = "n8.1.3";

#[cfg(all(target_os = "linux", target_arch = "x86_64"))]
const BUILD: Option<Build> = Some(Build {
    url: "https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-09-30-13-08/ffmpeg-n8.1.3-9-g29e619e767-linux64-lgpl-shared-8.1.tar.xz",
    sha256: "d4d7b6936f492c0b866b1cb4a29a1bbae09d6a064b4ec4eb90c95bed7969535a",
    size: 65_590_572,
});

#[cfg(all(windows, target_arch = "x86_64"))]
const BUILD: Option<Build> = Some(Build {
    url: "https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-09-30-13-08/ffmpeg-n8.1.3-9-g29e619e767-win64-lgpl-shared-8.1.zip",
    sha256: "3e47bda1607740550141e37c0e49d1e5182b34699f15adfd137ee266d346811a",
    size: 80_733_362,
});

#[cfg(not(any(all(target_os = "linux", target_arch = "x86_64"), all(windows, target_arch = "x86_64"))))]
const BUILD: Option<Build> = None;

fn dir() -> PathBuf {
    crate::data_dir().join("ffmpeg")
}

/// Where the server is told to look, whether or not it's installed.
pub fn bin() -> PathBuf {
    dir().join("bin").join(if cfg!(windows) { "ffmpeg.exe" } else { "ffmpeg" })
}

fn on_path() -> bool {
    let Some(path) = std::env::var_os("PATH") else { return false };
    let name = if cfg!(windows) { "ffmpeg.exe" } else { "ffmpeg" };
    std::env::split_paths(&path).any(|d| d.join(name).is_file())
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct State {
    pub installed: bool,
    /// ffmpeg is on PATH, so the server can use it without a download.
    pub system: bool,
    /// None when there's no pinned build for this platform.
    pub download_bytes: Option<u64>,
}

pub fn state() -> State {
    State { installed: bin().is_file(), system: on_path(), download_bytes: BUILD.map(|b| b.size) }
}

#[derive(Serialize, Clone)]
struct Progress {
    done: u64,
    total: u64,
}

pub async fn install(app: &AppHandle) -> anyhow::Result<()> {
    let Some(build) = BUILD else { bail!("no ffmpeg download for this platform") };
    let data = crate::data_dir();
    std::fs::create_dir_all(&data)?;
    let archive = data.join(if cfg!(windows) { "ffmpeg-download.zip" } else { "ffmpeg-download.tar.xz" });
    let staging = data.join("ffmpeg-staging");
    let result = download(app, &build, &archive).await.and_then(|()| unpack(&archive, &staging));
    let _ = std::fs::remove_file(&archive);
    let _ = std::fs::remove_dir_all(&staging);
    result
}

async fn download(app: &AppHandle, build: &Build, to: &Path) -> anyhow::Result<()> {
    use sha2::Digest;
    use std::io::Write;
    let mut r = crate::api::web_client()
        .get(build.url)
        // The client's default is too short for 80 MB on a slow line.
        .timeout(Duration::from_secs(3600))
        .send()
        .await?
        .error_for_status()?;
    let mut f = std::io::BufWriter::new(std::fs::File::create(to)?);
    let mut hash = sha2::Sha256::new();
    let mut done = 0u64;
    let mut last = 0u64;
    while let Some(chunk) = r.chunk().await? {
        hash.update(&chunk);
        f.write_all(&chunk)?;
        done += chunk.len() as u64;
        if done - last > 1 << 20 {
            last = done;
            let _ = app.emit("ffmpeg-progress", Progress { done, total: build.size });
        }
    }
    f.flush()?;
    let got: String = hash.finalize().iter().map(|b| format!("{b:02x}")).collect();
    if got != build.sha256 {
        bail!("ffmpeg download didn't match the expected checksum");
    }
    Ok(())
}

/// Keeps only what running ffmpeg needs: the binary and its libraries
/// (next to it on Windows, in ../lib on Linux, where its rpath looks).
fn unpack(archive: &Path, staging: &Path) -> anyhow::Result<()> {
    let _ = std::fs::remove_dir_all(staging);
    std::fs::create_dir_all(staging)?;
    // GNU tar reads .tar.xz and Windows' bsdtar reads .zip, so no archive
    // crates are needed.
    let st = crate::quiet_command("tar").arg("-xf").arg(archive).arg("-C").arg(staging).status();
    if !st.context("running tar")?.success() {
        bail!("couldn't unpack ffmpeg");
    }
    let top = std::fs::read_dir(staging)?
        .filter_map(Result::ok)
        .map(|e| e.path())
        .find(|p| p.join("bin").is_dir())
        .context("unexpected ffmpeg archive layout")?;

    let new = crate::data_dir().join("ffmpeg.new");
    let _ = std::fs::remove_dir_all(&new);
    std::fs::create_dir_all(&new)?;
    std::fs::rename(top.join("bin"), new.join("bin"))?;
    for extra in ["ffplay", "ffprobe"] {
        let _ = std::fs::remove_file(new.join("bin").join(if cfg!(windows) { format!("{extra}.exe") } else { extra.into() }));
    }
    if cfg!(not(windows)) {
        std::fs::rename(top.join("lib"), new.join("lib"))?;
        let _ = std::fs::remove_dir_all(new.join("lib").join("pkgconfig"));
    }
    std::fs::write(new.join("version"), VERSION)?;

    let exe = new.join("bin").join(if cfg!(windows) { "ffmpeg.exe" } else { "ffmpeg" });
    let ok = crate::quiet_command(&exe)
        .arg("-version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .is_ok_and(|s| s.success());
    if !ok {
        let _ = std::fs::remove_dir_all(&new);
        bail!("the downloaded ffmpeg doesn't run on this computer");
    }
    remove()?;
    std::fs::rename(&new, dir())?;
    Ok(())
}

pub fn remove() -> anyhow::Result<()> {
    match std::fs::remove_dir_all(dir()) {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        // Windows won't delete an exe that's running, e.g. mid-thumbnail.
        Err(e) => Err(e).context("couldn't remove ffmpeg (is it in use? try again in a moment)"),
    }
}
