//! Mounts shares as drives with the bundled rclone. One `rclone rcd` runs
//! while any drive is in use; the app drives it over its loopback API with a
//! random password, and reads upload/download progress from it.

use crate::settings::DriveSpec;
use anyhow::{anyhow, bail, Context};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::{HashMap, VecDeque};
use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::process::{Child, Stdio};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

const LOG_LINES: usize = 300;
const NS: u64 = 1_000_000_000;

#[derive(Default)]
struct Inner {
    child: Option<Child>,
    port: u16,
    pass: String,
    started: Option<Instant>,
    log: VecDeque<String>,
    /// Latest error per file, from rclone's log, so the Transfers view can
    /// say why an upload keeps retrying.
    errors: HashMap<String, String>,
}

#[derive(Clone, Default)]
pub struct Drives(Arc<Mutex<Inner>>);

pub fn rclone_bin() -> anyhow::Result<PathBuf> {
    #[cfg(debug_assertions)]
    if let Some(p) = std::env::var_os("TAILSHARE_RCLONE_BIN") {
        return Ok(PathBuf::from(p));
    }
    let exe = std::env::current_exe()?;
    let dir = exe.parent().context("no exe dir")?;
    let p = dir.join(if cfg!(windows) { "rclone.exe" } else { "rclone" });
    if !p.exists() {
        bail!("bundled rclone not found at {}", p.display());
    }
    Ok(p)
}

fn drives_dir() -> PathBuf {
    crate::data_dir().join("drives")
}

pub fn cache_dir() -> PathBuf {
    drives_dir().join("cache")
}

/// rclone's cache is keyed by remote name, so this must stay stable across
/// app versions for unfinished uploads to resume. FNV-1a, not Hash, for that.
pub fn remote_name(base: &str, share: &str) -> String {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for b in base.bytes().chain([0]).chain(share.bytes()) {
        h ^= b as u64;
        h = h.wrapping_mul(0x0100_0000_01b3);
    }
    format!("ts-{h:016x}")
}

pub fn dav_url(base: &str, share: &str) -> anyhow::Result<String> {
    let mut u = crate::api::check_base(base)?;
    u.path_segments_mut()
        .map_err(|_| anyhow!("bad base"))?
        .pop_if_empty()
        .extend(["dav", share, ""]);
    Ok(u.to_string())
}

/// Whether this computer can mount at all, and if not, why.
pub fn support() -> Option<String> {
    #[cfg(windows)]
    {
        let found = ["ProgramFiles(x86)", "ProgramFiles"].iter().any(|v| {
            std::env::var_os(v)
                .map(|p| Path::new(&p).join("WinFsp").join("bin").join("winfsp-x64.dll").exists())
                .unwrap_or(false)
        });
        if !found {
            return Some("winfsp".into());
        }
    }
    #[cfg(target_os = "linux")]
    {
        if !Path::new("/dev/fuse").exists() || fusermount().is_none() {
            return Some("fuse".into());
        }
    }
    None
}

#[cfg(target_os = "linux")]
fn fusermount() -> Option<PathBuf> {
    let path = std::env::var_os("PATH")?;
    std::env::split_paths(&path)
        .flat_map(|d| [d.join("fusermount3"), d.join("fusermount")])
        .find(|p| p.exists())
}

/// A default place to mount a share: the first free letter from T: on
/// Windows, ~/tailshare/<share> elsewhere.
pub fn suggest_target(share: &str, taken: &[String]) -> String {
    #[cfg(windows)]
    {
        let _ = share;
        let order = ('T'..='Z').chain(('D'..='S').rev());
        for l in order {
            let t = format!("{l}:");
            if !taken.contains(&t) && !Path::new(&format!("{l}:\\")).exists() {
                return t;
            }
        }
        String::new()
    }
    #[cfg(not(windows))]
    {
        let home = dirs::home_dir().unwrap_or_else(|| PathBuf::from("."));
        let safe: String = share.chars().map(|c| if c == '/' { '_' } else { c }).collect();
        let mut n = 1;
        loop {
            let name = if n == 1 { safe.clone() } else { format!("{safe} ({n})") };
            let p = home.join("tailshare").join(name).to_string_lossy().into_owned();
            if !taken.contains(&p) && is_empty_dir(Path::new(&p)) {
                return p;
            }
            n += 1;
        }
    }
}

