import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { openUrl } from "@tauri-apps/plugin-opener";
import { useStore } from "../state/store";
import { baseOf } from "../lib/api";

export function Host() {
  const host = useStore((s) => s.host);
  const log = useStore((s) => s.hostLog);
  const settings = useStore((s) => s.settings);
  const tailnet = useStore((s) => s.tailnet);
  const [busy, setBusy] = useState(false);
  const [showLog, setShowLog] = useState(false);
  const [name, setName] = useState("");

  useEffect(() => {
    if (settings) setName(settings.hostname);
  }, [settings?.hostname]);

  const start = async () => {
    setBusy(true);
    try {
      if (settings && name !== settings.hostname) await useStore.getState().saveSettings({ ...settings, hostname: name });
      await invoke("host_start");
    } catch (e) {
      useStore.getState().say(String(e));
    } finally {
      setBusy(false);
    }
  };
  const stop = async () => {
    setBusy(true);
    try {
      await invoke("host_stop");
    } finally {
      setBusy(false);
    }
  };
  const setOnLaunch = (v: boolean) => settings && void useStore.getState().saveSettings({ ...settings, hostOnLaunch: v });

  const running = host.phase === "running";
  const active = host.phase === "starting" || host.phase === "login" || running;
  const machinesURL = `https://login.tailscale.com/admin/machines?q=${encodeURIComponent(host.hostname || name)}`;

  return (
    <div className="view">
      <h2>Host from this computer</h2>
      <p className="dim">
        Runs tailshare as its own machine on your tailnet. Friends you share it with can reach that machine and the
        folders you pick, and nothing else on this computer.
      </p>

      <div className="card">
        <div className="row">
          <span className={"dot " + host.phase} />
          <b>
            {
              {
                stopped: "Not hosting",
                starting: "Starting\u2026",
                login: "Waiting for sign-in",
                running: "Hosting",
                failed: "Stopped with an error",
              }[host.phase]
            }
          </b>
          {running && host.url && <code>{host.url}</code>}
          <span className="spacer" />
          {active ? (
            <button onClick={() => void stop()} disabled={busy}>
              Stop
            </button>
          ) : (
            <button className="primary" onClick={() => void start()} disabled={busy || !tailnet?.running}>
              Start hosting
            </button>
          )}
        </div>

        {!active && (
          <div className="row">
            <label className="field">
              Machine name
              <input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} spellCheck={false} />
            </label>
            <span className="dim small">
              Becomes <code>https://{name || "\u2026"}.{tailnet?.node.split(".").slice(1).join(".") || "your-tailnet.ts.net"}</code>
            </span>
          </div>
        )}

        {host.phase === "login" && host.loginUrl && (
          <div className="callout">
            <div>
              Approve the new <b>{host.hostname}</b> machine with <b>your</b> Tailscale account. This only happens once.
            </div>
            <button className="primary" onClick={() => void openUrl(host.loginUrl!)}>
              Sign in
            </button>
          </div>
        )}
        {host.phase === "starting" && (
          <div className="dim small">Connecting to Tailscale. The first start can take a minute while it gets an HTTPS certificate.</div>
        )}
        {host.phase === "failed" && host.error && <div className="bad small">{host.error}</div>}

        {running && host.url && (
          <div className="row">
            <button
              className="primary"
              onClick={() => useStore.getState().select({ kind: "server", base: baseOf(host.url!) })}
            >
              Manage shares
            </button>
            <button onClick={() => void openUrl(host.url!)}>Open in browser</button>
          </div>
        )}

        <label className="check">
          <input type="checkbox" checked={!!settings?.hostOnLaunch} onChange={(e) => setOnLaunch(e.target.checked)} />
          Start hosting when tailshare opens
        </label>
        <div className="dim small">Closing tailshare stops hosting.</div>
      </div>

      {running && (
        <>
          <h3>Sharing with a friend</h3>
          <ol className="steps">
            <li>
              In the Tailscale admin console, open the <b>{host.hostname}</b> machine, choose <b>Share</b> and send the
              invite. Your friend accepts it with their own Tailscale account.
              <div>
                <button onClick={() => void openUrl(machinesURL)}>Open admin console</button>
              </div>
            </li>
            <li>They open {host.url} once in their browser.</li>
            <li>
              They then show up under <b>Manage shares</b> (People), where you give them access to folders.
            </li>
          </ol>
        </>
      )}

      <h3>
        <button className="link" onClick={() => setShowLog(!showLog)}>
          {showLog ? "Hide" : "Show"} server log
        </button>
      </h3>
      {showLog && <pre className="log">{log.join("\n") || "Nothing yet."}</pre>}
    </div>
  );
}
