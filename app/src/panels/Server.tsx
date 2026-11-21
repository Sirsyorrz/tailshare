import { useEffect, useState } from "react";
import { openUrl } from "@tauri-apps/plugin-opener";
import { describePerms, fmtSize, shortName } from "../lib/api";
import type { Server as ServerT } from "../state/store";
import { Shares } from "./Shares";
import { People } from "./People";
import { Activity } from "./Activity";
import { Trash } from "./Trash";
import { DriveButtons } from "./Drives";
import { Linkify } from "../lib/Linkify";

type Tab = "access" | "shares" | "people" | "activity" | "trash";

export function Server({ server }: { server: ServerT }) {
  const admin = !!server.me?.admin;
  const [tab, setTab] = useState<Tab>(admin ? "shares" : "access");
  useEffect(() => setTab(admin ? "shares" : "access"), [server.base, admin]);

  const me = server.me;
  const tabs: [Tab, string][] = admin
    ? [
        ["shares", "Shares"],
        ["people", "People"],
        ["activity", "Activity"],
        ["trash", "Trash"],
        ["access", "Your access"],
      ]
    : [];

  return (
    <div className="view">
      <div className="row">
        <h2 className="grow">{shortName(server.base)}</h2>
        <button onClick={() => void openUrl(server.base)}>Open in browser</button>
      </div>
      <div className="dim small">
        <code>{server.base}</code>
        {server.version && ` · version ${server.version}`}
        {admin && " · you're an admin"}
      </div>

      {tabs.length > 0 && (
        <div className="tabs">
          {tabs.map(([t, label]) => (
            <button key={t} className={tab === t ? "active" : ""} onClick={() => setTab(t)}>
              {label}
            </button>
          ))}
        </div>
      )}

      <div className="tab-body">
        {server.error && <div className="bad"><Linkify text={server.error} /></div>}
        {tab === "access" && me && (
          me.shares.length === 0 ? (
            <p className="dim">
              You're connected as {me.user.login}, but nothing on this server is shared with you yet.
            </p>
          ) : (
            <table className="grid">
              <thead>
                <tr>
                  <th>Share</th>
                  <th>You can</th>
                  <th>Uploaded</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {me.shares.map((s) => (
                  <tr key={s.name}>
                    <td>{s.name}</td>
                    <td className="small">
                      {s.owner
                        ? `Owner: full control${s.perms.includes("upload") ? "" : " (read-only share)"}`
                        : describePerms(s.perms)}
                      {s.expires && <div className="dim">until {new Date(new Date(s.expires).getTime() - 1).toLocaleDateString()}</div>}
                    </td>
                    <td className="small">
                      {fmtSize(s.used)}
                      {s.quota ? ` of ${fmtSize(s.quota)}` : ""}
                    </td>
                    <td className="right nowrap">
                      <button onClick={() => void openUrl(server.base + "#" + encodeURIComponent(s.name))}>Open in browser</button>
                      <DriveButtons base={server.base} share={s} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )
        )}
        {tab === "shares" && me && (
          <Shares base={server.base} myKeys={[`id:${me.user.id}`, me.user.login.toLowerCase()]} />
        )}
        {tab === "people" && <People base={server.base} />}
        {tab === "activity" && <Activity base={server.base} />}
        {tab === "trash" && <Trash base={server.base} />}
      </div>
    </div>
  );
}
