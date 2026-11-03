//! Runs the bundled tailshare-server so this computer can host shares.

use anyhow::{bail, Context};
use serde::Serialize;
use std::collections::VecDeque;
use std::io::{BufRead, BufReader};
use std::path::PathBuf;
use std::process::{Child, ChildStdin, Stdio};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Emitter};

const LOG_LINES: usize = 400;

#[derive(Serialize, Clone, Debug, Default, PartialEq)]
#[serde(rename_all = "camelCase")]
pub enum Phase {
    #[default]
    Stopped,
    Starting,
    /// Waiting for someone to approve the node in the browser.
    Login,
    /// Signed in, but the tailnet requires an admin to approve new machines.
    Approval,
    Running,
    Failed,
}

#[derive(Serialize, Clone, Debug, Default)]
#[serde(rename_all = "camelCase")]
pub struct HostState {
    pub phase: Phase,
    pub url: Option<String>,
    pub login_url: Option<String>,
    /// Why it's waiting, e.g. the node was removed in the admin console.
    pub notice: Option<String>,
    /// Hosting works but something needs attention (key expiring, offline).
    pub warning: Option<String>,
    pub error: Option<String>,
    pub hostname: String,
    pub state_dir: String,
}

#[derive(Default)]
struct Inner {
    state: HostState,
    log: VecDeque<String>,
    child: Option<Child>,
    // Held open on purpose: the server runs with -exit-on-eof, so if this app
    // dies the pipe closes and the server shuts down with it.
    stdin: Option<ChildStdin>,
    stopping: bool,
}

#[derive(Clone, Default)]
pub struct Host(Arc<Mutex<Inner>>);

pub fn state_dir() -> PathBuf {
    crate::data_dir().join("host")
}

/// The server re-reads this on every use, so edits apply while hosting.
pub fn write_roots(roots: &[String]) -> std::io::Result<()> {
    let dir = state_dir();
    std::fs::create_dir_all(&dir)?;
    let tmp = dir.join("roots.txt.tmp");
    std::fs::write(&tmp, roots.join("\n") + "\n")?;
    std::fs::rename(tmp, dir.join("roots.txt"))
}

pub fn server_bin() -> anyhow::Result<PathBuf> {
    // Tests run from target/debug/deps, away from the copied sidecar.
    #[cfg(debug_assertions)]
    if let Some(p) = std::env::var_os("TAILSHARE_SERVER_BIN") {
        return Ok(PathBuf::from(p));
    }
    let exe = std::env::current_exe()?;
    let dir = exe.parent().context("no exe dir")?;
    let name = if cfg!(windows) { "tailshare-server.exe" } else { "tailshare-server" };
    let p = dir.join(name);
    if !p.exists() {
        bail!("bundled server not found at {}", p.display());
    }
    Ok(p)
}

#[derive(serde::Deserialize)]
struct Event {
    event: String,
    url: Option<String>,
    reason: Option<String>,
    message: Option<String>,
}

impl Host {
    pub fn snapshot(&self) -> (HostState, Vec<String>) {
        let g = self.0.lock().unwrap();
        (g.state.clone(), g.log.iter().cloned().collect())
    }

    fn update(&self, app: &AppHandle, f: impl FnOnce(&mut Inner)) {
        let state = {
            let mut g = self.0.lock().unwrap();
            f(&mut g);
            g.state.clone()
        };
        let _ = app.emit("host-state", state);
    }

    fn push_log(&self, app: &AppHandle, line: String) {
        {
            let mut g = self.0.lock().unwrap();
            if g.log.len() >= LOG_LINES {
                g.log.pop_front();
            }
            g.log.push_back(line.clone());
        }
        let _ = app.emit("host-log", line);
    }