#[cfg(not(windows))]
fn is_empty_dir(p: &Path) -> bool {
    match std::fs::read_dir(p) {
        Ok(mut d) => d.next().is_none(),
        Err(e) => e.kind() == std::io::ErrorKind::NotFound,
    }
}

#[derive(Deserialize)]
struct LogLine {
    level: Option<String>,
    msg: Option<String>,
    object: Option<String>,
}

fn random_hex(n: usize) -> anyhow::Result<String> {
    let mut b = vec![0u8; n];
    getrandom::fill(&mut b).map_err(|e| anyhow!("random: {e}"))?;
    Ok(b.iter().map(|x| format!("{x:02x}")).collect())
}

fn pid_file() -> PathBuf {
    drives_dir().join("rclone.pid")
}

/// A crashed app leaves its rclone running with the drives still mounted;
/// the next start takes over by ending it first.
fn kill_orphan() {
    let Ok(s) = std::fs::read_to_string(pid_file()) else { return };
    let Ok(pid) = s.trim().parse::<u32>() else { return };
    #[cfg(target_os = "linux")]
    {
        let is_rclone = std::fs::read_link(format!("/proc/{pid}/exe"))
            .map(|p| p.file_name().is_some_and(|n| n.to_string_lossy().starts_with("rclone")))
            .unwrap_or(false);
        if is_rclone {
            let _ = std::process::Command::new("kill").arg(pid.to_string()).status();
            std::thread::sleep(Duration::from_millis(800));
        }
    }
    #[cfg(windows)]
    {
        let out = crate::quiet_command("tasklist")
            .args(["/FI", &format!("PID eq {pid}"), "/FO", "CSV", "/NH"])
            .output();
        if out.is_ok_and(|o| String::from_utf8_lossy(&o.stdout).to_lowercase().contains("rclone")) {
            let _ =
                crate::quiet_command("taskkill").args(["/PID", &pid.to_string(), "/F"]).status();
        }
    }
    let _ = std::fs::remove_file(pid_file());
}

/// After a crash the old FUSE mount point is left dead ("transport endpoint
/// is not connected") and can't be mounted over until it's detached.
#[cfg(target_os = "linux")]
fn clear_stale_mount(p: &Path) {
    if let Err(e) = std::fs::read_dir(p) {
        if e.raw_os_error() == Some(107) {
            if let Some(f) = fusermount() {
                let _ = std::process::Command::new(f).arg("-uz").arg(p).status();
            }
        }
    }
}

#[derive(Serialize, Clone, Debug, Default)]
#[serde(rename_all = "camelCase")]
pub struct Queued {
    pub name: String,
    pub size: i64,
    pub uploading: bool,
    /// Seconds until the upload starts (rclone waits for writes to settle).
    pub starts_in: f64,
    pub tries: i64,
    pub error: Option<String>,
}

#[derive(Serialize, Clone, Debug, Default)]
#[serde(rename_all = "camelCase")]
pub struct Mounted {
    pub remote: String,
    pub target: String,
    pub cache_bytes: i64,
    pub uploads_queued: i64,
    pub uploads_in_progress: i64,
    pub errored_files: i64,
    pub queue: Vec<Queued>,
}

#[derive(Serialize, Clone, Debug, Default)]
#[serde(rename_all = "camelCase")]
pub struct Transfer {
    pub name: String,
    pub remote: String,
    pub upload: bool,
    pub size: i64,
    pub bytes: i64,
    pub speed: f64,
    pub eta: Option<i64>,
}

#[derive(Serialize, Clone, Debug, Default)]
#[serde(rename_all = "camelCase")]
pub struct DrivesState {
    pub running: bool,
    /// "winfsp" or "fuse" when this computer can't mount yet.
    pub missing: Option<String>,
    pub mounts: Vec<Mounted>,
    pub transfers: Vec<Transfer>,
    pub log: Vec<String>,
    /// rclone remote name to share name, for labelling transfers.
    pub names: HashMap<String, String>,
}

impl Drives {
    fn alive(g: &mut Inner) -> bool {
        g.child.as_mut().is_some_and(|c| matches!(c.try_wait(), Ok(None)))
    }

