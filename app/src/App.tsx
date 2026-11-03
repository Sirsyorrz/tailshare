import { useEffect, useState } from "react";
import { listen } from "@tauri-apps/api/event";
import { useStore } from "./state/store";
import { Settings } from "./panels/Settings";
import { Updater } from "./panels/Updater";
import { Host } from "./panels/Host";
import { Server } from "./panels/Server";
import { baseOf, shortName, type HostState } from "./lib/api";
import "./app.css";

function Status() {
  const nonce = useStore((s) => s.updateNonce);
  const [checked, setChecked] = useState(0);
  const message = useStore((s) => s.message);
  const tailnet = useStore((s) => s.tailnet);
  return (
    <div className="status">
      <Updater manual={nonce > checked} onDone={() => setChecked(nonce)} />
      <span>{message}</span>
      <span className="spacer" />
      {tailnet?.running && (
        <span>
          {tailnet.login} on {tailnet.node.split(".")[0]}
        </span>
      )}
    </div>
  );
}

function Sidebar() {
  const servers = useStore((s) => s.servers);
  const selected = useStore((s) => s.selected);
  const host = useStore((s) => s.host);
  const scanning = useStore((s) => s.scanning);
  const select = useStore((s) => s.select);
  const hostBase = host.url ? baseOf(host.url) : "";

  return (
    <div className="sidebar">
      <div className="sidebar-h">This computer</div>
      <ul className="nav">
        <li className={selected.kind === "host" ? "active" : ""} onClick={() => select({ kind: "host" })}>
          <span className={"dot " + host.phase} />
          <span className="grow">Hosting</span>
          <span className="dim small">{host.phase === "running" ? host.hostname : ""}</span>
        </li>
      </ul>
      <div className="sidebar-h">Servers</div>
      <ul className="nav">
        {servers.map((s) => {
          const active = selected.kind === "server" && selected.base === s.base;
          const sub = s.error
            ? "can't load"
            : s.me?.admin
              ? "admin"
              : `${s.me?.shares.length ?? 0} share${s.me?.shares.length === 1 ? "" : "s"}`;
          return (
            <li key={s.base} className={active ? "active" : ""} onClick={() => select({ kind: "server", base: s.base })}>
              <span className="grow">
                {shortName(s.base)}
                {s.base === hostBase && <span className="dim small"> (this computer)</span>}
              </span>
              <span className="dim small">{sub}</span>
            </li>
          );
        })}
        {servers.length === 0 && <li className="dim small static">{scanning ? "Looking…" : "None found on your tailnet."}</li>}
      </ul>
    </div>
  );
}

function Main() {
  const selected = useStore((s) => s.selected);
  const servers = useStore((s) => s.servers);
  const tailnet = useStore((s) => s.tailnet);
  const tailnetError = useStore((s) => s.tailnetError);

  if (tailnetError || (tailnet && !tailnet.running)) {
    return (
      <div className="main">
        <div className="view">
          <h2>Tailscale isn't connected</h2>
          <p className="dim">
            {tailnetError || `Tailscale is ${tailnet?.backendState || "not running"}.`} Install Tailscale, sign in, and
            press Refresh.
          </p>
        </div>
      </div>
    );
  }
  if (selected.kind === "server") {
    const s = servers.find((x) => x.base === selected.base);
    return (
      <div className="main">{s ? <Server key={s.base} server={s} /> : <div className="view dim">Looking for that server…</div>}</div>
    );
  }
  return (
    <div className="main">
      <Host />
    </div>
  );
}

export default function App() {
  const scanning = useStore((s) => s.scanning);

  useEffect(() => {
    const st = useStore.getState();
    void (async () => {
      await st.loadSettings();
      await st.loadHost();
      await useStore.getState().scan();
    })();
    const unlisten = [
      listen<HostState>("host-state", (e) => {
        const was = useStore.getState().host.phase;
        useStore.setState({ host: e.payload });
        // The new node takes a moment to appear to this computer's Tailscale.
        if (e.payload.phase === "running" && was !== "running") {
          setTimeout(() => void useStore.getState().scan(), 3000);
          setTimeout(() => void useStore.getState().scan(), 15000);
        }
      }),
      listen<string>("host-log", (e) => useStore.setState((s) => ({ hostLog: [...s.hostLog.slice(-399), e.payload] }))),
    ];
    const t = setInterval(() => void useStore.getState().scan(), 60_000);
    // A background server isn't our child, so its state is polled from its
    // status file instead of pushed.
    const poll = setInterval(() => useStore.getState().background && void useStore.getState().loadHost(), 2000);
    return () => {
      clearInterval(t);
      clearInterval(poll);
      unlisten.forEach((p) => void p.then((f) => f()));
    };
  }, []);

  return (
    <div className="shell">
      <div className="topbar">
        <b className="brand">tailshare</b>
        <span className="spacer" />
        <button onClick={() => void useStore.getState().scan()} disabled={scanning}>
          {scanning ? "Refreshing…" : "Refresh"}
        </button>
        <button onClick={() => useStore.getState().setShowSettings(true)}>Settings</button>
      </div>
      <Sidebar />
      <Main />
      <Status />
      <Settings />
    </div>
  );
}
