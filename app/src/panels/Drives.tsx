import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { invoke } from "@tauri-apps/api/core";
import { open } from "@tauri-apps/plugin-dialog";
import { openPath, openUrl } from "@tauri-apps/plugin-opener";
import { useStore } from "../state/store";
import { canWrite, fmtSize, mountable, shortName, type DriveSpec, type Mounted, type ShareInfo } from "../lib/api";
import { Linkify } from "../lib/Linkify";

const isWindows = navigator.userAgent.includes("Windows");

const sameDrive = (a: { base: string; share: string }, b: { base: string; share: string }) =>
  a.base === b.base && a.share === b.share;

export function mountOf(spec: DriveSpec | undefined, mounts: Mounted[]) {
  return spec ? mounts.find((m) => m.target === spec.target) : undefined;
}

function openTarget(target: string) {
  void openPath(isWindows ? target + "\\" : target);
}

async function unmount(spec: DriveSpec) {
  const st = useStore.getState();
  const m = mountOf(spec, st.drives.mounts);
  const pending = m ? Math.max(m.queue.length, m.uploadsQueued + m.uploadsInProgress) : 0;
  if (pending > 0) {
    const { ask } = await import("@tauri-apps/plugin-dialog");
    const ok = await ask(
      `${pending} file${pending === 1 ? " hasn't" : "s haven't"} finished uploading to ${spec.share}. ` +
        "They're kept on this computer and continue the next time you mount it. Unmount anyway?",
      { title: "Uploads still pending", kind: "warning", okLabel: "Unmount", cancelLabel: "Keep mounted" },
    );
    if (!ok) return;
  }
  try {
    await invoke("drive_unmount", { base: spec.base, share: spec.share });
    st.say(`Unmounted ${spec.share}`);
  } catch (e) {
    st.say(`Couldn't unmount: ${e}`);
  }
  await st.loadSettings();
  await st.loadDrives();
}

/// Mount / Open / Unmount controls for one share, used in a server's
/// "Your access" table.
export function DriveButtons({ base, share }: { base: string; share: ShareInfo }) {
  const settings = useStore((s) => s.settings);
  const drives = useStore((s) => s.drives);
  const [asking, setAsking] = useState(false);
  const spec = settings?.drives.find((d) => sameDrive(d, { base, share: share.name }));
  const mounted = mountOf(spec, drives.mounts);

  if (!mountable(share.perms)) {
    return (
      <button disabled title="Upload-only shares can't be a drive; use Open to upload in the browser.">
        Mount
      </button>
    );
  }
  if (mounted && spec) {
    return (
      <>
        <button onClick={() => openTarget(spec.target)}>Open drive</button>
        <button onClick={() => void unmount(spec)}>Unmount</button>
      </>
    );
  }
  return (
    <>
      <button onClick={() => setAsking(true)}>Mount</button>
      {/* Portalled: it's opened from a table cell whose layout it would inherit. */}
      {asking &&
        createPortal(
          <MountDialog base={base} share={share} previous={spec} onClose={() => setAsking(false)} />,
          document.body,
        )}
    </>
  );
}

