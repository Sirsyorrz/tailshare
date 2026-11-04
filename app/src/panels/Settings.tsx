import { useState } from "react";
import { open } from "@tauri-apps/plugin-dialog";
import { useStore } from "../state/store";
import { baseOf } from "../lib/api";
import { Linkify } from "../lib/Linkify";

export function Settings() {
  const isOpen = useStore((s) => s.showSettings);
  const settings = useStore((s) => s.settings);
  const [server, setServer] = useState("");
  const [error, setError] = useState("");
  const close = () => useStore.getState().setShowSettings(false);

  if (!isOpen || !settings) return null;

  const save = async (next: typeof settings) => {
    try {
      await useStore.getState().saveSettings(next);
      setError("");
    } catch (e) {
      setError(String(e));
    }
  };

  const addRoot = async () => {
    const dir = await open({ directory: true, title: "Folder shares may be created in" });
    if (typeof dir === "string" && !settings.roots.includes(dir)) void save({ ...settings, roots: [...settings.roots, dir] });
  };

  const addServer = () => {
    let s = server.trim();
    if (!s) return;
    if (!/^https?:\/\//.test(s)) s = "https://" + s;
    try {
      const b = baseOf(s);
      if (!settings.servers.includes(b)) void save({ ...settings, servers: [...settings.servers, b] });
      setServer("");
      void useStore.getState().scan();
    } catch {
      setError("That isn't a valid address");
    }
  };

  return (
    <div className="modal-back" onClick={close}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-h">
          <b>Settings</b>
          <span className="spacer" />
          <button onClick={close}>Close</button>
        </div>

        <div className="modal-body">
          {error && <div className="bad pad"><Linkify text={error} /></div>}

          <div className="sidebar-h">Hosting</div>
          <div className="setrow">
            <div className="setlabel">
              Allowed folders
              <div className="dim">
                Shares can only be made inside these, so a mistake in the share editor can't expose the rest of this
                computer.
              </div>
              {settings.roots.length === 0 && <div className="bad small">None yet, so nothing can be shared.</div>}
              <ul className="plain">
                {settings.roots.map((r) => (
                  <li key={r} className="row">
                    <code className="grow">{r}</code>
                    <button onClick={() => void save({ ...settings, roots: settings.roots.filter((x) => x !== r) })}>Remove</button>
                  </li>
                ))}
              </ul>
            </div>
            <button onClick={() => void addRoot()}>Add</button>
          </div>

          <div className="sidebar-h">Servers</div>
          <div className="setrow">
            <div className="setlabel">
              Extra servers
              <div className="dim">Servers on your tailnet are found automatically. Add one here if it isn't.</div>
              <ul className="plain">
                {settings.servers.map((s) => (
                  <li key={s} className="row">
                    <code className="grow">{s}</code>
                    <button onClick={() => void save({ ...settings, servers: settings.servers.filter((x) => x !== s) })}>
                      Remove
                    </button>
                  </li>
                ))}
              </ul>
              <div className="row">
                <input
                  className="grow"
                  placeholder="tailshare-nas.your-tailnet.ts.net"
                  value={server}
                  onChange={(e) => setServer(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && addServer()}
                />
                <button onClick={addServer}>Add</button>
              </div>
            </div>
          </div>

          <div className="sidebar-h">Updates</div>
          <div className="setrow">
            <div className="setlabel">
              Check for updates
              <div className="dim">Updates are checked automatically on launch.</div>
            </div>
            <button
              onClick={() => {
                close();
                useStore.setState((v) => ({ updateNonce: v.updateNonce + 1 }));
              }}
            >
              Check
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
