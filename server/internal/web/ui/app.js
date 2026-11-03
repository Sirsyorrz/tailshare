"use strict";

const CHUNK = 16 << 20;
const PARALLEL_FILES = 2;

const $ = (id) => document.getElementById(id);

const state = {
  me: null,
  // "files", or an admin view: "status", "activity", "trash"
  view: "files",
  share: null,
  path: "",
  entries: [],
};

// Every node is built with textContent; file and share names come from other
// people and must never be parsed as HTML.
function el(tag, props = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v);
  }
  for (const c of children) if (c) n.append(c);
  return n;
}

function status(msg, bad = false) {
  const s = $("status-msg");
  s.textContent = msg;
  s.className = bad ? "bad" : "";
}

async function api(method, url, body) {
  const opts = { method, headers: {} };
  if (method !== "GET") opts.headers["X-Tailshare"] = "1";
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(url, opts);
  const data = r.status === 204 ? null : await r.json().catch(() => null);
  if (!r.ok) {
    const e = new Error((data && data.error) || r.statusText);
    e.status = r.status;
    e.data = data;
    throw e;
  }
  return data;
}

const shareURL = (name, rest) => `/api/shares/${encodeURIComponent(name)}/${rest}`;
const has = (p) => state.share && state.share.perms.includes(p);
const joinPath = (dir, name) => (dir ? dir + "/" + name : name);

// Decimal units, matching the server's messages.
function fmtSize(n) {
  if (n < 1000) return Math.round(n) + " B";
  const u = ["KB", "MB", "GB", "TB"];
  let i = -1;
  do {
    n /= 1000;
    i++;
  } while (n >= 1000 && i < u.length - 1);
  return n.toFixed(n < 10 ? 1 : 0) + " " + u[i];
}

function fmtDate(s) {
  const d = new Date(s);
  return d.toLocaleDateString() + " " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// modal resolves to the input value (or true), "alt" for the alternate
// button, or null on cancel.
function modal({ title, text = "", input = null, ok = "OK", alt = null, danger = false }) {
  return new Promise((resolve) => {
    $("modal-title").textContent = title;
    $("modal-text").textContent = text;
    const inp = $("modal-input");
    inp.hidden = input === null;
    inp.value = input ?? "";
    $("modal-ok").textContent = ok;
    $("modal-ok").className = danger ? "danger" : "";
    $("modal-alt").hidden = !alt;
    $("modal-alt").textContent = alt ?? "";
    $("modal-back").hidden = false;
    (input !== null ? inp : $("modal-ok")).focus();
    if (input !== null) {
      const dot = inp.value.lastIndexOf(".");
      inp.setSelectionRange(0, dot > 0 ? dot : inp.value.length);
    }
    const done = (v) => {
      $("modal-back").hidden = true;
      $("modal").onsubmit = $("modal-cancel").onclick = $("modal-alt").onclick = null;
      resolve(v);
    };
    $("modal").onsubmit = (e) => {
      e.preventDefault();
      done(input !== null ? inp.value.trim() || null : true);
    };
    $("modal-cancel").onclick = () => done(null);
    $("modal-alt").onclick = () => done("alt");
  });
}

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !$("modal-back").hidden) $("modal-cancel").click();
});

// Mirrors perm.Presets on the server, largest first.
const PRESETS = [
  ["editor", ["view", "download", "upload", "mkdir", "overwrite", "rename", "delete"]],
  ["contributor", ["view", "download", "upload", "mkdir"]],
  ["viewer", ["view", "download"]],
  ["upload only", ["upload"]],
];
const PERM_NAMES = {
  view: "view",
  download: "download",
  upload: "upload",
  mkdir: "new folders",
  overwrite: "replace",
  rename: "rename",
  delete: "delete",
  deleteown: "delete own",
};

function permLabel(perms) {
  const preset = PRESETS.find(([, ps]) => ps.every((p) => perms.includes(p)));
  const extras = perms.filter((p) => !(preset && preset[1].includes(p))).map((p) => PERM_NAMES[p] || p);
  if (!preset) return extras.join(", ");
  return [preset[0], ...extras].join(" + ");
}

