#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use tailshare::{api, host, settings, tailnet};
use tauri::{AppHandle, Manager, RunEvent, State};

type Res<T> = Result<T, String>;

fn err(e: impl std::fmt::Display) -> String {
    e.to_string()
}

#[tauri::command]
async fn tailnet_status() -> Res<tailnet::Tailnet> {
    tauri::async_runtime::spawn_blocking(tailnet::status).await.map_err(err)?.map_err(err)
}

#[tauri::command]
async fn discover(bases: Vec<String>) -> Vec<api::Found> {
    api::discover(bases).await
}

#[tauri::command]
async fn api_request(req: api::Request) -> Res<api::Response> {
    api::request(req).await.map_err(err)
}

#[tauri::command]
fn get_settings() -> settings::Settings {
    settings::load()
}

#[tauri::command]
fn set_settings(s: settings::Settings) -> Res<()> {
    if !settings::valid_hostname(&s.hostname) {
        return Err("machine name may only use letters, digits and dashes".into());
    }
    for b in &s.servers {
        api::check_base(b).map_err(err)?;
    }
    settings::save(&s).map_err(err)
}

#[derive(serde::Serialize)]
struct HostSnapshot {
    state: host::HostState,
    log: Vec<String>,
}

#[tauri::command]
fn host_state(h: State<host::Host>) -> HostSnapshot {
    let (state, log) = h.snapshot();
    HostSnapshot { state, log }
}

/// The hosted node's first admin is whoever runs this app, by stable user id.
fn me() -> String {
    tailnet::status()
        .map(|t| if t.user_id != 0 { format!("id:{}", t.user_id) } else { t.login })
        .unwrap_or_default()
}

#[tauri::command]
async fn host_start(app: AppHandle, h: State<'_, host::Host>) -> Res<()> {
    let s = settings::load();
    let admin = tauri::async_runtime::spawn_blocking(me).await.map_err(err)?;
    h.start(&app, &s.hostname, &s.roots, &admin).map_err(err)
}

#[tauri::command]
async fn host_stop(h: State<'_, host::Host>) -> Res<()> {
    let h = h.inner().clone();
    tauri::async_runtime::spawn_blocking(move || h.stop()).await.map_err(err)
}

fn main() {
    // WebKitGTK's DMA-BUF renderer hard-crashes with a Wayland protocol error on
    // NVIDIA. Must be set before GTK initialises.
    #[cfg(target_os = "linux")]
    if std::env::var_os("WEBKIT_DISABLE_DMABUF_RENDERER").is_none() {
        std::env::set_var("WEBKIT_DISABLE_DMABUF_RENDERER", "1");
    }

    let app = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_process::init())
        .plugin(tauri_plugin_opener::init())
        .manage(host::Host::default())
        .setup(|app| {
            if settings::load().host_on_launch {
                let handle = app.handle().clone();
                tauri::async_runtime::spawn_blocking(move || {
                    let s = settings::load();
                    let h = handle.state::<host::Host>();
                    if let Err(e) = h.start(&handle, &s.hostname, &s.roots, &me()) {
                        eprintln!("hosting on launch: {e}");
                    }
                });
            }
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            tailnet_status,
            discover,
            api_request,
            get_settings,
            set_settings,
            host_state,
            host_start,
            host_stop,
        ])
        .build(tauri::generate_context!())
        .expect("error while building tauri application");

    app.run(|handle, event| {
        if let RunEvent::Exit = event {
            handle.state::<host::Host>().stop();
        }
    });
}