    /// Starts rclone if it isn't running and returns how to reach it.
    fn ensure(&self) -> anyhow::Result<(u16, String, Instant)> {
        let mut g = self.0.lock().unwrap();
        if Self::alive(&mut g) {
            return Ok((g.port, g.pass.clone(), g.started.unwrap()));
        }
        kill_orphan();
        std::fs::create_dir_all(drives_dir())?;
        let port = std::net::TcpListener::bind("127.0.0.1:0")?.local_addr()?.port();
        let pass = random_hex(24)?;
        let mut cmd = crate::quiet_command(rclone_bin()?);
        cmd.arg("rcd")
            .args(["--rc-addr", &format!("127.0.0.1:{port}")])
            .arg("--config")
            .arg(drives_dir().join("rclone.conf"))
            .arg("--cache-dir")
            .arg(cache_dir())
            .args(["--use-json-log", "--log-level", "NOTICE"])
            // Through the environment, not argv, so other local users can't
            // read the password from the process list.
            .env("RCLONE_RC_USER", "tailshare")
            .env("RCLONE_RC_PASS", &pass)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::piped());
        let mut child = cmd.spawn().context("starting rclone")?;
        let _ = std::fs::write(pid_file(), child.id().to_string());
        let stderr = child.stderr.take().unwrap();
        g.child = Some(child);
        g.port = port;
        g.pass = pass.clone();
        g.errors.clear();
        let started = Instant::now();
        g.started = Some(started);
        drop(g);