async function refreshMe() {
  try {
    const me = await api("GET", "/api/me");
    state.me = me;
    if (state.share) state.share = me.shares.find((s) => s.name === state.share.name) || state.share;
    renderShares();
  } catch {}
}

function shareDetail(s) {
  const parts = [permLabel(s.perms)];
  if (s.quota) parts.push(`${fmtSize(s.used)} of ${fmtSize(s.quota)} used`);
  if (s.expires) parts.push("until " + new Date(new Date(s.expires).getTime() - 1).toLocaleDateString());
  return parts.join(" \u00b7 ");
}

function renderShares() {
  const ul = $("shares");
  ul.replaceChildren();
  for (const s of state.me.shares) {
    const active = state.view === "files" && state.share && state.share.name === s.name;
    ul.append(
      el(
        "li",
        { class: active ? "active" : "", onclick: () => openShare(s, "") },
        el("span", { text: s.name }),
        el("span", { class: "perms", text: shareDetail(s) }),
      ),
    );
  }
}

function renderCrumbs() {
  const nav = $("crumbs");
  nav.replaceChildren();
  if (!state.share) return;
  nav.append(el("a", { text: state.share.name, onclick: () => navigate("") }));
  let acc = "";
  for (const part of state.path ? state.path.split("/") : []) {
    acc = joinPath(acc, part);
    const target = acc;
    nav.append(el("span", { class: "sep", text: "/" }), el("a", { text: part, onclick: () => navigate(target) }));
  }
}

function renderFiles() {
  const table = $("files");
  const tbody = table.querySelector("tbody");
  tbody.replaceChildren();
  const empty = $("empty");

  if (!state.share) {
    table.hidden = true;
    empty.hidden = false;
    empty.textContent = state.me.shares.length ? "Pick a share." : "Nothing has been shared with you yet.";
    return;
  }
  if (!has("view")) {
    table.hidden = true;
    empty.hidden = false;
    empty.textContent = "Upload-only share: you can add files but not see what's here.";
    return;
  }
  table.hidden = state.entries.length === 0;
  empty.hidden = state.entries.length !== 0;
  empty.textContent = "Empty folder.";

  for (const e of state.entries) {
    const p = joinPath(state.path, e.name);
    const name = e.dir
      ? el("a", { class: "dir", text: e.name, onclick: () => navigate(p) })
      : has("download")
        ? el("a", { href: shareURL(state.share.name, "download?path=" + encodeURIComponent(p)), download: e.name, text: e.name })
        : el("span", { text: e.name });
    const actions = el("td", { class: "actions" });
    if (has("download")) {
      const href = e.dir ? zipURL(p) : shareURL(state.share.name, "download?path=" + encodeURIComponent(p));
      actions.append(el("a", { href, download: e.dir ? e.name + ".zip" : e.name, text: e.dir ? "Zip" : "Download", title: e.dir ? "Download folder as zip" : "" }));
    }
    if (has("rename")) actions.append(el("button", { text: "Rename", onclick: () => renameEntry(e) }));
    // deleteown only covers folders whose whole contents are theirs; the
    // server has the final say and reports otherwise.
    if (has("delete") || (has("deleteown") && e.mine)) {
      actions.append(el("button", { class: "danger", text: "Delete", onclick: () => deleteEntry(e) }));
    }
    tbody.append(
      el(
        "tr",
        {},
        el("td", { class: "name", title: e.name }, name),
        el("td", { class: "num", text: e.dir ? "" : fmtSize(e.size) }),
        el("td", { class: "num mod", text: fmtDate(e.modified) }),
        actions,
      ),
    );
  }
}

const zipURL = (p) => shareURL(state.share.name, "zip?path=" + encodeURIComponent(p));

function renderToolbar() {
  const files = state.view === "files";
  $("btn-upload").hidden = !files || !has("upload");
  $("btn-mkdir").hidden = !files || !has("mkdir");
  $("btn-zip").hidden = !files || !has("view") || !has("download");
}