    pub fn start(
        &self,
        app: &AppHandle,
        hostname: &str,
        roots: &[String],
        admin: &str,
    ) -> anyhow::Result<()> {
        if !crate::settings::valid_hostname(hostname) {
            bail!("machine name may only use letters, digits and dashes");
        }
        let mut g = self.0.lock().unwrap();
        if g.child.is_some() {
            return Ok(());
        }
        let dir = state_dir();
        write_roots(roots)?;
        let mut cmd = crate::quiet_command(server_bin()?);
        cmd.arg("-state")
            .arg(&dir)
            .args(["-hostname", hostname, "-events", "-exit-on-eof"])
            // Empty -roots overrides the server's home-folder default; the
            // app's list is the only source.
            .args(["-roots", ""])
            .arg("-roots-file")
            .arg(dir.join("roots.txt"))
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        if !admin.is_empty() {
            cmd.args(["-bootstrap-admin", admin]);
        }
        let mut child = cmd.spawn().context("starting the server")?;
        let stdout = child.stdout.take().unwrap();
        let stderr = child.stderr.take().unwrap();
        g.stdin = child.stdin.take();
        g.child = Some(child);
        g.stopping = false;
        g.log.clear();
        g.state = HostState {
            phase: Phase::Starting,
            hostname: hostname.to_string(),
            state_dir: dir.to_string_lossy().into_owned(),
            ..Default::default()
        };
        let snapshot = g.state.clone();
        drop(g);
        let _ = app.emit("host-state", snapshot);

        let (h, a) = (self.clone(), app.clone());
        std::thread::spawn(move || {
            for line in BufReader::new(stdout).lines().map_while(Result::ok) {
                let Some(json) = line.strip_prefix("TAILSHARE-EVENT ") else { continue };
                let Ok(ev) = serde_json::from_str::<Event>(json) else { continue };
                h.update(&a, |g| match ev.event.as_str() {
                    "login" => {
                        g.state.phase = Phase::Login;
                        g.state.login_url = ev.url.filter(|u| !u.is_empty());
                        g.state.notice = ev.reason;
                    }
                    "approval" => {
                        g.state.phase = Phase::Approval;
                        g.state.login_url = None;
                        g.state.notice = ev.reason;
                    }
                    "serving" => {
                        g.state.phase = Phase::Running;
                        g.state.url = ev.url;
                        g.state.login_url = None;
                        g.state.notice = None;
                    }
                    "warning" => g.state.warning = ev.message.filter(|m| !m.is_empty()),
                    "error" => g.state.error = ev.message,
                    _ => {}
                });
            }
        });
        let (h, a) = (self.clone(), app.clone());
        std::thread::spawn(move || {
            for line in BufReader::new(stderr).lines().map_while(Result::ok) {
                h.push_log(&a, line);
            }
            h.reap(&a);
        });
        Ok(())
    }

    /// Called once the server's output closes, i.e. it has exited.
    fn reap(&self, app: &AppHandle) {
        let child = {
            let mut g = self.0.lock().unwrap();
            g.stdin = None;
            g.child.take()
        };
        let status = child.and_then(|mut c| c.wait().ok());
        self.update(app, |g| {
            let clean = g.stopping || status.is_some_and(|s| s.success());
            if clean {
                g.state.phase = Phase::Stopped;
            } else {
                g.state.phase = Phase::Failed;
                if g.state.error.is_none() {
                    g.state.error =
                        Some(g.log.back().cloned().unwrap_or_else(|| "server exited".into()));
                }
            }
            g.state.url = None;
            g.state.login_url = None;
            g.state.notice = None;
            g.state.warning = None;
        });
    }

    /// Closing stdin asks the server to shut down cleanly; it is killed if
    /// it hasn't within a few seconds.
    pub fn stop(&self) {
        let mut g = self.0.lock().unwrap();
        g.stopping = true;
        g.stdin = None;
        let Some(child) = g.child.as_mut() else { return };
        let pid = child.id();
        drop(g);
        let deadline = Instant::now() + Duration::from_secs(5);
        while Instant::now() < deadline {
            let g = self.0.lock().unwrap();
            if g.child.as_ref().is_none_or(|c| c.id() != pid) {
                return;
            }
            drop(g);
            std::thread::sleep(Duration::from_millis(100));
        }
        if let Some(c) = self.0.lock().unwrap().child.as_mut() {
            let _ = c.kill();
        }
    }
}
