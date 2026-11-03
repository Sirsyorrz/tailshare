//! Background hosting: the server runs as an OS-managed process (systemd
//! user service on Linux, a logon scheduled task on Windows) so it keeps
//! going with the app closed. The app talks to it only through files in the
//! state dir: status.json, server.log and roots.txt.

use crate::host::{state_dir, HostState, Phase};
use anyhow::{bail, Context};
use serde::Deserialize;
use std::fs;
use std::path::PathBuf;
use std::time::{Duration, SystemTime};

#[cfg(target_os = "linux")]
const UNIT: &str = "tailshare.service";
#[cfg(windows)]
const TASK: &str = "tailshare";

pub fn supported() -> bool {
    cfg!(any(target_os = "linux", windows))
}

fn exe_name() -> &'static str {
    if cfg!(windows) {
        "tailshare-server.exe"
    } else {
        "tailshare-server"
    }
}

/// A stable copy of the server. The bundled one can't be used directly: an
/// AppImage mounts at a new random path on every run.
pub fn installed_bin() -> PathBuf {
    crate::data_dir().join("bin").join(exe_name())
}

pub fn status_file() -> PathBuf {
    state_dir().join("status.json")
}

pub fn log_file() -> PathBuf {
    state_dir().join("server.log")
}

pub struct Args {
    pub hostname: String,
    pub admin: String,
}

fn server_args(a: &Args) -> Vec<String> {
    let dir = state_dir();
    let mut v = vec![
        "-state".into(),
        dir.to_string_lossy().into_owned(),
        "-hostname".into(),
        a.hostname.clone(),
        "-roots-file".into(),
        dir.join("roots.txt").to_string_lossy().into_owned(),
        "-status-file".into(),
        status_file().to_string_lossy().into_owned(),
        "-log-file".into(),
        log_file().to_string_lossy().into_owned(),
    ];
    if !a.admin.is_empty() {
        v.push("-bootstrap-admin".into());
        v.push(a.admin.clone());
    }
    v
}

/// Copies the bundled server into place if it differs. Returns whether it
/// changed, in which case a running service needs a restart.
pub fn sync_binary() -> anyhow::Result<bool> {
    let bundled = crate::host::server_bin()?;
    let dest = installed_bin();
    let new = fs::read(&bundled)?;
    if fs::read(&dest).ok().as_deref() == Some(&new[..]) {
        return Ok(false);
    }
    fs::create_dir_all(dest.parent().unwrap())?;
    // Write beside and rename: the old binary may still be running (Linux
    // allows replacing it; Windows needs the service stopped first).
    let tmp = dest.with_extension("new");
    fs::write(&tmp, &new)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&tmp, fs::Permissions::from_mode(0o755))?;
    }
    fs::rename(&tmp, &dest).context("replacing the background server (is it still running?)")?;
    Ok(true)
}