function showFiles() {
  state.view = "files";
  $("admin-view").hidden = true;
  renderAdminNav();
}

async function openShare(s, path) {
  state.share = s;
  showFiles();
  renderShares();
  await navigate(path);
}

async function navigate(path) {
  state.path = path;
  state.entries = [];
  location.hash = "#" + encodeURIComponent(state.share.name) + (path ? "/" + path.split("/").map(encodeURIComponent).join("/") : "");
  renderCrumbs();
  renderToolbar();
  if (has("view")) {
    try {
      state.entries = await api("GET", shareURL(state.share.name, "list?path=" + encodeURIComponent(path)));
      status("");
    } catch (e) {
      status("Couldn't open folder: " + e.message, true);
    }
  }
  renderFiles();
}

const refresh = () => state.share && navigate(state.path);

async function mkdir() {
  const name = await modal({ title: "New folder", input: "", ok: "Create" });
  if (!name) return;
  try {
    await api("POST", shareURL(state.share.name, "mkdir"), { path: state.path, name });
    refresh();
  } catch (e) {
    status("Couldn't create folder: " + e.message, true);
  }
}

async function renameEntry(e) {
  const name = await modal({ title: "Rename", input: e.name, ok: "Rename" });
  if (!name || name === e.name) return;
  try {
    await api("POST", shareURL(state.share.name, "rename"), { path: joinPath(state.path, e.name), toDir: state.path, name });
    refresh();
  } catch (err) {
    status("Couldn't rename: " + err.message, true);
  }
}

async function deleteEntry(e) {
  const ok = await modal({
    title: `Delete ${e.name}?`,
    text: e.dir ? "The folder and everything in it moves to the share's trash." : "The file moves to the share's trash.",
    ok: "Delete",
    danger: true,
  });
  if (!ok) return;
  try {
    await api("POST", shareURL(state.share.name, "delete"), { path: joinPath(state.path, e.name) });
    refresh();
    refreshMe();
  } catch (err) {
    status("Couldn't delete: " + err.message, true);
  }
}

// Uploads. Each file is sent in CHUNK-sized PUTs at an explicit offset; the
// upload id is kept in localStorage so picking the same file again after a
// reload or dropped connection continues where it stopped.

const transfers = [];
let active = 0;

function resumeKey(t) {
  return ["ts-up", t.share, t.dir, t.file.name, t.file.size, t.file.lastModified].join("|");
}

function renderTransfers() {
  const ul = $("transfers");
  ul.replaceChildren();
  for (const t of transfers) {
    const pct = t.file.size ? Math.floor((t.offset / t.file.size) * 100) : 100;
    const bar = el("div", { class: "bar" + (t.state === "done" ? " ok" : t.state === "failed" ? " bad" : "") }, el("div"));
    bar.firstChild.style.width = pct + "%";
    let line = t.state;
    if (t.state === "uploading") line = `${pct}%  ${fmtSize(t.offset)} / ${fmtSize(t.file.size)}${t.rate ? "  " + fmtSize(t.rate) + "/s" : ""}`;
    if (t.state === "done") line = t.finalName !== t.file.name ? "done, saved as " + t.finalName : "done";
    if (t.state === "failed") line = "failed: " + t.error;
    const btn =
      t.state === "uploading" || t.state === "queued"
        ? el("button", { text: "Cancel", onclick: () => cancelTransfer(t) })
        : t.state === "failed"
          ? el("button", { text: "Retry", onclick: () => retryTransfer(t) })
          : el("button", { text: "Clear", onclick: () => clearTransfer(t) });
    ul.append(el("li", {}, el("div", { class: "tname" }, el("span", { text: t.file.name, title: t.file.name }), btn), bar, el("div", { class: "dim", text: line })));
  }
  $("transfers-h").hidden = transfers.length === 0;
  const running = transfers.filter((t) => t.state === "uploading" || t.state === "queued").length;
  document.title = running ? `(${running}) tailshare` : "tailshare";
}

