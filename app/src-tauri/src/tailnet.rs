use anyhow::{anyhow, Context};
use serde::Serialize;
use serde_json::Value;
use std::path::PathBuf;

#[derive(Serialize, Clone, Debug, Default)]
#[serde(rename_all = "camelCase")]
pub struct Tailnet {
    /// tailscaled is up and logged in.
    pub running: bool,
    pub backend_state: String,
    pub login: String,
    pub user_id: i64,
    pub node: String,
    pub tailnet: String,
    pub peers: Vec<Peer>,
}

#[derive(Serialize, Clone, Debug)]
#[serde(rename_all = "camelCase")]
pub struct Peer {
    pub dns: String,
    pub host: String,
    pub online: bool,
    pub owner: String,
    pub tagged: bool,
}

fn tailscale_bin() -> PathBuf {
    #[cfg(windows)]
    {
        let p = PathBuf::from(r"C:\Program Files\Tailscale\tailscale.exe");
        if p.exists() {
            return p;
        }
    }
    PathBuf::from("tailscale")
}

pub fn status() -> anyhow::Result<Tailnet> {
    let out = crate::quiet_command(tailscale_bin())
        .args(["status", "--json"])
        .output()
        .context("Tailscale doesn't seem to be installed")?;
    // `status --json` exits non-zero while logged out but still prints JSON.
    let v: Value = serde_json::from_slice(&out.stdout).map_err(|_| {
        anyhow!("couldn't read Tailscale status: {}", String::from_utf8_lossy(&out.stderr).trim())
    })?;
    Ok(parse(&v))
}

fn trim_dot(s: &str) -> String {
    s.trim_end_matches('.').to_string()
}

pub fn parse(v: &Value) -> Tailnet {
    let users = &v["User"];
    let login_of = |id: &Value| -> String {
        users[id.as_i64().unwrap_or(0).to_string()]["LoginName"].as_str().unwrap_or("").to_string()
    };
    let me = &v["Self"];
    let state = v["BackendState"].as_str().unwrap_or("").to_string();
    let mut peers: Vec<Peer> = v["Peer"]
        .as_object()
        .map(|m| {
            m.values()
                .map(|p| Peer {
                    dns: trim_dot(p["DNSName"].as_str().unwrap_or("")),
                    host: p["HostName"].as_str().unwrap_or("").to_string(),
                    online: p["Online"].as_bool().unwrap_or(false),
                    owner: login_of(&p["UserID"]),
                    tagged: p["Tags"].as_array().is_some_and(|t| !t.is_empty()),
                })
                .filter(|p| !p.dns.is_empty())
                .collect()
        })
        .unwrap_or_default();
    peers.sort_by(|a, b| a.dns.cmp(&b.dns));
    Tailnet {
        running: state == "Running",
        backend_state: state,
        login: login_of(&me["UserID"]),
        user_id: me["UserID"].as_i64().unwrap_or(0),
        node: trim_dot(me["DNSName"].as_str().unwrap_or("")),
        tailnet: v["CurrentTailnet"]["Name"].as_str().unwrap_or("").to_string(),
        peers,
    }
}

/// This computer's own name, used for the default hosted node name.
pub fn machine_name() -> String {
    let raw = std::env::var("COMPUTERNAME")
        .or_else(|_| std::env::var("HOSTNAME"))
        .ok()
        .or_else(|| std::fs::read_to_string("/etc/hostname").ok())
        .unwrap_or_default();
    raw.trim()
        .to_ascii_lowercase()
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '-' })
        .collect::<String>()
        .trim_matches('-')
        .chars()
        .take(40)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_status() {
        let v: Value = serde_json::from_str(
            r#"{"BackendState":"Running","CurrentTailnet":{"Name":"me@x"},
            "Self":{"DNSName":"desk.t.ts.net.","UserID":1},
            "User":{"1":{"LoginName":"me@x"},"2":{"LoginName":"friend@y"}},
            "Peer":{"a":{"DNSName":"share.t.ts.net.","HostName":"share","Online":true,"UserID":2},
                    "b":{"DNSName":"srv.t.ts.net.","HostName":"srv","Online":false,"UserID":1,"Tags":["tag:x"]}}}"#,
        )
        .unwrap();
        let t = parse(&v);
        assert!(t.running);
        assert_eq!(t.login, "me@x");
        assert_eq!(t.node, "desk.t.ts.net");
        assert_eq!(t.peers.len(), 2);
        assert_eq!(t.peers[0].dns, "share.t.ts.net");
        assert_eq!(t.peers[0].owner, "friend@y");
        assert!(t.peers[1].tagged);
    }
}