fn run(program: &str, args: &[&str]) -> anyhow::Result<String> {
    let out = crate::quiet_command(program)
        .args(args)
        .output()
        .with_context(|| format!("running {program}"))?;
    if !out.status.success() {
        let msg = String::from_utf8_lossy(&out.stderr);
        let msg = if msg.trim().is_empty() { String::from_utf8_lossy(&out.stdout) } else { msg };
        bail!("{program} {}: {}", args.join(" "), msg.trim());
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

#[cfg(target_os = "linux")]
mod imp {
    use super::*;

    fn unit_path() -> PathBuf {
        dirs::config_dir().unwrap_or_default().join("systemd/user").join(UNIT)
    }

    /// systemd unquotes double-quoted words, with C-style escapes inside.
    fn quote(s: &str) -> String {
        format!("\"{}\"", s.replace('\\', "\\\\").replace('"', "\\\"").replace('%', "%%"))
    }

    pub fn install(a: &Args) -> anyhow::Result<()> {
        let exec = std::iter::once(installed_bin().to_string_lossy().into_owned())
            .chain(server_args(a))
            .map(|s| quote(&s))
            .collect::<Vec<_>>()
            .join(" ");
        let unit = format!(
            "# Written by the tailshare app; changes are overwritten.\n\
             [Unit]\n\
             Description=tailshare file sharing over Tailscale\n\
             After=network-online.target\n\
             Wants=network-online.target\n\n\
             [Service]\n\
             ExecStart={exec}\n\
             Restart=on-failure\n\
             RestartSec=10\n\n\
             [Install]\n\
             WantedBy=default.target\n"
        );
        let p = unit_path();
        fs::create_dir_all(p.parent().unwrap())?;
        fs::write(&p, unit)?;
        run("systemctl", &["--user", "daemon-reload"])?;
        run("systemctl", &["--user", "enable", UNIT])?;
        Ok(())
    }

    /// Lingering keeps user services running after logout and starts them
    /// at boot. Not fatal if refused: it then runs while logged in.
    pub fn enable_linger() -> Option<String> {
        let user = std::env::var("USER").unwrap_or_default();
        run("loginctl", &["enable-linger", &user])
            .err()
            .map(|e| format!("Runs only while you're logged in ({e})."))
    }

    pub fn start() -> anyhow::Result<()> {
        run("systemctl", &["--user", "restart", UNIT]).map(|_| ())
    }

    /// Also disables it, so a stopped server stays stopped across reboots.
    pub fn stop() -> anyhow::Result<()> {
        run("systemctl", &["--user", "disable", "--now", UNIT]).map(|_| ())
    }

    pub fn uninstall() -> anyhow::Result<()> {
        let _ = run("systemctl", &["--user", "disable", "--now", UNIT]);
        let _ = fs::remove_file(unit_path());
        let _ = run("systemctl", &["--user", "daemon-reload"]);
        Ok(())
    }

    pub fn active() -> bool {
        run("systemctl", &["--user", "is-active", UNIT]).is_ok_and(|s| s.trim() == "active")
    }
}

#[cfg_attr(not(windows), allow(dead_code))]
fn xml_escape(s: &str) -> String {
    s.replace('&', "&amp;").replace('<', "&lt;").replace('>', "&gt;").replace('"', "&quot;")
}

/// Windows command-line quoting, as parsed by CommandLineToArgvW.
#[cfg_attr(not(windows), allow(dead_code))]
fn win_quote(s: &str) -> String {
    if !s.is_empty() && !s.contains([' ', '\t', '"']) {
        return s.to_string();
    }
    let mut out = String::from("\"");
    let mut backslashes = 0;
    for c in s.chars() {
        match c {
            '\\' => backslashes += 1,
            '"' => {
                out.push_str(&"\\".repeat(backslashes * 2 + 1));
                out.push('"');
                backslashes = 0;
            }
            _ => {
                out.push_str(&"\\".repeat(backslashes));
                out.push(c);
                backslashes = 0;
            }
        }
    }
    out.push_str(&"\\".repeat(backslashes * 2));
    out.push('"');
    out
}

#[cfg(windows)]
mod imp {
    use super::*;

    // The defaults would stop the server on battery and after 72 hours.
    pub fn install(a: &Args) -> anyhow::Result<()> {
        let user = format!(
            "{}\\{}",
            std::env::var("USERDOMAIN").unwrap_or_default(),
            std::env::var("USERNAME").unwrap_or_default()
        );
        let args = server_args(a).iter().map(|s| win_quote(s)).collect::<Vec<_>>().join(" ");
        let xml = format!(
            r#"<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>tailshare file sharing over Tailscale. Managed by the tailshare app.</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>{user}</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>{user}</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
    <StartWhenAvailable>true</StartWhenAvailable>
    <Hidden>true</Hidden>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author"><Exec><Command>{cmd}</Command><Arguments>{args}</Arguments></Exec></Actions>
</Task>
"#,
            user = xml_escape(&user),
            cmd = xml_escape(&installed_bin().to_string_lossy()),
            args = xml_escape(&args),
        );
        // schtasks wants the XML file as UTF-16 with a BOM.
        let mut bytes = vec![0xFF, 0xFE];
        for u in xml.encode_utf16() {
            bytes.extend_from_slice(&u.to_le_bytes());
        }
        let p = state_dir().join("task.xml");
        fs::create_dir_all(state_dir())?;
        fs::write(&p, bytes)?;
        run("schtasks", &["/Create", "/TN", TASK, "/XML", &p.to_string_lossy(), "/F"])?;
        let _ = fs::remove_file(p);
        Ok(())
    }

    pub fn enable_linger() -> Option<String> {
        None
    }

    pub fn start() -> anyhow::Result<()> {
        let _ = run("schtasks", &["/End", "/TN", TASK]);
        run("schtasks", &["/Run", "/TN", TASK]).map(|_| ())
    }

    /// Also disables the logon trigger, so a stopped server stays stopped.
    pub fn stop() -> anyhow::Result<()> {
        let _ = run("schtasks", &["/End", "/TN", TASK]);
        run("schtasks", &["/Change", "/TN", TASK, "/DISABLE"]).map(|_| ())
    }

    pub fn uninstall() -> anyhow::Result<()> {
        let _ = stop();
        let _ = run("schtasks", &["/Delete", "/TN", TASK, "/F"]);
        Ok(())
    }

    pub fn active() -> bool {
        run("schtasks", &["/Query", "/TN", TASK, "/FO", "CSV", "/NH"])
            .is_ok_and(|s| s.contains("Running"))
    }
}

#[cfg(not(any(target_os = "linux", windows)))]
mod imp {
    use super::*;
    pub fn install(_: &Args) -> anyhow::Result<()> {
        bail!("background hosting isn't supported on this system")
    }
    pub fn enable_linger() -> Option<String> {
        None
    }
    pub fn start() -> anyhow::Result<()> {
        bail!("unsupported")
    }
    pub fn stop() -> anyhow::Result<()> {
        Ok(())
    }
    pub fn uninstall() -> anyhow::Result<()> {
        Ok(())
    }
    pub fn active() -> bool {
        false
    }
}

pub use imp::{active, enable_linger, uninstall};

/// Rewrites the service definition from current settings, then (re)starts.
/// Returns a note for the user, e.g. that it won't survive logout.
pub fn start(a: &Args) -> anyhow::Result<Option<String>> {
    if active() {
        imp::stop()?;
    }
    sync_binary()?;
    imp::install(a)?;
    let note = enable_linger();
    imp::start()?;
    Ok(note)
}

pub fn stop() -> anyhow::Result<()> {
    imp::stop()
}

/// After an app update: swap in the new server and restart it if running.
/// A stopped one is left stopped (installing would re-enable it); start()
/// rewrites the definition anyway.
pub fn refresh_binary(a: &Args) -> anyhow::Result<()> {
    if fs::read(installed_bin()).ok() == fs::read(crate::host::server_bin()?).ok() {
        return Ok(());
    }
    if !active() {
        sync_binary()?;
        return Ok(());
    }
    imp::stop()?;
    sync_binary()?;
    imp::install(a)?;
    imp::start()
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct StatusDoc {
    phase: String,
    url: Option<String>,
    login_url: Option<String>,
    error: Option<String>,
    hostname: String,
    updated: String,
}

/// The server rewrites status.json every few seconds; a stale file means it
/// died without saying so.
pub fn status() -> HostState {
    let mut st =
        HostState { state_dir: state_dir().to_string_lossy().into_owned(), ..Default::default() };
    let Ok(b) = fs::read(status_file()) else { return st };
    let Ok(doc) = serde_json::from_slice::<StatusDoc>(&b) else { return st };
    let fresh = fs::metadata(status_file())
        .and_then(|m| m.modified())
        .ok()
        .and_then(|t| SystemTime::now().duration_since(t).ok())
        .is_some_and(|age| age < Duration::from_secs(20));
    let _ = doc.updated;
    st.hostname = doc.hostname;
    st.error = doc.error.filter(|e| !e.is_empty());
    st.phase = match (doc.phase.as_str(), fresh) {
        (_, false) if doc.phase != "failed" => Phase::Stopped,
        ("starting", _) => Phase::Starting,
        ("login", _) => Phase::Login,
        ("running", _) => Phase::Running,
        ("failed", _) => Phase::Failed,
        _ => Phase::Stopped,
    };
    if st.phase == Phase::Running {
        st.url = doc.url;
    }
    if st.phase == Phase::Login {
        st.login_url = doc.login_url;
    }
    st
}

pub fn log_tail(lines: usize) -> Vec<String> {
    let s = fs::read_to_string(log_file()).unwrap_or_default();
    let all: Vec<&str> = s.lines().collect();
    all[all.len().saturating_sub(lines)..].iter().map(|l| l.to_string()).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn windows_quoting() {
        assert_eq!(win_quote("plain"), "plain");
        assert_eq!(win_quote(""), "\"\"");
        assert_eq!(win_quote(r"C:\Users\A B\x"), r#""C:\Users\A B\x""#);
        assert_eq!(win_quote(r"C:\dir with space\"), r#""C:\dir with space\\""#);
        assert_eq!(win_quote(r#"a"b"#), r#""a\"b""#);
        assert_eq!(xml_escape(r#"<a & "b">"#), "&lt;a &amp; &quot;b&quot;&gt;");
    }

    /// Installs, starts, checks and removes the real background service.
    /// TAILSHARE_SERVER_BIN=target/debug/tailshare-server cargo test -- --ignored background
    #[test]
    #[ignore]
    fn background_round_trip() {
        let a = Args { hostname: "tailshare-desktop".into(), admin: String::new() };
        let note = start(&a).unwrap();
        println!("note: {note:?}");
        let mut st = status();
        for _ in 0..60 {
            if st.phase == Phase::Running {
                break;
            }
            std::thread::sleep(Duration::from_secs(1));
            st = status();
        }
        println!("running: {st:?} active={}", active());
        assert_eq!(st.phase, Phase::Running);
        assert!(active());
        stop().unwrap();
        std::thread::sleep(Duration::from_secs(2));
        let st = status();
        println!("stopped: {:?} active={}", st.phase, active());
        assert_eq!(st.phase, Phase::Stopped);
        assert!(!active());
        uninstall().unwrap();
    }
}