async function enqueue(fileList) {
  if (!state.share || !has("upload")) return;
  const files = [...fileList];
  const share = state.share.name;
  const dir = state.path;
  let replace = false;
  if (has("overwrite")) {
    const existing = new Set(state.entries.filter((e) => !e.dir).map((e) => e.name));
    const clashes = files.filter((f) => existing.has(f.name));
    if (clashes.length) {
      const choice = await modal({
        title: clashes.length === 1 ? `${clashes[0].name} already exists` : `${clashes.length} files already exist`,
        text: "Replace (old versions go to trash) or keep both?",
        ok: "Keep both",
        alt: "Replace",
      });
      if (choice === null) return;
      replace = choice === "alt";
    }
  }
  for (const file of files) {
    transfers.push({ file, share, dir, offset: 0, state: "queued", replace });
  }
  renderTransfers();
  pump();
}

function pump() {
  while (active < PARALLEL_FILES) {
    const t = transfers.find((x) => x.state === "queued");
    if (!t) return;
    active++;
    t.state = "uploading";
    runUpload(t)
      .catch((e) => {
        if (t.state !== "cancelled") {
          t.state = "failed";
          t.error = e.message;
        }
      })
      .finally(() => {
        active--;
        renderTransfers();
        pump();
      });
  }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function startOrResume(t) {
  const saved = localStorage.getItem(resumeKey(t));
  if (saved) {
    try {
      const st = await api("GET", shareURL(t.share, "uploads/" + saved));
      t.id = saved;
      t.offset = st.offset;
      return;
    } catch (e) {
      if (e.status !== 404) throw e;
    }
  }
  const st = await api("POST", shareURL(t.share, "uploads"), { dir: t.dir, name: t.file.name, size: t.file.size });
  t.id = st.id;
  t.offset = 0;
  localStorage.setItem(resumeKey(t), t.id);
}

function putChunk(t, blob) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    t.xhr = xhr;
    const base = t.offset;
    const started = performance.now();
    xhr.open("PUT", shareURL(t.share, `uploads/${t.id}?offset=${base}`));
    xhr.setRequestHeader("X-Tailshare", "1");
    xhr.upload.onprogress = (e) => {
      t.offset = base + e.loaded;
      const secs = (performance.now() - started) / 1000;
      if (secs > 0.5) t.rate = e.loaded / secs;
      renderTransfers();
    };
    xhr.onload = () => {
      let data = null;
      try {
        data = JSON.parse(xhr.responseText);
      } catch {}
      if (xhr.status === 200) return resolve(data.offset);
      const e = new Error((data && data.error) || "HTTP " + xhr.status);
      e.status = xhr.status;
      e.data = data;
      reject(e);
    };
    xhr.onerror = () => reject(Object.assign(new Error("network error"), { network: true }));
    xhr.onabort = () => reject(Object.assign(new Error("cancelled"), { aborted: true }));
    xhr.send(blob);
  });
}

async function runUpload(t) {
  await startOrResume(t);
  let failures = 0;
  while (t.offset < t.file.size) {
    if (t.state === "cancelled") return;
    try {
      t.offset = await putChunk(t, t.file.slice(t.offset, Math.min(t.offset + CHUNK, t.file.size)));
      failures = 0;
    } catch (e) {
      if (e.aborted) return;
      if (e.status === 409 && e.data && typeof e.data.offset === "number") {
        t.offset = e.data.offset;
        continue;
      }
      if (!e.network && e.status < 500) throw e;
      if (++failures > 30) throw e;
      t.rate = 0;
      renderTransfers();
      await sleep(Math.min(30000, 1000 * 2 ** Math.min(failures, 5)));
      const st = await api("GET", shareURL(t.share, "uploads/" + t.id)).catch(() => null);
      if (st) t.offset = st.offset;
    }
  }
  if (t.state === "cancelled") return;

  const res = await api("POST", shareURL(t.share, `uploads/${t.id}/complete`), { replace: t.replace });
  localStorage.removeItem(resumeKey(t));
  t.finalName = res.name;
  t.state = "done";
  refreshMe();
  if (state.share && state.share.name === t.share && state.path === t.dir) refresh();
}

