import { useEffect, useMemo, useState } from "react";
import { ask } from "@tauri-apps/plugin-dialog";
import {
  call,
  get,
  permsOf,
  presetOf,
  PERMS,
  PERM_LABEL,
  PRESETS,
  PRESET_HELP,
  ApiError,
  type AdminStatus,
  type Config,
  type Perm,
  type ShareConfig,
} from "../lib/api";
import { useLive, useLoad } from "../lib/hooks";
import { useStore } from "../state/store";
import { FolderPicker } from "./FolderPicker";

const NAME_RE = /^[a-z0-9_][a-z0-9_ ()-]{0,63}$/;

// The editor always works on permission lists; presets are a display and
// file-format detail.
interface DraftShare extends Omit<ShareConfig, "access"> {
  access: Record<string, Perm[]>;
}
interface Draft extends Omit<Config, "shares"> {
  shares: DraftShare[];
}

const ordered = (ps: Perm[]) => PERMS.filter((p) => ps.includes(p));

function toDraft(c: Config): Draft {
  return {
    ...c,
    admins: [...c.admins],
    shares: c.shares.map((s) => ({
      ...s,
      access: Object.fromEntries(Object.entries(s.access).map(([k, v]) => [k, ordered(permsOf(v))])),
      expires: { ...(s.expires ?? {}) },
      quotaGB: { ...(s.quotaGB ?? {}) },
    })),
  };
}

// Admins own every share on the server side, so per-share entries for them
// do nothing and only confuse; they're dropped on save.
function isAdminKey(admins: string[], people: Map<string, Person>, key: string) {
  const a = admins.map((x) => x.toLowerCase());
  return a.includes(key.toLowerCase()) || a.includes(labelFor(people, key).toLowerCase());
}

function withoutAdmins(d: Draft, people: Map<string, Person>): Draft {
  const next = structuredClone(d);
  for (const s of next.shares)
    for (const k of Object.keys(s.access))
      if (isAdminKey(next.admins, people, k)) {
        delete s.access[k];
        delete s.quotaGB?.[k];
        delete s.expires?.[k];
      }
  return next;
}

function fromDraft(d: Draft): Config {
  const clean = <T,>(m: Record<string, T> | undefined, keys: string[]) => {
    const out = Object.fromEntries(Object.entries(m ?? {}).filter(([k, v]) => keys.includes(k) && v !== undefined && v !== ""));
    return Object.keys(out).length ? out : undefined;
  };
  return {
    ...d,
    shares: d.shares.map((s) => {
      const keys = Object.keys(s.access);
      return {
        ...s,
        readOnly: s.readOnly || undefined,
        access: Object.fromEntries(Object.entries(s.access).map(([k, v]) => [k, presetOf(v) ?? v])),
        expires: clean(s.expires, keys),
        quotaGB: clean(s.quotaGB, keys),
      };
    }),
  };
}

export interface Person {
  key: string;
  label: string;
  sub: string;
  external: boolean;
}

/// Names for config keys: "id:123" means nothing to a person, so show the
/// login the server has seen for that id.
export function usePeople(status: AdminStatus | null | undefined) {
  return useMemo(() => {
    const byKey = new Map<string, Person>();
    for (const p of status?.peers ?? []) {
      const key = `id:${p.userId}`;
      if (!byKey.has(key))
        byKey.set(key, { key, label: p.login, sub: p.external ? "shared in" : "your tailnet", external: p.external });
    }
    return byKey;
  }, [status]);
}

export function labelFor(people: Map<string, Person>, key: string) {
  return people.get(key)?.label ?? key;
}

function slug(path: string) {
  const base = path.split(/[\\/]/).filter(Boolean).pop() ?? "share";
  return (
    base
      .toLowerCase()
      .replace(/[^a-z0-9_ ()-]+/g, "-")
      .replace(/^[^a-z0-9_]+/, "")
      .slice(0, 64) || "share"
  );
}

function problems(d: Draft): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const s of d.shares) {
    if (!NAME_RE.test(s.name)) out.push(`"${s.name}": names use lowercase letters, digits, spaces, - _ ( )`);
    if (seen.has(s.name)) out.push(`two shares are called "${s.name}"`);
    seen.add(s.name);
  }
  return out;
}

