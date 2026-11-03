use anyhow::{anyhow, bail};
use serde::{Deserialize, Serialize};
use std::sync::OnceLock;
use std::time::Duration;
use url::Url;

fn client() -> &'static reqwest::Client {
    static C: OnceLock<reqwest::Client> = OnceLock::new();
    C.get_or_init(|| {
        if rustls::crypto::CryptoProvider::get_default().is_none() {
            let _ = rustls::crypto::ring::default_provider().install_default();
        }
        reqwest::Client::builder()
            .connect_timeout(Duration::from_secs(4))
            // Tailnet traffic must never go through a system proxy.
            .no_proxy()
            .build()
            .expect("http client")
    })
}

/// Only tailnet names and loopback (dev mode) are ever contacted, so the
/// frontend can't be turned into a way to reach arbitrary sites.
pub fn check_base(base: &str) -> anyhow::Result<Url> {
    let u = Url::parse(base)?;
    let host = u.host_str().unwrap_or("");
    let ok = match u.scheme() {
        "https" => host.ends_with(".ts.net"),
        "http" => host == "127.0.0.1" || host == "localhost",
        _ => false,
    };
    if !ok || u.path() != "/" || u.query().is_some() {
        bail!("not a tailshare address: {base}");
    }
    Ok(u)
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Request {
    pub base: String,
    pub method: String,
    pub path: String,
    pub body: Option<String>,
    pub if_match: Option<String>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Response {
    pub status: u16,
    pub body: String,
    pub etag: Option<String>,
}

pub async fn request(r: Request) -> anyhow::Result<Response> {
    let base = check_base(&r.base)?;
    if !r.path.starts_with("/api/") {
        bail!("bad api path");
    }
    let url = base.join(&r.path)?;
    let method = reqwest::Method::from_bytes(r.method.as_bytes())?;
    let mut req = client()
        .request(method, url)
        .timeout(Duration::from_secs(30))
        // The server's CSRF check: every state-changing request carries it.
        .header("X-Tailshare", "1");
    if let Some(body) = r.body {
        req = req.header("Content-Type", "application/json").body(body);
    }
    if let Some(v) = r.if_match {
        req = req.header("If-Match", format!("\"{v}\""));
    }
    let resp = req.send().await.map_err(|e| anyhow!(describe(&e)))?;
    let status = resp.status().as_u16();
    let etag = resp
        .headers()
        .get("ETag")
        .and_then(|v| v.to_str().ok())
        .map(|s| s.trim_matches('"').to_string());
    let body = resp.text().await?;
    Ok(Response { status, body, etag })
}

fn describe(e: &reqwest::Error) -> String {
    if e.is_timeout() {
        "timed out".into()
    } else if e.is_connect() {
        "can't connect (offline, or blocked by the tailnet policy)".into()
    } else {
        e.to_string()
    }
}

#[derive(Serialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct Found {
    pub base: String,
    pub version: String,
}

#[derive(Deserialize)]
struct Hello {
    app: String,
    version: String,
}

async fn hello(base: String) -> Option<Found> {
    let url = Url::parse(&base).ok()?.join("/api/hello").ok()?;
    let resp = client().get(url).timeout(Duration::from_secs(5)).send().await.ok()?;
    let h: Hello = resp.json().await.ok()?;
    (h.app == "tailshare").then_some(Found { base, version: h.version })
}

/// Asks every candidate whether it is a tailshare server, all at once.
/// Other web servers on the tailnet just fail the check.
pub async fn discover(bases: Vec<String>) -> Vec<Found> {
    let checks = bases.into_iter().filter(|b| check_base(b).is_ok()).map(hello);
    futures::future::join_all(checks).await.into_iter().flatten().collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_tailnet_bases() {
        assert!(check_base("https://tailshare.example.ts.net/").is_ok());
        assert!(check_base("http://127.0.0.1:8765/").is_ok());
        assert!(check_base("https://example.com/").is_err());
        assert!(check_base("http://x.ts.net/").is_err());
        assert!(check_base("https://ts.net.evil.com/").is_err());
        assert!(check_base("https://a.ts.net/api/").is_err());
        assert!(check_base("file:///etc/passwd").is_err());
    }
}

/// Against a real server: TAILSHARE_TEST_BASE=https://x.ts.net/ cargo test -- --ignored
#[cfg(test)]
mod live {
    use super::*;

    #[test]
    #[ignore]
    fn talks_to_a_live_server() {
        let base = std::env::var("TAILSHARE_TEST_BASE").expect("TAILSHARE_TEST_BASE");
        let rt = tauri::async_runtime::block_on(async {
            let found = discover(vec![base.clone(), "https://example.com/".into()]).await;
            let me = request(Request {
                base: base.clone(),
                method: "GET".into(),
                path: "/api/me".into(),
                body: None,
                if_match: None,
            })
            .await
            .unwrap();
            let cfg = request(Request {
                base,
                method: "GET".into(),
                path: "/api/admin/config".into(),
                body: None,
                if_match: None,
            })
            .await
            .unwrap();
            (found.len(), me.status, me.body, cfg.status, cfg.etag)
        });
        println!("{rt:?}");
        assert_eq!(rt.0, 1);
        assert_eq!(rt.1, 200);
        assert_eq!(rt.3, 200);
        assert!(rt.4.is_some());
    }
}
