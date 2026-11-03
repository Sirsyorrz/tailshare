use serde::{Deserialize, Serialize};
use std::fs;
use std::path::PathBuf;

#[derive(Serialize, Deserialize, Clone, Debug)]
#[serde(rename_all = "camelCase", default)]
pub struct Settings {
    /// Start hosting when the app opens.
    pub host_on_launch: bool,
    /// Host through an OS service that runs without the app.
    pub background: bool,
    /// Tailscale machine name for the hosted node.
    pub hostname: String,
    /// Folders shares may be created in.
    pub roots: Vec<String>,
    /// Servers added by hand, e.g. ones the scan can't see.
    pub servers: Vec<String>,
}

impl Default for Settings {
    fn default() -> Self {
        let machine = crate::tailnet::machine_name();
        Settings {
            host_on_launch: false,
            background: false,
            hostname: if machine.is_empty() {
                "tailshare".into()
            } else {
                format!("tailshare-{machine}")
            },
            roots: dirs::home_dir()
                .map(|h| vec![h.to_string_lossy().into_owned()])
                .unwrap_or_default(),
            servers: vec![],
        }
    }
}

fn path() -> PathBuf {
    crate::data_dir().join("settings.json")
}

pub fn load() -> Settings {
    fs::read(path()).ok().and_then(|b| serde_json::from_slice(&b).ok()).unwrap_or_default()
}

pub fn save(s: &Settings) -> anyhow::Result<()> {
    let p = path();
    fs::create_dir_all(p.parent().unwrap())?;
    let tmp = p.with_extension("json.tmp");
    fs::write(&tmp, serde_json::to_vec_pretty(s)?)?;
    fs::rename(tmp, p)?;
    Ok(())
}

/// Machine names become part of a DNS name, so keep only what Tailscale
/// would accept.
pub fn valid_hostname(h: &str) -> bool {
    !h.is_empty()
        && h.len() <= 63
        && h.chars().all(|c| c.is_ascii_alphanumeric() || c == '-')
        && !h.starts_with('-')
        && !h.ends_with('-')
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hostnames() {
        assert!(valid_hostname("tailshare-desktop"));
        assert!(!valid_hostname("-x"));
        assert!(!valid_hostname("a b"));
        assert!(!valid_hostname("a.b"));
        assert!(!valid_hostname(""));
    }
}