export function Shares({ base, myKeys }: { base: string; myKeys: string[] }) {
  const { data, error, reload } = useLoad(async () => {
    const [cfg, status] = await Promise.all([
      call<{ config: Config; version: string; roots: string[] }>(base, "GET", "/api/admin/config"),
      get<AdminStatus>(base, "/api/admin/status"),
    ]);
    return { ...cfg.data, status };
  }, [base]);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [picking, setPicking] = useState<{ index: number | null; start?: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState("");
  const people = usePeople(data?.status);

  useEffect(() => {
    if (data) setDraft(withoutAdmins(toDraft(data.config), people));
  }, [data, people]);

  const dirty = !!data && !!draft && JSON.stringify(draft) !== JSON.stringify(withoutAdmins(toDraft(data.config), people));
  useLive(base, reload, { paused: dirty || saving || picking !== null });

  if (error) return <div className="bad">Couldn't load: {error}</div>;
  if (!data || !draft) return <div className="dim">Loading…</div>;

  const issues = problems(draft);

  const edit = (f: (d: Draft) => void) => {
    const next = structuredClone(draft);
    f(next);
    setDraft(next);
  };
  const editShare = (i: number, f: (s: DraftShare) => void) => edit((d) => f(d.shares[i]));

  const save = async () => {
    setSaving(true);
    setSaveError("");
    try {
      await call(base, "PUT", "/api/admin/config", fromDraft(draft), data.version);
      useStore.getState().say("Saved");
      await reload();
    } catch (e) {
      setSaveError(
        e instanceof ApiError && e.status === 412
          ? "Someone else changed the settings since you opened them. Discard and try again."
          : String((e as Error).message),
      );
    } finally {
      setSaving(false);
    }
  };

  const picked = (path: string) => {
    if (!picking) return;
    if (picking.index === null) {
      let name = slug(path);
      for (let n = 2; draft.shares.some((s) => s.name === name); n++) name = `${slug(path)}-${n}`;
      edit((d) => d.shares.push({ name, path, access: {}, expires: {}, quotaGB: {} }));
    } else {
      editShare(picking.index, (s) => (s.path = path));
    }
    setPicking(null);
  };

  const shareState = (name: string) => data.status.shares.find((s) => s.name === name);
  // By folder, so it still finds the share while it's being renamed.
  const trashIn = (path: string) => data.status.shares.find((s) => s.path === path)?.trashCount ?? 0;
  // The server deletes a folder's data (trash, unfinished uploads) once no
  // share uses it.
  const leaveWarning = (path: string) => {
    const n = trashIn(path);
    return n > 0
      ? ` The ${n} item${n === 1 ? "" : "s"} in its trash will be deleted for good, along with unfinished uploads.`
      : " Unfinished uploads into it are discarded.";
  };

  return (
    <div className="editor">
      {draft.shares.length === 0 && <p className="dim">Nothing is shared yet. Pick a folder to start.</p>}

      {draft.shares.map((s, i) => {
        const live = shareState(s.name);
        return (
          <div className="card" key={i}>
            <div className="row">
              <input
                className={"share-name" + (NAME_RE.test(s.name) ? "" : " invalid")}
                value={s.name}
                onChange={(e) => editShare(i, (x) => (x.name = e.target.value))}
                spellCheck={false}
                title="Share name, part of the web address"
              />
              {live && !live.online && <span className="bad small">folder unavailable</span>}
              <span className="spacer" />
              <label className="check">
                <input type="checkbox" checked={!!s.readOnly} onChange={(e) => editShare(i, (x) => (x.readOnly = e.target.checked))} />
                Read-only
              </label>
              <button
                className="danger"
                onClick={async () => {
                  const msg = `Stop sharing "${s.name}"? Its files stay where they are.` + leaveWarning(s.path);
                  if (await ask(msg, { title: "Remove share", kind: "warning", okLabel: "Remove", cancelLabel: "Cancel" }))
                    edit((d) => d.shares.splice(i, 1));
                }}
              >
                Remove
              </button>
            </div>
            <div className="row">
              <code className="grow">{s.path}</code>
              <button
                onClick={async () => {
                  if (trashIn(s.path) > 0) {
                    const msg = `Move "${s.name}" to another folder?` + leaveWarning(s.path);
                    if (!(await ask(msg, { title: "Change folder", kind: "warning", okLabel: "Choose folder", cancelLabel: "Cancel" })))
                      return;
                  }
                  setPicking({ index: i, start: s.path });
                }}
              >
                Change folder
              </button>
            </div>

            <AccessTable
              share={s}
              people={people}
              admins={draft.admins}
              onChange={(f) => editShare(i, f)}
            />
          </div>
        );
      })}

      <div className="row">
        <button onClick={() => setPicking({ index: null })}>+ Share a folder</button>
      </div>

      <ServerSettings draft={draft} edit={edit} people={people} myKeys={myKeys} />

      {picking && <FolderPicker base={base} start={picking.start} onPick={picked} onClose={() => setPicking(null)} />}

      {(dirty || saveError) && (
        <div className="savebar">
          {issues.length ? (
            <span className="bad">{issues[0]}</span>
          ) : saveError ? (
            <span className="bad">{saveError}</span>
          ) : (
            <span>Unsaved changes</span>
          )}
          <span className="spacer" />
          <button
            onClick={() => {
              setSaveError("");
              void reload();
            }}
          >
            Discard
          </button>
          <button className="primary" disabled={saving || issues.length > 0 || !dirty} onClick={() => void save()}>
            {saving ? "Saving…" : "Save"}
          </button>
        </div>
      )}
    </div>
  );
}

function AccessTable({
  share,
  people,
  admins,
  onChange,
}: {
  share: DraftShare;
  people: Map<string, Person>;
  admins: string[];
  onChange: (f: (s: DraftShare) => void) => void;
}) {
  const [custom, setCustom] = useState<Set<string>>(new Set());
  const [typing, setTyping] = useState(false);
  const [typed, setTyped] = useState("");
  const keys = Object.keys(share.access).filter((k) => !isAdminKey(admins, people, k));
  const candidates = [...people.values()].filter(
    (p) => !keys.includes(p.key) && !keys.includes(p.label) && !isAdminKey(admins, people, p.key),
  );

  const add = (key: string) => {
    key = key.trim();
    if (!key || keys.includes(key)) return;
    onChange((s) => (s.access[key] = [...PRESETS.viewer]));
  };

  return (
    <>
      <table className="grid">
          <thead>
            <tr>
              <th>Person</th>
              <th>Access</th>
              <th title="Most they can upload into this share">Upload limit</th>
              <th title="Last day they have access">Until</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {admins.map((a) => (
              <tr key={"admin:" + a}>
                <td>
                  <div>{labelFor(people, a)}</div>
                  <div className="dim small">admin</div>
                </td>
                <td className="small" title="Admins always have full control of every share">
                  Owner: full control{share.readOnly ? " (read-only share)" : ""}
                </td>
                <td className="small dim">No limit</td>
                <td className="small dim">Never expires</td>
                <td />
              </tr>
            ))}
            {keys.map((k) => {
              const perms = share.access[k];
              const preset = presetOf(perms);
              const showBoxes = custom.has(k) || !preset;
              const person = people.get(k);
              const exp = share.expires?.[k] ?? "";
              return (
                <tr key={k}>
                  <td>
                    <div>{person?.label ?? k}</div>
                    <div className="dim small">{person ? person.sub : k.startsWith("id:") ? "not seen yet" : "login"}</div>
                  </td>
                  <td>
                    <select
                      value={showBoxes ? "custom" : preset!}
                      onChange={(e) => {
                        const v = e.target.value;
                        const next = new Set(custom);
                        if (v === "custom") next.add(k);
                        else {
                          next.delete(k);
                          onChange((s) => (s.access[k] = [...PRESETS[v]]));
                        }
                        setCustom(next);
                      }}
                    >
                      {Object.keys(PRESETS).map((p) => (
                        <option key={p} value={p}>
                          {p} ({PRESET_HELP[p]})
                        </option>
                      ))}
                      <option value="custom">custom</option>
                    </select>
                    {showBoxes && (
                      <div className="perm-boxes">
                        {PERMS.map((p) => (
                          <label key={p} className="check">
                            <input
                              type="checkbox"
                              checked={perms.includes(p)}
                              onChange={(e) =>
                                onChange((s) => {
                                  const set = new Set(s.access[k]);
                                  if (e.target.checked) set.add(p);
                                  else set.delete(p);
                                  s.access[k] = ordered([...set]);
                                })
                              }
                            />
                            {PERM_LABEL[p]}
                          </label>
                        ))}
                      </div>
                    )}
                  </td>
                  <td>
                    <span className="unit">
                      <input
                        type="number"
                        min={0}
                        step="any"
                        placeholder="none"
                        value={share.quotaGB?.[k] ?? ""}
                        onChange={(e) =>
                          onChange((s) => {
                            s.quotaGB ??= {};
                            if (e.target.value === "") delete s.quotaGB[k];
                            else s.quotaGB[k] = Math.max(0, Number(e.target.value));
                          })
                        }
                      />
                      GB
                    </span>
                  </td>
                  <td>
                    <ExpiryCell
                      value={exp}
                      onChange={(v) =>
                        onChange((s) => {
                          s.expires ??= {};
                          if (v) s.expires[k] = v;
                          else delete s.expires[k];
                        })
                      }
                    />
                  </td>
                  <td className="right">
                    <button
                      className="danger"
                      title="Remove their access"
                      onClick={() =>
                        onChange((s) => {
                          delete s.access[k];
                          delete s.quotaGB?.[k];
                          delete s.expires?.[k];
                        })
                      }
                    >
                      Remove
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
      </table>
      {keys.length === 0 && <div className="dim small">Nobody else has access yet.</div>}

      <div className="row">
        {typing ? (
          <>
            <input
              autoFocus
              placeholder="login, e.g. friend@example.com"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  add(typed);
                  setTyped("");
                  setTyping(false);
                }
                if (e.key === "Escape") setTyping(false);
              }}
            />
            <button
              onClick={() => {
                add(typed);
                setTyped("");
                setTyping(false);
              }}
            >
              Add
            </button>
          </>
        ) : (
          <select
            value=""
            onChange={(e) => {
              if (e.target.value === "\u0000type") setTyping(true);
              else add(e.target.value);
            }}
          >
            <option value="">+ Give someone access…</option>
            {candidates.map((p) => (
              <option key={p.key} value={p.key}>
                {p.label} ({p.sub})
              </option>
            ))}
            <option value={"\u0000type"}>Someone not listed (type their login)…</option>
          </select>
        )}
      </div>
    </>
  );
}

function ServerSettings({
  draft,
  edit,
  people,
  myKeys,
}: {
  draft: Draft;
  edit: (f: (d: Draft) => void) => void;
  people: Map<string, Person>;
  myKeys: string[];
}) {
  const own = [...people.values()].filter((p) => !p.external && !draft.admins.includes(p.key) && !draft.admins.includes(p.label));
  return (
    <>
      <h3>Server</h3>
      <div className="card">
        <div className="setrow">
          <div className="setlabel">
            Admins
            <div className="dim">Can change these settings. Only from devices in your own tailnet.</div>
          </div>
          <div className="chips">
            {draft.admins.map((a) => (
              <span className="chip" key={a}>
                {labelFor(people, a)}
                {!myKeys.includes(a.toLowerCase()) && (
                  <button className="x" title="Remove admin" onClick={() => edit((d) => (d.admins = d.admins.filter((x) => x !== a)))}>
                    ×
                  </button>
                )}
              </span>
            ))}
            {own.length > 0 && (
              <select value="" onChange={(e) => e.target.value && edit((d) => d.admins.push(e.target.value))}>
                <option value="">+ Add</option>
                {own.map((p) => (
                  <option key={p.key} value={p.key}>
                    {p.label}
                  </option>
                ))}
              </select>
            )}
          </div>
        </div>
        <div className="setrow">
          <div className="setlabel">
            Keep free
            <div className="dim">Uploads are refused if they would leave less than this free on the disk.</div>
          </div>
          <span className="unit">
            <input
              type="number"
              min={0}
              step="any"
              placeholder="5"
              value={draft.minFreeGB ?? ""}
              onChange={(e) =>
                edit((d) => {
                  if (e.target.value === "") delete d.minFreeGB;
                  else d.minFreeGB = Math.max(0, Number(e.target.value));
                })
              }
            />
            GB
          </span>
        </div>
      </div>
    </>
  );
}

function isoDay(d: Date) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

// "" means the access never expires.
function ExpiryCell({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  if (!value) {
    return (
      <select
        value="never"
        onChange={(e) => {
          if (e.target.value !== "date") return;
          const d = new Date();
          d.setDate(d.getDate() + 30);
          onChange(isoDay(d));
        }}
      >
        <option value="never">Never expires</option>
        <option value="date">Until a date...</option>
      </select>
    );
  }
  return (
    <span className="row-inline">
      {/^\d{4}-\d{2}-\d{2}$/.test(value) ? (
        <input type="date" value={value} min={isoDay(new Date())} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <span className="small">{new Date(value).toLocaleString()}</span>
      )}
      <button title="Never expire" onClick={() => onChange("")}>
        Never
      </button>
    </span>
  );
}
