import { $, el, api, fmtSize, fmtDate, status } from "./util.js";
import { state, emit } from "./state.js";
import { modal } from "./modal.js";

// Admin views. Only offered when the server says this login is an admin;
// the server enforces it regardless.

function table(headers, rows, numeric = []) {
  const head = el("tr", {}, ...headers.map((h, i) => el("th", { class: numeric.includes(i) ? "num" : "", text: h })));
  const body = rows.map((cells) =>
    el(
      "tr",
      {},
      ...cells.map((c, i) => {
        const td = el("td", { class: numeric.includes(i) ? "num" : "" });
        if (c instanceof Node) td.append(c);
        else td.textContent = c ?? "";
        return td;
      }),
    ),
  );
  return el("table", { class: "files" }, el("thead", {}, head), el("tbody", {}, ...body));
}

const tag = (text, cls) => el("span", { class: cls, text });

export function renderAdminNav() {
  const show = state.me && state.me.admin;
  $("admin-h").hidden = !show;
  const ul = $("admin");
  ul.hidden = !show;
  if (!show) return;
  ul.replaceChildren(
    ...[
      ["status", "Status"],
      ["activity", "Activity"],
      ["trash", "Trash"],
    ].map(([v, label]) => el("li", { class: state.view === v ? "active" : "", onclick: () => (location.hash = "#!" + v) }, el("span", { text: label }))),
  );
}

// quiet re-renders in place, for updates the user didn't ask for.
export async function showAdmin(view, quiet = false) {
  state.view = view;
  const box = $("admin-view");
  box.hidden = false;
  if (!quiet) {
    box.replaceChildren(el("div", { class: "dim", text: "Loading\u2026" }));
    emit("ts:view");
  }
  try {
    if (view === "status") await renderStatus(box);
    if (view === "activity") await renderActivity(box);
    if (view === "trash") await renderTrash(box);
    if (!quiet) status("");
  } catch (e) {
    if (quiet) return;
    box.replaceChildren(el("div", { class: "bad", text: "Couldn't load: " + e.message }));
  }
}

async function renderStatus(box) {
  const st = await api("GET", "/api/admin/status");
  const people = st.peers
    .filter((p) => p.hasAccess || p.external)
    .map((p) => {
      let conn;
      if (!p.canReach) conn = tag(p.hasAccess ? "blocked by tailnet policy" : "blocked", "bad");
      else conn = tag(p.online ? "can connect" : "can connect (offline)", "ok");
      const access = p.hasAccess ? "yes" : el("span", {}, tag("none yet ", "dim"), el("code", { text: "id:" + p.userId }));
      return [p.login, p.node, p.external ? "shared in" : "your tailnet", access, conn];
    });
  const blocked = st.peers.some((p) => p.hasAccess && !p.canReach);
  fill(
    box,
    el("h3", { text: "People" }),
    st.policyKnown
      ? people.length
        ? table(["Person", "Device", "From", "Access", "Connection"], people)
        : el("div", { class: "dim", text: "Nobody with access and nobody has accepted a share yet." })
      : el("div", { class: "dim", text: "Policy check unavailable (dev mode)." }),
    blocked
      ? el(
          "p",
          { class: "dim" },
          "Someone with access is blocked by your tailnet policy. Add this to \"grants\" in the policy file (Tailscale admin console, Access controls): ",
          el("code", { text: st.grant || "" }),
        )
      : null,
    el("h3", { text: "Shares" }),
    table(
      ["Share", "Folder", "Free space", "In trash", "State"],
      st.shares.map((s) => [
        s.name,
        el("code", { text: s.path }),
        s.online ? fmtSize(s.freeBytes) : "",
        String(s.trashCount),
        s.online ? (s.readOnly ? "read-only" : "ok") : tag("folder unavailable", "bad"),
      ]),
      [2, 3],
    ),
    el("p", { class: "dim", text: `Uploads are refused if they would leave less than ${fmtSize(st.minFreeBytes)} free.` }),
  );
}

const fill = (box, ...nodes) => box.replaceChildren(...nodes.filter(Boolean));

function speed(e) {
  if (!e.bytes || !e.seconds) return "";
  return fmtSize(e.bytes / Math.max(e.seconds, 0.001)) + "/s";
}

async function renderActivity(box) {
  const entries = await api("GET", "/api/admin/activity?n=300");
  box.replaceChildren(
    el("div", { class: "row" }, el("button", { text: "Refresh", onclick: () => showAdmin("activity") })),
    entries.length
      ? table(
          ["When", "Who", "Action", "Share", "Path", "Size", "Speed", ""],
          entries.map((e) => [
            fmtDate(e.time),
            e.login + (e.external ? " (shared in)" : ""),
            e.action,
            e.share,
            e.to ? `${e.path} \u2192 ${e.to}` : e.path || "",
            e.bytes ? fmtSize(e.bytes) : "",
            speed(e),
            e.note || "",
          ]),
          [5, 6],
        )
      : el("div", { class: "dim", text: "No activity yet." }),
  );
}

async function renderTrash(box) {
  const st = await api("GET", "/api/admin/status");
  const sections = [];
  for (const s of st.shares.filter((x) => x.online)) {
    const items = await api("GET", `/api/admin/shares/${encodeURIComponent(s.name)}/trash`);
    sections.push(el("h3", { text: s.name }));
    if (!items.length) {
      sections.push(el("div", { class: "dim", text: "Empty." }));
      continue;
    }
    const base = `/api/admin/shares/${encodeURIComponent(s.name)}/trash/`;
    sections.push(
      table(
        ["Path", "Deleted by", "When", "Size", ""],
        items.map((it) => [
          (it.dir ? "\u25B8 " : "") + it.path,
          it.by,
          fmtDate(it.at),
          fmtSize(it.size),
          el(
            "span",
            { class: "actions" },
            el("button", {
              text: "Restore",
              onclick: async () => {
                try {
                  const r = await api("POST", base + it.id + "/restore");
                  await showAdmin("trash");
                  status(`Restored to ${s.name}/${r.path}`);
                } catch (e) {
                  status("Couldn't restore: " + e.message, true);
                }
              },
            }),
            el("button", {
              class: "danger",
              text: "Delete forever",
              onclick: async () => {
                const ok = await modal({ title: `Permanently delete ${it.path}?`, text: "This can't be undone.", ok: "Delete forever", danger: true });
                if (!ok) return;
                try {
                  await api("DELETE", base + it.id);
                  showAdmin("trash");
                } catch (e) {
                  status("Couldn't delete: " + e.message, true);
                }
              },
            }),
          ),
        ]),
        [3],
      ),
    );
  }
  box.replaceChildren(el("p", { class: "dim", text: "Deleted and replaced files are kept for 30 days." }), ...sections);
}