        let me = self.clone();
        std::thread::spawn(move || {
            for line in BufReader::new(stderr).lines().map_while(Result::ok) {
                let mut g = me.0.lock().unwrap();
                let text = match serde_json::from_str::<LogLine>(&line) {
                    Ok(l) => {
                        let msg = l.msg.unwrap_or_default();
                        if l.level.as_deref() == Some("error") {
                            if let Some(o) = &l.object {
                                g.errors.insert(o.clone(), clean_error(&msg));
                            }
                        }
                        match l.object {
                            Some(o) => format!("{}: {o}: {msg}", l.level.unwrap_or_default()),
                            None => format!("{}: {msg}", l.level.unwrap_or_default()),
                        }
                    }
                    Err(_) => line,
                };
                if g.log.len() >= LOG_LINES {
                    g.log.pop_front();
                }
                g.log.push_back(text);
            }
        });
        Ok((port, pass, started))
    }

    fn endpoint(&self) -> Option<(u16, String)> {
        let mut g = self.0.lock().unwrap();
        Self::alive(&mut g).then(|| (g.port, g.pass.clone()))
    }

    async fn call(port: u16, pass: &str, path: &str, body: Value) -> anyhow::Result<Value> {
        let r = crate::api::client()
            .post(format!("http://127.0.0.1:{port}/{path}"))
            .basic_auth("tailshare", Some(pass))
            .timeout(Duration::from_secs(60))
            .json(&body)
            .send()
            .await?;
        let ok = r.status().is_success();
        let v: Value = r.json().await.unwrap_or(Value::Null);
        if !ok {
            bail!("{}", clean_error(v["error"].as_str().unwrap_or("rclone error")));
        }
        Ok(v)
    }

    /// Calls rclone, starting it first if needed.
    async fn rc(&self, path: &str, body: Value) -> anyhow::Result<Value> {
        let (port, pass, started) = tauri::async_runtime::spawn_blocking({
            let me = self.clone();
            move || me.ensure()
        })
        .await??;
        loop {
            match Self::call(port, &pass, path, body.clone()).await {
                Err(e) if started.elapsed() < Duration::from_secs(10) && is_connect(&e) => {
                    tokio::time::sleep(Duration::from_millis(100)).await;
                }
                r => return r,
            }
        }
    }

    pub async fn mount(&self, spec: &DriveSpec, cache_gb: u32) -> anyhow::Result<()> {
        if let Some(m) = support() {
            bail!(if m == "winfsp" { "WinFsp is not installed" } else { "FUSE is not available" });
        }
        let remote = remote_name(&spec.base, &spec.share);
        let url = dav_url(&spec.base, &spec.share)?;
        prepare_target(&spec.target)?;
        let _ = self.rc("config/delete", json!({ "name": remote })).await;
        self.rc(
            "config/create",
            json!({
                "name": remote,
                "type": "webdav",
                "parameters": { "url": url, "vendor": "rclone", "headers": "X-Tailshare,1" },
                "opt": { "nonInteractive": true, "obscure": false },
            }),
        )
        .await?;
        self.rc(
            "mount/mount",
            json!({
                "fs": format!("{remote}:"),
                "mountPoint": spec.target,
                "vfsOpt": {
                    // Full caching is what makes a network share behave like
                    // a disk: seeking, partial writes, apps that rewrite
                    // files in place.
                    "CacheMode": "full",
                    "CacheMaxSize": (cache_gb.max(1) as u64) * 1_000_000_000,
                    "CacheMaxAge": 24 * 3600 * NS,
                    "WriteBack": 5 * NS,
                    // WebDAV can't push changes, so this is how long other
                    // people's changes take to show up.
                    "DirCacheTime": 20 * NS,
                    "PollInterval": 0,
                    "ReadOnly": spec.read_only,
                },
                "mountOpt": {
                    "VolumeName": spec.share,
                    "NetworkMode": cfg!(windows),
                },
            }),
        )
        .await?;
        Ok(())
    }

    pub async fn unmount(&self, target: &str) -> anyhow::Result<()> {
        let Some((port, pass)) = self.endpoint() else { return Ok(()) };
        Self::call(port, &pass, "mount/unmount", json!({ "mountPoint": target })).await?;
        Ok(())
    }

    pub async fn state(&self) -> DrivesState {
        let mut st = DrivesState { missing: support(), ..Default::default() };
        let Some((port, pass)) = self.endpoint() else { return st };
        st.running = true;
        let (log, errors) = {
            let g = self.0.lock().unwrap();
            (g.log.iter().cloned().collect::<Vec<_>>(), g.errors.clone())
        };
        st.log = log;
        if let Ok(v) = Self::call(port, &pass, "mount/listmounts", json!({})).await {
            for m in v["mountPoints"].as_array().into_iter().flatten() {
                let fs = m["Fs"].as_str().unwrap_or("").to_string();
                let mut mo = Mounted {
                    remote: fs.trim_end_matches(':').to_string(),
                    target: m["MountPoint"].as_str().unwrap_or("").to_string(),
                    ..Default::default()
                };
                if let Ok(s) = Self::call(port, &pass, "vfs/stats", json!({ "fs": fs })).await {
                    let d = &s["diskCache"];
                    mo.cache_bytes = d["bytesUsed"].as_i64().unwrap_or(0);
                    mo.uploads_queued = d["uploadsQueued"].as_i64().unwrap_or(0);
                    mo.uploads_in_progress = d["uploadsInProgress"].as_i64().unwrap_or(0);
                    mo.errored_files = d["erroredFiles"].as_i64().unwrap_or(0);
                }
                if let Ok(q) = Self::call(port, &pass, "vfs/queue", json!({ "fs": fs })).await {
                    for it in q["queue"].as_array().into_iter().flatten() {
                        let name = it["name"].as_str().unwrap_or("").to_string();
                        mo.queue.push(Queued {
                            error: errors.get(&name).cloned(),
                            size: it["size"].as_i64().unwrap_or(0),
                            uploading: it["uploading"].as_bool().unwrap_or(false),
                            starts_in: it["expiry"].as_f64().unwrap_or(0.0).max(0.0),
                            tries: it["tries"].as_i64().unwrap_or(0),
                            name,
                        });
                    }
                }
                st.mounts.push(mo);
            }
        }
        if let Ok(s) = Self::call(port, &pass, "core/stats", json!({})).await {
            for t in s["transferring"].as_array().into_iter().flatten() {
                // Uploads go into one of our remotes; reads come out of one.
                let dst = t["dstFs"].as_str().unwrap_or("");
                let upload = dst.starts_with("ts-");
                let remote = if upload { dst } else { t["srcFs"].as_str().unwrap_or("") };
                st.transfers.push(Transfer {
                    name: t["name"].as_str().unwrap_or("").to_string(),
                    remote: remote.trim_end_matches(':').to_string(),
                    upload,
                    size: t["size"].as_i64().unwrap_or(0),
                    bytes: t["bytes"].as_i64().unwrap_or(0),
                    speed: t["speedAvg"].as_f64().or(t["speed"].as_f64()).unwrap_or(0.0),
                    eta: t["eta"].as_i64(),
                });
            }
        }
        st
    }

    /// Unmounts everything and stops rclone. Unfinished uploads stay in the
    /// cache and continue the next time their drive is mounted.
    pub fn shutdown(&self) {
        let Some((port, pass)) = self.endpoint() else { return };
        tauri::async_runtime::block_on(async {
            let _ = tokio::time::timeout(
                Duration::from_secs(10),
                Self::call(port, &pass, "mount/unmountall", json!({})),
            )
            .await;
            let _ = Self::call(port, &pass, "core/quit", json!({})).await;
        });
        let deadline = Instant::now() + Duration::from_secs(3);
        loop {
            let mut g = self.0.lock().unwrap();
            let Some(c) = g.child.as_mut() else { break };
            if !matches!(c.try_wait(), Ok(None)) || Instant::now() > deadline {
                let _ = c.kill();
                let _ = c.wait();
                g.child = None;
                break;
            }
            drop(g);
            std::thread::sleep(Duration::from_millis(100));
        }
        let _ = std::fs::remove_file(pid_file());
    }
}

