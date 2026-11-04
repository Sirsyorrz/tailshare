import { useState } from "react";
import { writeText } from "../lib/clipboard";
import { call, describePerms, get, permsOf, type AdminStatus, type Config } from "../lib/api";
import { useLive, useLoad } from "../lib/hooks";
import { useStore } from "../state/store";

/// Everyone the server can see: who has access, who accepted a share but
/// has none yet, and who the tailnet policy is blocking.
function ago(iso: string) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return new Date(iso).toLocaleDateString();
}

export function People({ base }: { base: string }) {
  const { data, error, reload } = useLoad(async () => {
    const [status, cfg] = await Promise.all([
      get<AdminStatus>(base, "/api/admin/status"),
      call<{ config: Config; version: string }>(base, "GET", "/api/admin/config"),
    ]);
    return { status, config: cfg.data.config, version: cfg.data.version };
  }, [base]);
  const [granting, setGranting] = useState<string | null>(null);
  const [grantError, setGrantError] = useState("");
  useLive(base, reload, { paused: granting !== null, everyMs: 15_000 });

  if (error) return <div className="bad">Couldn't load: {error}</div>;
  if (!data) return <div className="dim">Loading…</div>;
  const { status, config } = data;

  if (!status.policyKnown) return <div className="dim">This server can't check the tailnet policy (dev mode).</div>;

  // Same rule as the server: admins own every share, so there's nothing to give.
  const isAdmin = (userId: number, login: string, external: boolean) =>
    (!external || !!config.allowExternalAdmins) &&
    config.admins.some((a) => a.toLowerCase() === `id:${userId}` || (login && a.toLowerCase() === login.toLowerCase()));

  const accessOf = (userId: number, login: string) =>
    config.shares
      .map((s) => {
        const v = s.access[`id:${userId}`] ?? Object.entries(s.access).find(([k]) => k.toLowerCase() === login.toLowerCase())?.[1];
        return v ? `${s.name}: ${describePerms(permsOf(v))}` : null;
      })
      .filter(Boolean) as string[];

  const byUser = new Map<number, typeof status.peers>();
  for (const p of status.peers) byUser.set(p.userId, [...(byUser.get(p.userId) ?? []), p]);
  const rows = [...byUser.values()].filter((ps) => ps.some((p) => p.hasAccess || p.external));
  const blocked = status.peers.some((p) => p.hasAccess && !p.canReach);
  const grant = status.grant ?? "";

  const give = async (userId: number, share: string) => {
    try {
      const cur = await call<{ config: Config; version: string }>(base, "GET", "/api/admin/config");
      const cfg = cur.data.config;
      const s = cfg.shares.find((x) => x.name === share);
      if (!s) return;
      s.access[`id:${userId}`] = "viewer";
      await call(base, "PUT", "/api/admin/config", cfg, cur.data.version);
      useStore.getState().say(`Gave viewer access to ${share}. Change it under Shares.`);
      setGranting(null);
      setGrantError("");
      await reload();
    } catch (e) {
      setGrantError((e as Error).message);
    }
  };

  return (
    <div>
      {blocked && (
        <div className="callout warn">
          <div>
            Someone with access can't connect because of your tailnet policy (it only lets your own devices in). Add
            this to the <code>"grants"</code> list in the policy file, in the Tailscale admin console under Access
            controls:
            <pre className="snippet">{grant}</pre>
          </div>
          <button onClick={() => void writeText(grant).then(() => useStore.getState().say("Copied"))}>Copy</button>
        </div>
      )}
      {rows.length === 0 ? (
        <p className="dim">
          Nobody yet. Share this machine from the Tailscale admin console; once someone accepts and opens it, they show
          up here.
        </p>
      ) : (
        <table className="grid">
          <thead>
            <tr>
              <th>Person</th>
              <th>Devices</th>
              <th>Access</th>
              <th>Connection</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((ps) => {
              const p = ps[0];
              const access = accessOf(p.userId, p.login);
              const reach = ps.every((x) => x.canReach) ? "ok" : ps.some((x) => x.canReach) ? "some" : "blocked";
              const seen = ps
                .map((x) => x.lastSeen)
                .filter(Boolean)
                .sort()
                .pop();
              return (
                <tr key={p.userId}>
                  <td>
                    <div>{p.login}</div>
                    <div className="dim small">
                      {p.external ? "shared in" : "your tailnet"} · id:{p.userId}
                    </div>
                  </td>
                  <td className="small">
                    {ps.map((x) => (
                      <div key={x.node}>
                        <span className={"dot " + (x.online ? "running" : "stopped")} /> {x.node}
                      </div>
                    ))}
                  </td>
                  <td className="small">
                    {isAdmin(p.userId, p.login, p.external) ? (
                      <span>Owner: every share</span>
                    ) : access.length ? (
                      access.map((a) => <div key={a}>{a}</div>)
                    ) : granting === String(p.userId) ? (
                      // No onBlur close: WebKitGTK blurs the select when its
                      // popup opens, which would remove it before the pick lands.
                      <span className="row tight">
                        <select value="" onChange={(e) => e.target.value && void give(p.userId, e.target.value)}>
                          <option value="">Pick a share…</option>
                          {config.shares.map((s) => (
                            <option key={s.name} value={s.name}>
                              {s.name}
                            </option>
                          ))}
                        </select>
                        <button
                          onClick={() => {
                            setGranting(null);
                            setGrantError("");
                          }}
                        >
                          Cancel
                        </button>
                        {grantError && <div className="bad small">Couldn't save: {grantError}</div>}
                      </span>
                    ) : (
                      <button disabled={!config.shares.length} onClick={() => setGranting(String(p.userId))}>
                        Give access
                      </button>
                    )}
                  </td>
                  <td className="small">
                    {reach === "ok" ? (
                      <span className="ok">can connect</span>
                    ) : (
                      <span className="bad">{reach === "some" ? "some devices blocked" : "blocked by tailnet policy"}</span>
                    )}
                    {seen && <div className="dim">active {ago(seen)}</div>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      <div className="row">
        <button onClick={() => void reload()}>Refresh</button>
      </div>
    </div>
  );
}
