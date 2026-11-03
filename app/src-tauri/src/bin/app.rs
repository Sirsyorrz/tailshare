#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use tailshare::{api, background, drives, host, settings, tailnet};
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
    settings::save(&s).map_err(err)?;
    host::write_roots(&s.roots).map_err(err)
}

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct HostSnapshot {
    state: host::HostState,
    log: Vec<String>,
    background: bool,
    background_supported: bool,
}

#[tauri::command]
async fn host_state(h: State<'_, host::Host>) -> Res<HostSnapshot> {
    let bg = settings::load().background;
    let (state, log) = if bg {
        tauri::async_runtime::spawn_blocking(|| (background::status(), background::log_tail(400)))
            .await
            .map_err(err)?
    } else {
        h.snapshot()
    };
    Ok(HostSnapshot { state, log, background: bg, background_supported: background::supported() })
}

fn bg_args() -> background::Args {
    background::Args { hostname: settings::load().hostname, admin: me() }
}

/// Switches between hosting inside the app and as a background service,
/// carrying over whether it was running.
#[tauri::command]
async fn set_background(enabled: bool, h: State<'_, host::Host>) -> Res<Option<String>> {
    let h = h.inner().clone();
    tauri::async_runtime::spawn_blocking(move || -> anyhow::Result<Option<String>> {
        let mut s = settings::load();
        if enabled == s.background {
            return Ok(None);
        }
        let mut note = None;
        if enabled {
            let was_running = h.snapshot().0.phase != host::Phase::Stopped;
            h.stop();
            s.background = true;
            settings::save(&s)?;
            if was_running {
                note = background::start(&bg_args())?;
            } else {
                background::sync_binary()?;
            }
        } else {
            let was_running = background::active();
            background::uninstall()?;
            s.background = false;
            settings::save(&s)?;
            note = was_running.then(|| {
                "Background hosting stopped. Start it again here to host while the app is open."
                    .into()
            });
        }
        Ok(note)
    })
    .await
    .map_err(err)?
    .map_err(err)
}

/// The hosted node's first admin is whoever runs this app, by stable user id.
fn me() -> String {
    tailnet::status()
        .map(|t| if t.user_id != 0 { format!("id:{}", t.user_id) } else { t.login })
        .unwrap_or_default()
}

#[tauri::command]
async fn host_start(app: AppHandle, h: State<'_, host::Host>) -> Res<Option<String>> {
    let s = settings::load();
    host::write_roots(&s.roots).map_err(err)?;
    if s.background {
        return tauri::async_runtime::spawn_blocking(|| background::start(&bg_args()))
            .await
            .map_err(err)?
            .map_err(err);
    }
    let admin = tauri::async_runtime::spawn_blocking(me).await.map_err(err)?;
    h.start(&app, &s.hostname, &s.roots, &admin).map_err(err)?;
    Ok(None)
}

#[tauri::command]
async fn host_stop(h: State<'_, host::Host>) -> Res<()> {
    let h = h.inner().clone();
    tauri::async_runtime::spawn_blocking(move || {
        if settings::load().background {
            background::stop()
        } else {
            h.stop();
            Ok(())
        }
    })
    .await
    .map_err(err)?
    .map_err(err)
}

#[tauri::command]
async fn drives_state(d: State<'_, drives::Drives>) -> Res<drives::DrivesState> {
    let mut st = d.state().await;
    for spec in settings::load().drives {
        st.names.insert(drives::remote_name(&spec.base, &spec.share), spec.share);
    }
    Ok(st)
}

#[tauri::command]
fn drive_suggest(share: String) -> String {
    let taken: Vec<String> = settings::load().drives.into_iter().map(|d| d.target).collect();
    drives::suggest_target(&share, &taken)
}

/// Mounts and remembers the drive, so it comes back when the app starts.
#[tauri::command]
async fn drive_mount(spec: settings::DriveSpec, d: State<'_, drives::Drives>) -> Res<()> {
    let s = settings::load();
    if s.drives.iter().any(|x| {
        x.target == spec.target && (x.base != spec.base || x.share != spec.share) && x.auto
    }) {
        return Err(format!("{} is already used by another drive", spec.target));
    }
    d.mount(&spec, s.cache_gb).await.map_err(err)?;
    let mut s = settings::load();
    s.drives.retain(|x| !(x.base == spec.base && x.share == spec.share));
    s.drives.push(settings::DriveSpec { auto: true, ..spec });
    settings::save(&s).map_err(err)
}

#[tauri::command]
async fn drive_unmount(base: String, share: String, d: State<'_, drives::Drives>) -> Res<()> {
    let mut s = settings::load();
    let Some(spec) = s.drives.iter_mut().find(|x| x.base == base && x.share == share) else {
        return Ok(());
    };
    d.unmount(&spec.target).await.map_err(err)?;
    spec.auto = false;
    settings::save(&s).map_err(err)
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
        .manage(drives::Drives::default())
        .setup(|app| {
            let s = settings::load();
            if s.background {
                // An app update may carry a newer server.
                tauri::async_runtime::spawn_blocking(|| {
                    if let Err(e) = background::refresh_binary(&bg_args()) {
                        eprintln!("updating background server: {e}");
                    }
                });
            } else if s.host_on_launch {
                let handle = app.handle().clone();
                tauri::async_runtime::spawn_blocking(move || {
                    let s = settings::load();
                    let h = handle.state::<host::Host>();
                    if let Err(e) = h.start(&handle, &s.hostname, &s.roots, &me()) {
                        eprintln!("hosting on launch: {e}");
                    }
                });
            }
            let auto: Vec<_> = s.drives.iter().filter(|d| d.auto).cloned().collect();
            if !auto.is_empty() {
                let d = app.state::<drives::Drives>().inner().clone();
                let gb = s.cache_gb;
                tauri::async_runtime::spawn(async move {
                    for spec in auto {
                        if let Err(e) = d.mount(&spec, gb).await {
                            eprintln!("mounting {} at {}: {e}", spec.share, spec.target);
                        }
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
            set_background,
            drives_state,
            drive_suggest,
            drive_mount,
            drive_unmount,
        ])
        .build(tauri::generate_context!())
        .expect("error while building tauri application");

    app.run(|handle, event| {
        // Background hosting belongs to the OS service, not to this process.
        if let RunEvent::Exit = event {
            handle.state::<host::Host>().stop();
            handle.state::<drives::Drives>().shutdown();
        }
    });
}