fn prepare_target(target: &str) -> anyhow::Result<()> {
    #[cfg(windows)]
    {
        let t = target.as_bytes();
        if !(t.len() == 2 && t[0].is_ascii_alphabetic() && t[1] == b':') {
            bail!("pick a drive letter");
        }
        if Path::new(&format!("{target}\\")).exists() {
            bail!("drive {target} is already in use");
        }
    }
    #[cfg(not(windows))]
    {
        let p = Path::new(target);
        if !p.is_absolute() {
            bail!("pick a folder to mount into");
        }
        #[cfg(target_os = "linux")]
        clear_stale_mount(p);
        std::fs::create_dir_all(p).with_context(|| format!("creating {target}"))?;
        if !is_empty_dir(p) {
            bail!("{target} isn't empty; pick an empty folder");
        }
    }
    Ok(())
}

/// rclone errors nest the server's JSON reply; pull out the readable part.
fn clean_error(msg: &str) -> String {
    if let Some(i) = msg.find("{\"error\":\"") {
        let rest = &msg[i + 10..];
        if let Some(j) = rest.find('"') {
            return rest[..j].to_string();
        }
    }
    msg.to_string()
}

fn is_connect(e: &anyhow::Error) -> bool {
    e.downcast_ref::<reqwest::Error>().is_some_and(|r| r.is_connect())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_and_urls() {
        assert_eq!(remote_name("https://a.ts.net/", "x"), remote_name("https://a.ts.net/", "x"));
        assert_ne!(remote_name("https://a.ts.net/", "x"), remote_name("https://a.ts.net/x", ""));
        assert_eq!(
            dav_url("https://srv.example.ts.net/", "my share/#1").unwrap(),
            "https://srv.example.ts.net/dav/my%20share%2F%231/"
        );
        assert!(dav_url("https://evil.com/", "x").is_err());
        assert_eq!(
            clean_error(
                r#"failed: unchunked simple update failed: {"error":"not allowed to upload"}: 403 Forbidden"#
            ),
            "not allowed to upload"
        );
    }
}

/// Against a dev server with a writable "media" share:
/// TAILSHARE_RCLONE_BIN=... TAILSHARE_TEST_BASE=http://127.0.0.1:8766/ cargo test drives_live -- --ignored
#[cfg(test)]
mod live {
    use super::*;

    #[test]
    #[ignore]
    fn drives_live() {
        let base = std::env::var("TAILSHARE_TEST_BASE").expect("TAILSHARE_TEST_BASE");
        let dir = std::env::temp_dir().join(format!("ts-drive-{}", std::process::id()));
        let target = dir.to_string_lossy().into_owned();
        let d = Drives::default();
        let spec =
            DriveSpec { base, share: "media".into(), target: target.clone(), ..Default::default() };
        tauri::async_runtime::block_on(async {
            d.mount(&spec, 1).await.expect("mount");
            assert!(std::fs::read_dir(&dir).unwrap().count() > 0, "mount shows the share");
            std::fs::write(dir.join("drive-test.bin"), vec![7u8; 3_000_000]).unwrap();
            let st = d.state().await;
            assert_eq!(st.mounts.len(), 1);
            assert_eq!(st.mounts[0].queue.len(), 1, "write is queued for upload: {st:?}");
            let deadline = Instant::now() + Duration::from_secs(30);
            loop {
                let st = d.state().await;
                if st.mounts[0].queue.is_empty() && st.mounts[0].uploads_in_progress == 0 {
                    break;
                }
                assert!(Instant::now() < deadline, "upload never finished: {st:?}");
                tokio::time::sleep(Duration::from_millis(300)).await;
            }
            std::fs::remove_file(dir.join("drive-test.bin")).unwrap();
            d.unmount(&target).await.expect("unmount");
            assert_eq!(std::fs::read_dir(&dir).unwrap().count(), 0, "unmounted");
        });
        d.shutdown();
        assert!(d.endpoint().is_none());
        let _ = std::fs::remove_dir(&dir);
    }
}