function cancelTransfer(t) {
  const wasRunning = t.state === "uploading";
  t.state = "cancelled";
  if (t.xhr) t.xhr.abort();
  if (t.id) {
    api("DELETE", shareURL(t.share, "uploads/" + t.id)).catch(() => {});
    localStorage.removeItem(resumeKey(t));
  }
  clearTransfer(t);
  if (!wasRunning) pump();
}

function retryTransfer(t) {
  t.state = "queued";
  t.error = "";
  renderTransfers();
  pump();
}

function clearTransfer(t) {
  const i = transfers.indexOf(t);
  if (i >= 0) transfers.splice(i, 1);
  renderTransfers();
}

window.addEventListener("beforeunload", (e) => {
  if (transfers.some((t) => t.state === "uploading" || t.state === "queued")) e.preventDefault();
});

function setupDrop() {
  const main = $("main");
  const zone = $("dropzone");
  let depth = 0;
  const isFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes("Files");
  main.addEventListener("dragenter", (e) => {
    if (!isFiles(e) || !has("upload")) return;
    e.preventDefault();
    depth++;
    zone.hidden = false;
  });
  main.addEventListener("dragover", (e) => {
    if (isFiles(e) && has("upload")) e.preventDefault();
  });
  main.addEventListener("dragleave", () => {
    if (--depth <= 0) {
      depth = 0;
      zone.hidden = true;
    }
  });
  main.addEventListener("drop", (e) => {
    depth = 0;
    zone.hidden = true;
    if (!isFiles(e) || !has("upload")) return;
    e.preventDefault();
    enqueue(e.dataTransfer.files);
  });
}

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

function renderAdminNav() {
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
    ].map(([v, label]) => el("li", { class: state.view === v ? "active" : "", onclick: () => showAdmin(v) }, el("span", { text: label }))),
  );
}

async function showAdmin(view) {
  state.view = view;
  $("files").hidden = true;
  $("empty").hidden = true;
  const box = $("admin-view");
  box.hidden = false;
  box.replaceChildren(el("div", { class: "dim", text: "Loading\u2026" }));
  $("crumbs").replaceChildren(el("span", { text: { status: "Status", activity: "Activity", trash: "Trash" }[view] }));
  renderToolbar();
  renderShares();
  renderAdminNav();
  try {
    if (view === "status") await renderStatus(box);
    if (view === "activity") await renderActivity(box);
    if (view === "trash") await renderTrash(box);
    status("");
  } catch (e) {
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
          "Someone with access is blocked. Add a grant to your tailnet policy: ",
          el("code", { text: '{"src": ["autogroup:shared"], "dst": ["<this node>"], "ip": ["443"]}' }),
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

async function init() {
  $("btn-upload").onclick = () => $("file-input").click();
  $("file-input").onchange = (e) => {
    enqueue(e.target.files);
    e.target.value = "";
  };
  $("btn-mkdir").onclick = mkdir;
  $("btn-zip").onclick = () => {
    const a = el("a", { href: zipURL(state.path), download: "" });
    a.click();
  };
  setupDrop();

  try {
    state.me = await api("GET", "/api/me");
  } catch (e) {
    status("Couldn't reach tailshare: " + e.message, true);
    return;
  }
  const u = state.me.user;
  $("whoami").textContent = (u.display && u.display !== u.login ? `${u.display} (${u.login})` : u.login) + ` on ${u.node}`;
  renderShares();
  renderAdminNav();

  const [shareName, ...rest] = decodeURIComponent(location.hash.slice(1)).split("/");
  const s = state.me.shares.find((x) => x.name === shareName) || (state.me.shares.length === 1 ? state.me.shares[0] : null);
  if (s) await openShare(s, s.name === shareName ? rest.filter(Boolean).join("/") : "");
  else renderFiles();
}

init();