function MountDialog({
  base,
  share,
  previous,
  onClose,
}: {
  base: string;
  share: ShareInfo;
  previous?: DriveSpec;
  onClose: () => void;
}) {
  const [target, setTarget] = useState(previous?.target ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const missing = useStore((s) => s.drives.missing);
  const readOnly = !canWrite(share.perms);

  useEffect(() => {
    if (!previous) void invoke<string>("drive_suggest", { share: share.name }).then((t) => setTarget((cur) => cur || t));
  }, [previous, share.name]);

  const pick = async () => {
    const dir = await open({ directory: true, title: "Empty folder to show the share in" });
    if (typeof dir === "string") setTarget(dir);
  };

  const go = async () => {
    setBusy(true);
    setError("");
    try {
      const spec: DriveSpec = { base, share: share.name, target: target.trim(), readOnly, auto: true };
      await invoke("drive_mount", { spec });
      const st = useStore.getState();
      await st.loadSettings();
      await st.loadDrives();
      st.say(`Mounted ${share.name} at ${spec.target}`);
      openTarget(spec.target);
      onClose();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-back" onClick={onClose}>
      <div className="modal narrow" onClick={(e) => e.stopPropagation()}>
        <div className="modal-h">
          <b>Mount {share.name}</b>
        </div>
        <div className="modal-body pad">
          {missing ? (
            <MissingSupport missing={missing} />
          ) : (
            <>
              <p className="dim small">
                Shows {share.name} from {shortName(base)} as {isWindows ? "a drive in Explorer" : "a folder"}, so any
                program can open and save files on it. Files you open are cached on this computer; changes upload in the
                background (see Drives &amp; transfers).
              </p>
              {readOnly && <p className="small">You can only read this share, so the drive will be read-only.</p>}
              <label className="field stack">
                {isWindows ? "Drive letter" : "Folder"}
                <div className="row tight">
                  {isWindows ? (
                    <select value={target} onChange={(e) => setTarget(e.target.value)}>
                      {[...new Set([target, ..."TUVWXYZSRQPONMLKJIHGFED".split("").map((l) => l + ":")])]
                        .filter(Boolean)
                        .map((l) => (
                          <option key={l} value={l}>
                            {l}
                          </option>
                        ))}
                    </select>
                  ) : (
                    <>
                      <input className="grow" value={target} onChange={(e) => setTarget(e.target.value)} spellCheck={false} />
                      <button onClick={() => void pick()}>Choose…</button>
                    </>
                  )}
                </div>
              </label>
              {error && <div className="bad small"><Linkify text={error} /></div>}
            </>
          )}
        </div>
        <div className="modal-f">
          <span className="spacer" />
          <button onClick={onClose}>Cancel</button>
          {!missing && (
            <button className="primary" disabled={busy || !target.trim()} onClick={() => void go()}>
              {busy ? "Mounting…" : "Mount"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function MissingSupport({ missing }: { missing: "winfsp" | "fuse" }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const install = async () => {
    setBusy(true);
    setError("");
    try {
      await invoke("install_winfsp");
      useStore.getState().say("WinFsp installed");
      await useStore.getState().loadDrives();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  if (missing === "winfsp") {
    return (
      <div>
        <p>
          Mounting needs <b>WinFsp</b>, a free filesystem driver for Windows. tailshare can download and install it
          for you; Windows will ask for permission. No restart needed.
        </p>
        <div className="row tight">
          <button className="primary" disabled={busy} onClick={() => void install()}>
            {busy ? "Installing…" : "Install WinFsp"}
          </button>
          <button className="link small" onClick={() => void openUrl("https://winfsp.dev/rel/")}>
            or download it yourself
          </button>
        </div>
        {error && <div className="bad small"><Linkify text={error} /></div>}
      </div>
    );
  }
  return (
    <div>
      <p>Mounting needs FUSE, which your distribution provides. Install it, then try again:</p>
      <pre className="small">
        {"sudo apt install fuse3      # Debian, Ubuntu, Mint\n" +
          "sudo dnf install fuse3      # Fedora\n" +
          "sudo pacman -S fuse3        # Arch"}
      </pre>
    </div>
  );
}

function eta(s?: number) {
  if (s === undefined || s === null || s < 0) return "";
  if (s < 60) return `${s}s left`;
  if (s < 3600) return `${Math.round(s / 60)} min left`;
  return `${(s / 3600).toFixed(1)} h left`;
}

export function Drives() {
  const settings = useStore((s) => s.settings);
  const drives = useStore((s) => s.drives);
  const servers = useStore((s) => s.servers);
  const [showLog, setShowLog] = useState(false);
  const specs = settings?.drives ?? [];

  const remount = async (spec: DriveSpec) => {
    try {
      await invoke("drive_mount", { spec });
      useStore.getState().say(`Mounted ${spec.share}`);
    } catch (e) {
      useStore.getState().say(`Couldn't mount ${spec.share}: ${e}`);
    }
    await useStore.getState().loadSettings();
    await useStore.getState().loadDrives();
  };

  const forget = async (spec: DriveSpec) => {
    if (!settings) return;
    await useStore.getState().saveSettings({ ...settings, drives: settings.drives.filter((d) => !sameDrive(d, spec)) });
  };

  const transfers = drives.transfers;
  const queued = drives.mounts.flatMap((m) =>
    m.queue
      .filter((q) => !q.uploading)
      .map((q) => ({ ...q, share: specs.find((s) => s.target === m.target)?.share ?? m.target })),
  );
  const failing = queued.filter((q) => q.tries > 0);

  return (
    <div className="view">
      <h2>Drives &amp; transfers</h2>
      {drives.missing && (
        <div className="card">
          <MissingSupport missing={drives.missing} />
        </div>
      )}

      <h3>Drives</h3>
      {specs.length === 0 ? (
        <p className="dim">
          No drives yet. Open a server on the left and use <b>Mount</b> next to a share in <i>Your access</i>.
        </p>
      ) : (
        <table className="grid">
          <thead>
            <tr>
              <th>Share</th>
              <th>Where</th>
              <th>State</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {specs.map((spec) => {
              const m = mountOf(spec, drives.mounts);
              const pending = m ? Math.max(m.queue.length, m.uploadsQueued + m.uploadsInProgress) : 0;
              const known = servers.some((s) => s.base === spec.base);
              return (
                <tr key={spec.base + spec.share}>
                  <td>
                    <div>{spec.share}</div>
                    <div className="dim small">{shortName(spec.base)}</div>
                  </td>
                  <td>
                    <code>{spec.target}</code>
                  </td>
                  <td className="small">
                    {m ? (
                      <>
                        <span className="ok">mounted</span>
                        {spec.readOnly && " · read-only"}
                        {pending > 0 && ` · ${pending} to upload`}
                        <div className="dim">{fmtSize(m.cacheBytes)} cached</div>
                      </>
                    ) : (
                      <span className="dim">{spec.auto ? "not mounted (mounts at start)" : "not mounted"}</span>
                    )}
                  </td>
                  <td className="right nowrap">
                    {m ? (
                      <>
                        <button onClick={() => openTarget(spec.target)}>Open</button>
                        <button onClick={() => void unmount(spec)}>Unmount</button>
                      </>
                    ) : (
                      <>
                        <button
                          disabled={!!drives.missing}
                          title={known ? "" : "That server isn't online right now"}
                          onClick={() => void remount(spec)}
                        >
                          Mount
                        </button>
                        <button onClick={() => void forget(spec)}>Forget</button>
                      </>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}

      <h3>Transfers</h3>
      {transfers.length === 0 && queued.length === 0 ? (
        <p className="dim">Nothing transferring.</p>
      ) : (
        <table className="grid">
          <tbody>
            {transfers.map((t) => {
              const pct = t.size > 0 ? Math.min(100, (t.bytes / t.size) * 100) : 0;
              return (
                <tr key={t.remote + t.name + t.upload}>
                  <td className="grow">
                    <div className="ellipsis" title={t.name}>
                      {t.upload ? "\u2191 " : "\u2193 "}
                      {t.name}
                    </div>
                    <div className="progress">
                      <div style={{ width: pct + "%" }} />
                    </div>
                    <div className="dim small">
                      {drives.names[t.remote] ?? ""} · {fmtSize(t.bytes)} of {fmtSize(t.size)}
                      {t.speed > 0 && ` · ${fmtSize(t.speed)}/s`} {eta(t.eta)}
                    </div>
                  </td>
                </tr>
              );
            })}
            {queued.map((q) => (
              <tr key={q.share + q.name}>
                <td>
                  <div className="ellipsis" title={q.name}>
                    {"\u2191 "}
                    {q.name}
                  </div>
                  <div className={"small " + (q.tries > 0 ? "bad" : "dim")}>
                    {q.share} · {fmtSize(q.size)} ·{" "}
                    {q.tries > 0
                      ? `retrying (try ${q.tries + 1})${q.error ? ": " + q.error : ""}`
                      : `waiting, starts in ${Math.ceil(q.startsIn)}s`}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {failing.length > 0 && (
        <p className="dim small">
          Uploads that keep failing stay queued and are retried. If the server refuses them (for example you aren't
          allowed to replace that file), copy your version somewhere else and delete it from the drive.
        </p>
      )}

      <h3>Cache</h3>
      <div className="row">
        <span className="grow dim small">
          Files you open or save on a drive are kept here, up to the limit; the oldest are dropped first. Unfinished
          uploads are never dropped.
        </span>
        <label className="row-inline small">
          Limit
          <input
            className="num-input"
            type="number"
            min={1}
            value={settings?.cacheGb ?? 20}
            onChange={(e) =>
              settings && void useStore.getState().saveSettings({ ...settings, cacheGb: Math.max(1, Number(e.target.value) || 1) })
            }
          />
          GB
        </label>
      </div>
      <p className="dim small">A new limit applies the next time a drive is mounted.</p>

      {drives.running && (
        <>
          <button className="link small" onClick={() => setShowLog((v) => !v)}>
            {showLog ? "Hide" : "Show"} rclone log
          </button>
          {showLog && <pre className="log">{drives.log.join("\n") || "(empty)"}</pre>}
        </>
      )}
    </div>
  );
}
