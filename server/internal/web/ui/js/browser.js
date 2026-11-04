import {
  $,
  el,
  icon,
  api,
  shareURL,
  joinPath,
  parentOf,
  fmtSize,
  fmtDate,
  status,
  kindOf,
  typeLabel,
  hasThumb,
  previewKind,
} from "./util.js";
import { state, has, savePrefs, emit, on } from "./state.js";
import { modal, contextMenu, pickFolder, modalOpen, menuOpen } from "./modal.js";
import { enqueue, fromDataTransfer, fromFolderInput } from "./upload.js";
import { openLightbox } from "./lightbox.js";

const FILTERS = [
  ["all", "All"],
  ["folder", "Folders"],
  ["image", "Images"],
  ["video", "Videos"],
  ["audio", "Audio"],
  ["doc", "Documents"],
  ["archive", "Archives"],
  ["other", "Other"],
];

const SORTS = [
  ["name", "Name"],
  ["modified", "Date modified"],
  ["type", "Type"],
  ["size", "Size"],
];

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });
const coarse = matchMedia("(pointer: coarse)");

// Items currently shown, in order; rows maps path -> element for cheap
// selection updates without re-rendering.
let shown = [];
let rows = new Map();

const itemsFromEntries = () =>
  state.entries.map((e) => ({ ...e, path: joinPath(state.path, e.name), kind: kindOf(e) }));

let base = [];

function visible() {
  const q = state.query.trim().toLowerCase();
  let list = base;
  if (q && !state.hits) list = list.filter((e) => e.name.toLowerCase().includes(q));
  if (state.filter !== "all") list = list.filter((e) => e.kind === state.filter);
  const { sortKey: k, sortDir: d } = state.prefs;
  const cmp = {
    name: (a, b) => collator.compare(a.name, b.name),
    modified: (a, b) => Date.parse(a.modified) - Date.parse(b.modified),
    size: (a, b) => a.size - b.size,
    type: (a, b) => collator.compare(typeLabel(a), typeLabel(b)) || collator.compare(a.name, b.name),
  }[k];
  // Folders first, like Explorer, whatever the sort.
  return [...list].sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : d * cmp(a, b) || collator.compare(a.name, b.name)));
}

const thumbURL = (e) => shareURL(state.share.name, `thumb?path=${encodeURIComponent(e.path)}&v=${Date.parse(e.modified)}`);
const downloadURL = (e) => shareURL(state.share.name, "download?path=" + encodeURIComponent(e.path));
const zipURL = (paths) => shareURL(state.share.name, "zip?" + paths.map((p) => "path=" + encodeURIComponent(p)).join("&"));

function canDelete(e) {
  return has("delete") || (has("deleteown") && e.mine);
}

// Navigation goes through the URL hash so the browser's back/forward
// buttons work; the hashchange handler in main.js calls load().
export function navigate(path) {
  const h = "#" + encodeURIComponent(state.share.name) + (path ? "/" + path.split("/").map(encodeURIComponent).join("/") : "");
  if (location.hash === h) load(path);
  else location.hash = h;
}

export async function load(path, keepSelection = false) {
  const changedDir = path !== state.path;
  state.path = path;
  if (changedDir) {
    state.query = "";
    $("search").value = "";
    state.hits = null;
    state.filter = "all";
  }
  if (!keepSelection || changedDir) state.selected.clear();
  state.focus = null;
  state.loading = true;
  renderChrome();
  if (has("view")) {
    try {
      state.entries = (await listFolder(state.share.name, path)).entries;
      if ($("status-msg").className === "bad") status("");
    } catch (e) {
      state.entries = [];
      status("Couldn't open folder: " + e.message, true);
    }
  } else {
    state.entries = [];
  }
  state.loading = false;
  if (!state.hits) base = itemsFromEntries();
  for (const p of [...state.selected]) if (!base.some((e) => e.path === p)) state.selected.delete(p);
  render();
}

export const refresh = () => state.share && load(state.path, true);

// The last listing's ETag, so an unchanged folder costs the server a 304.
let listed = { key: "", etag: "" };

// Resolves to null when the folder hasn't changed since the last listing.
async function listFolder(share, path, onlyIfChanged = false) {
  const key = share + "\0" + path;
  const headers = onlyIfChanged && listed.key === key ? { "If-None-Match": listed.etag } : {};
  const r = await fetch(shareURL(share, "list?path=" + encodeURIComponent(path)), { headers });
  if (r.status === 304) return null;
  const data = await r.json().catch(() => null);
  if (!r.ok) {
    const e = new Error((data && data.error) || r.statusText);
    e.status = r.status;
    throw e;
  }
  listed = { key, etag: r.headers.get("ETag") || "" };
  return { entries: data };
}

// Changes made elsewhere (drives, other people, files added on the server's
// disk) can't be pushed to the page, so the open folder is re-checked while
// the tab is visible. Skipped while the user is mid-action, since a
// re-render would close what they're doing.
const POLL_MS = 3000;
let dragging = false;
let polling = false;

async function poll() {
  if (polling || document.hidden || state.view !== "files" || !state.share || !has("view")) return;
  if (state.hits || state.loading || dragging || modalOpen() || menuOpen() || !$("dropzone").hidden) return;
  const share = state.share.name;
  const path = state.path;
  polling = true;
  try {
    const r = await listFolder(share, path, true);
    if (!r || !state.share || state.share.name !== share || state.path !== path || state.loading || state.hits) return;
    state.entries = r.entries;
    base = itemsFromEntries();
    for (const p of [...state.selected]) if (!base.some((e) => e.path === p)) state.selected.delete(p);
    if (state.focus && !base.some((e) => e.path === state.focus)) state.focus = null;
    render();
  } catch {
    // Offline for a moment; the next tick tries again.
  } finally {
    polling = false;
  }
}

// On document, not the dragged row: a drop can re-render the row away.
document.addEventListener("dragend", () => (dragging = false), true);
document.addEventListener("drop", () => (dragging = false), true);
setInterval(poll, POLL_MS);
document.addEventListener("visibilitychange", poll);
window.addEventListener("focus", poll);

on("ts:changed", (d) => {
  if (state.share && d.share === state.share.name && d.dir === state.path && !state.hits) refresh();
});

async function deepSearch() {
  const q = state.query.trim();
  if (!q) return;
  status("Searching\u2026");
  try {
    const r = await api("GET", shareURL(state.share.name, `search?path=${encodeURIComponent(state.path)}&q=${encodeURIComponent(q)}`));
    state.hits = r.hits;
    state.truncated = r.truncated;
    base = r.hits.map((h) => ({ ...h, kind: kindOf(h) }));
    state.selected.clear();
    status("");
    render();
  } catch (e) {
    status("Search failed: " + e.message, true);
  }
}

function endSearch() {
  state.hits = null;
  state.query = "";
  $("search").value = "";
  base = itemsFromEntries();
  state.selected.clear();
  render();
}

// Rendering

export function render() {
  renderChrome();
  renderList();
}

function renderChrome() {
  const files = state.view === "files" && !!state.share;
  $("browser").hidden = state.view !== "files";
  $("btn-up").disabled = !files || state.path === "";
  $("btn-back").disabled = history.length < 2;
  $("search-box").hidden = !files || !has("view");
  $("search").placeholder = state.share ? `Search ${state.path ? state.path.split("/").pop() : state.share.name}` : "Search";
  renderCrumbs();
  if (!files) return;

  const sel = selectedItems();
  $("tb-normal").hidden = sel.length > 0;
  $("tb-select").hidden = sel.length === 0;
  $("btn-mkdir").hidden = !has("mkdir") || !!state.hits;
  $("btn-upload").hidden = !has("upload") || !!state.hits;
  $("btn-dlall").hidden = !has("download") || !has("view") || !!state.hits || base.length === 0;
  if (sel.length) {
    const size = sel.reduce((n, e) => n + (e.dir ? 0 : e.size), 0);
    $("sel-count").textContent = `${sel.length} selected` + (size ? ` (${fmtSize(size)})` : "");
    $("btn-dl").hidden = !has("download");
    $("btn-rename").hidden = !has("rename") || sel.length !== 1;
    $("btn-move").hidden = !has("rename");
    $("btn-delete").hidden = !sel.every(canDelete);
  }

  const view = has("view");
  $("view-controls").hidden = !view;
  $("btn-details").classList.toggle("on", state.prefs.layout === "details");
  $("btn-grid").classList.toggle("on", state.prefs.layout === "grid");
  $("sort").value = state.prefs.sortKey;
  $("sort-dir").replaceChildren(icon(state.prefs.sortDir > 0 ? "up" : "down"));
  $("sort-dir").title = state.prefs.sortDir > 0 ? "Ascending" : "Descending";

  const counts = {};
  const q = state.query.trim().toLowerCase();
  for (const e of base) if (!q || state.hits || e.name.toLowerCase().includes(q)) counts[e.kind] = (counts[e.kind] || 0) + 1;
  const total = Object.values(counts).reduce((a, b) => a + b, 0);
  $("chips").replaceChildren(
    ...FILTERS.filter(([k]) => k === "all" || counts[k] || state.filter === k).map(([k, label]) =>
      el(
        "button",
        {
          class: "chip" + (state.filter === k ? " on" : ""),
          onclick: () => {
            state.filter = state.filter === k ? "all" : k;
            state.selected.clear();
            render();
          },
        },
        label,
        el("span", { class: "n", text: String(k === "all" ? total : counts[k] || 0) }),
      ),
    ),
  );
  $("chips").hidden = !view || total === 0;

  const banner = $("banner");
  if (state.hits) {
    banner.hidden = false;
    banner.replaceChildren(
      ...[
      el("span", {
        text: `${state.hits.length}${state.truncated ? "+" : ""} result${state.hits.length === 1 ? "" : "s"} for \u201c${state.query.trim()}\u201d in ${state.path ? state.path : state.share.name} and subfolders`,
      }),
      state.truncated ? el("span", { class: "dim", text: " (stopped early, narrow the search)" }) : null,
      el("span", { class: "spacer" }),
      el("button", { text: "Back to folder", onclick: endSearch }),
      ].filter(Boolean),
    );
  } else if (state.query.trim()) {
    banner.hidden = false;
    banner.replaceChildren(
      el("span", { class: "dim", text: "Filtering this folder. " }),
      el("button", { class: "link", text: "Search subfolders too", onclick: deepSearch }),
      el("span", { class: "dim", text: " (or press Enter)" }),
    );
  } else {
    banner.hidden = true;
  }
}

function renderCrumbs() {
  const nav = $("crumbs");
  nav.replaceChildren();
  if (state.view !== "files") {
    nav.append(el("span", { class: "crumb-current", text: { status: "Status", activity: "Activity", trash: "Trash" }[state.view] }));
    return;
  }
  if (!state.share) return;
  const parts = state.path ? state.path.split("/") : [];
  const crumb = (label, target, last) => {
    const a = el("a", { class: last ? "crumb-current" : "", text: label, title: label, onclick: () => navigate(target) });
    dropTarget(a, target);
    return a;
  };
  nav.append(crumb(state.share.name, "", parts.length === 0));
  let acc = "";
  parts.forEach((part, i) => {
    acc = joinPath(acc, part);
    nav.append(el("span", { class: "sep", text: "\u203a" }), crumb(part, acc, i === parts.length - 1));
  });
}

function renderList() {
  const list = $("list");
  const empty = $("empty");
  rows = new Map();
  if (!state.share || state.view !== "files") {
    list.replaceChildren();
    empty.hidden = !!state.share || state.view !== "files";
    if (!state.share) {
      empty.replaceChildren(
        el("div", { class: "empty-title", text: state.me && state.me.shares.length ? "Pick a share" : "Nothing has been shared with you yet" }),
        el("div", { class: "dim", text: state.me && state.me.shares.length ? "Your shares are listed on the left." : "Ask the owner to give you access." }),
      );
    }
    updateCounts();
    return;
  }
  if (!has("view")) {
    list.replaceChildren();
    empty.hidden = false;
    empty.replaceChildren(
      icon("upload", "icon big"),
      el("div", { class: "empty-title", text: "Upload-only share" }),
      el("div", { class: "dim", text: "Drop files here or use Upload. You can't see what's already in this folder." }),
    );
    updateCounts();
    return;
  }

  shown = visible();
  empty.hidden = shown.length > 0 || state.loading;
  if (!shown.length) {
    const filtered = state.filter !== "all" || state.query.trim();
    empty.replaceChildren(
      el("div", { class: "empty-title", text: state.hits ? "No results" : filtered ? "Nothing matches" : "This folder is empty" }),
      el(
        "div",
        { class: "dim" },
        state.hits ? "Try a shorter or different search." : filtered ? "Clear the search or filter to see everything." : has("upload") ? "Drop files or folders here to upload." : "",
      ),
    );
  }
  list.className = "list layout-" + state.prefs.layout;
  list.replaceChildren(state.prefs.layout === "grid" ? renderGrid() : renderDetails());
  updateCounts();
}

function sortHeader(key, label, cls = "") {
  const on = state.prefs.sortKey === key;
  return el(
    "th",
    {
      class: cls + " sortable" + (on ? " sorted" : ""),
      onclick: () => setSort(key),
      title: "Sort by " + label.toLowerCase(),
    },
    label,
    on ? el("span", { class: "arrow", text: state.prefs.sortDir > 0 ? "\u25b2" : "\u25bc" }) : null,
  );
}

function setSort(key) {
  if (state.prefs.sortKey === key) state.prefs.sortDir *= -1;
  else {
    state.prefs.sortKey = key;
    // Newest and biggest first is what people want when they pick those.
    state.prefs.sortDir = key === "modified" || key === "size" ? -1 : 1;
  }
  savePrefs();
  render();
}

function checkbox(e) {
  return el("input", {
    type: "checkbox",
    class: "sel",
    "aria-label": "Select " + e.name,
    onclick: (ev) => {
      ev.stopPropagation();
      toggle(e, ev.shiftKey);
    },
  });
}

function renderDetails() {
  const allSel = shown.length > 0 && shown.every((e) => state.selected.has(e.path));
  const head = el(
    "tr",
    {},
    el(
      "th",
      { class: "c-sel" },
      el("input", {
        type: "checkbox",
        class: "sel",
        "aria-label": "Select all",
        checked: allSel,
        onclick: () => (allSel ? clearSelection() : selectAll()),
      }),
    ),
    sortHeader("name", "Name", "c-name"),
    state.hits ? el("th", { class: "c-loc", text: "Location" }) : null,
    sortHeader("modified", "Date modified", "c-mod"),
    sortHeader("type", "Type", "c-type"),
    sortHeader("size", "Size", "c-size num"),
    el("th", { class: "c-more" }),
  );
  const body = el("tbody");
  for (const e of shown) {
    const tr = el(
      "tr",
      { "data-path": e.path },
      el("td", { class: "c-sel" }, checkbox(e)),
      el(
        "td",
        { class: "c-name", title: e.name },
        el("span", { class: "name-cell" }, icon(e.kind === "other" ? "file" : e.kind, "icon " + e.kind), el("span", { class: "name", text: e.name })),
      ),
      state.hits
        ? el(
            "td",
            { class: "c-loc" },
            el("a", {
              text: "/" + parentOf(e.path),
              onclick: (ev) => {
                ev.stopPropagation();
                navigate(parentOf(e.path));
              },
            }),
          )
        : null,
      el("td", { class: "c-mod", text: fmtDate(e.modified) }),
      el("td", { class: "c-type", text: typeLabel(e) }),
      el("td", { class: "c-size num", text: e.dir ? "" : fmtSize(e.size) }),
      el("td", { class: "c-more" }, moreButton(e)),
    );
    wireItem(tr, e);
    body.append(tr);
  }
  return el("table", { class: "details" }, el("thead", {}, head), body);
}

function renderGrid() {
  const grid = el("div", { class: "grid" });
  for (const e of shown) {
    const pic = el("div", { class: "pic" });
    if (hasThumb(e) && has("download")) {
      const img = el("img", { src: thumbURL(e), alt: "", loading: "lazy", decoding: "async", draggable: "false" });
      img.addEventListener("error", () => pic.replaceChildren(icon(e.kind, "icon big " + e.kind)));
      img.addEventListener("load", () => pic.classList.add("loaded"));
      pic.append(img);
      if (e.kind === "video") pic.append(el("span", { class: "badge", text: "\u25b6" }));
    } else {
      pic.append(icon(e.kind === "other" ? "file" : e.kind, "icon big " + e.kind));
    }
    const tile = el(
      "div",
      { class: "tile", "data-path": e.path, title: e.name },
      checkbox(e),
      pic,
      el("div", { class: "tname", text: e.name }),
      el("div", { class: "tmeta", text: state.hits ? "/" + parentOf(e.path) : e.dir ? fmtDate(e.modified) : fmtSize(e.size) }),
    );
    wireItem(tile, e);
    grid.append(tile);
  }
  return grid;
}

function moreButton(e) {
  return el(
    "button",
    {
      class: "icon-btn more",
      title: "More",
      "aria-label": "More actions for " + e.name,
      onclick: (ev) => {
        ev.stopPropagation();
        if (!state.selected.has(e.path)) selectOnly(e);
        const r = ev.currentTarget.getBoundingClientRect();
        itemMenu(r.left, r.bottom);
      },
    },
    icon("more"),
  );
}

function wireItem(node, e) {
  rows.set(e.path, node);
  paintSelection(node, e);
  node.addEventListener("click", (ev) => {
    if (ev.target.closest("a, button, input")) return;
    // Touch: a tap opens, unless the user is already picking items.
    if (coarse.matches && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey) {
      if (state.selected.size) toggle(e);
      else open(e);
      return;
    }
    if (ev.shiftKey) rangeTo(e, ev.ctrlKey || ev.metaKey);
    else if (ev.ctrlKey || ev.metaKey) toggle(e);
    else selectOnly(e);
  });
  node.addEventListener("dblclick", (ev) => {
    if (ev.target.closest("a, button, input")) return;
    open(e);
  });
  node.addEventListener("contextmenu", (ev) => {
    ev.preventDefault();
    if (!state.selected.has(e.path)) selectOnly(e);
    itemMenu(ev.clientX, ev.clientY);
  });

  if (has("rename")) {
    node.draggable = true;
    node.addEventListener("dragstart", (ev) => {
      if (!state.selected.has(e.path)) selectOnly(e);
      ev.dataTransfer.setData("application/x-tailshare", JSON.stringify([...state.selected]));
      ev.dataTransfer.effectAllowed = "move";
      dragging = true;
    });

  }
  if (e.dir) dropTarget(node, e.path);
}

// Folders, and the breadcrumbs, accept items dragged from the list.
function dropTarget(node, dest) {
  if (!has("rename")) return;
  const ours = (ev) => [...ev.dataTransfer.types].includes("application/x-tailshare");
  node.addEventListener("dragover", (ev) => {
    if (!ours(ev)) return;
    ev.preventDefault();
    ev.dataTransfer.dropEffect = "move";
    node.classList.add("drop-on");
  });
  node.addEventListener("dragleave", () => node.classList.remove("drop-on"));
  node.addEventListener("drop", (ev) => {
    node.classList.remove("drop-on");
    if (!ours(ev)) return;
    ev.preventDefault();
    ev.stopPropagation();
    const paths = JSON.parse(ev.dataTransfer.getData("application/x-tailshare") || "[]");
    move(paths, dest);
  });
}

// Selection

export function selectedItems() {
  return base.filter((e) => state.selected.has(e.path));
}

function paintSelection(node, e) {
  const on = state.selected.has(e.path);
  node.classList.toggle("selected", on);
  node.classList.toggle("focused", state.focus === e.path);
  const cb = node.querySelector("input.sel");
  if (cb) cb.checked = on;
}

function selectionChanged() {
  for (const e of shown) {
    const n = rows.get(e.path);
    if (n) paintSelection(n, e);
  }
  const head = $("list").querySelector("thead input.sel");
  if (head) head.checked = shown.length > 0 && shown.every((e) => state.selected.has(e.path));
  renderChrome();
  updateCounts();
}

function selectOnly(e) {
  state.selected = new Set([e.path]);
  state.anchor = state.focus = e.path;
  selectionChanged();
}

function toggle(e, range = false) {
  if (range) return rangeTo(e, true);
  if (state.selected.has(e.path)) state.selected.delete(e.path);
  else state.selected.add(e.path);
  state.anchor = state.focus = e.path;
  selectionChanged();
}

function rangeTo(e, add) {
  const a = shown.findIndex((x) => x.path === state.anchor);
  const b = shown.indexOf(e);
  if (a < 0) return selectOnly(e);
  if (!add) state.selected.clear();
  for (let i = Math.min(a, b); i <= Math.max(a, b); i++) state.selected.add(shown[i].path);
  state.focus = e.path;
  selectionChanged();
}

export function selectAll() {
  state.selected = new Set(shown.map((e) => e.path));
  selectionChanged();
}

export function clearSelection() {
  if (!state.selected.size) return;
  state.selected.clear();
  selectionChanged();
}

function updateCounts() {
  const c = $("counts");
  if (!state.share || state.view !== "files" || !has("view")) {
    c.textContent = "";
    return;
  }
  const n = shown.length;
  const sel = selectedItems();
  c.textContent = `${n} item${n === 1 ? "" : "s"}` + (sel.length ? ` \u00b7 ${sel.length} selected` : "");
}

// Keyboard: arrows move focus (shift extends), Enter opens, Backspace goes
// up, Delete/F2 act on the selection.
export function onKey(ev) {
  if (state.view !== "files" || !state.share) return false;
  const k = ev.key;
  const mod = ev.ctrlKey || ev.metaKey;
  if (mod && k.toLowerCase() === "a") return selectAll(), true;
  if (k === "Escape") return clearSelection(), true;
  if (k === "Backspace" || (ev.altKey && k === "ArrowUp")) return up(), true;
  if (k === "Delete") return deleteItems(selectedItems()), true;
  if (k === "F2") {
    const s = selectedItems();
    if (s.length === 1 && has("rename")) renameItem(s[0]);
    return true;
  }
  if (k === "Enter") {
    const s = selectedItems();
    if (s.length === 1) open(s[0]);
    return true;
  }
  const grid = state.prefs.layout === "grid";
  const dirs = { ArrowDown: 1, ArrowUp: -1, ...(grid ? { ArrowRight: 1, ArrowLeft: -1 } : {}), Home: -1e9, End: 1e9 };
  if (!(k in dirs) || !shown.length) return false;
  let step = dirs[k];
  if (grid && (k === "ArrowDown" || k === "ArrowUp")) step *= gridColumns();
  const cur = shown.findIndex((e) => e.path === state.focus);
  const next = shown[Math.max(0, Math.min(shown.length - 1, cur < 0 ? 0 : cur + step))];
  if (ev.shiftKey) rangeTo(next, mod);
  else if (mod) {
    state.focus = next.path;
    selectionChanged();
  } else selectOnly(next);
  rows.get(next.path)?.scrollIntoView({ block: "nearest" });
  return true;
}

function gridColumns() {
  const tiles = $("list").querySelectorAll(".tile");
  if (tiles.length < 2) return 1;
  const top = tiles[0].offsetTop;
  let n = 0;
  while (n < tiles.length && tiles[n].offsetTop === top) n++;
  return Math.max(1, n);
}

// Actions

export function up() {
  if (state.hits) return endSearch();
  if (state.path) navigate(parentOf(state.path));
}

function open(e) {
  if (e.dir) return navigate(e.path);
  if (!has("download")) return;
  if (previewKind(e)) return openLightbox(shown, e);
  download([e]);
}

export function download(items) {
  if (!items.length || !has("download")) return;
  const href = items.length === 1 && !items[0].dir ? downloadURL(items[0]) : zipURL(items.map((e) => e.path));
  el("a", { href, download: "" }).click();
}

async function renameItem(e) {
  const name = await modal({ title: "Rename", input: e.name, ok: "Rename" });
  if (!name || name === e.name) return;
  try {
    await api("POST", shareURL(state.share.name, "rename"), { path: e.path, toDir: parentOf(e.path), name });
    if (state.hits) await deepSearch();
    else await refresh();
    status(`Renamed to ${name}`);
  } catch (err) {
    status("Couldn't rename: " + err.message, true);
  }
}

async function move(paths, dest) {
  paths = paths.filter((p) => parentOf(p) !== dest && dest !== p && !dest.startsWith(p + "/"));
  if (!paths.length) return;
  let ok = 0;
  for (const p of paths) {
    try {
      await api("POST", shareURL(state.share.name, "rename"), { path: p, toDir: dest, name: p.split("/").pop() });
      ok++;
    } catch (err) {
      status(`Couldn't move ${p.split("/").pop()}: ${err.message}`, true);
    }
  }
  if (ok) status(`Moved ${ok} item${ok === 1 ? "" : "s"} to /${dest}`);
  if (state.hits) await deepSearch();
  else await load(state.path);
}

async function moveItems(items) {
  const dirs = items.filter((e) => e.dir).map((e) => e.path);
  const dest = await pickFolder(state.share.name, state.path, `Move ${items.length === 1 ? items[0].name : items.length + " items"} to`, dirs);
  if (dest !== null) await move(items.map((e) => e.path), dest);
}

async function deleteItems(items) {
  items = items.filter(canDelete);
  if (!items.length) return;
  const one = items.length === 1;
  const ok = await modal({
    title: one ? `Delete ${items[0].name}?` : `Delete ${items.length} items?`,
    text: "They move to the share's trash, where the owner can restore them for 30 days.",
    ok: "Delete",
    danger: true,
  });
  if (!ok) return;
  let done = 0;
  for (const e of items) {
    try {
      await api("POST", shareURL(state.share.name, "delete"), { path: e.path });
      done++;
    } catch (err) {
      status(`Couldn't delete ${e.name}: ${err.message}`, true);
    }
  }
  if (done) status(`Deleted ${done} item${done === 1 ? "" : "s"}`);
  emit("ts:me");
  if (state.hits) await deepSearch();
  else await load(state.path);
}

export async function mkdir() {
  const name = await modal({ title: "New folder", input: "New folder", ok: "Create" });
  if (!name) return;
  try {
    await api("POST", shareURL(state.share.name, "mkdir"), { path: state.path, name });
    await refresh();
    const made = base.find((e) => e.name === name);
    if (made) {
      selectOnly(made);
      rows.get(made.path)?.scrollIntoView({ block: "nearest" });
    }
  } catch (e) {
    status("Couldn't create folder: " + e.message, true);
  }
}

function itemMenu(x, y) {
  const sel = selectedItems();
  if (!sel.length) return;
  const one = sel.length === 1 ? sel[0] : null;
  const items = [];
  if (one) {
    if (one.dir) items.push({ label: "Open", icon: "folder", onclick: () => open(one) });
    else if (previewKind(one) && has("download")) items.push({ label: "Preview", icon: "image", onclick: () => open(one) });
    if (state.hits) items.push({ label: "Open file location", icon: "folder", onclick: () => navigate(parentOf(one.path)) });
  }
  if (has("download"))
    items.push({
      label: one && !one.dir ? "Download" : "Download as zip",
      icon: "download",
      onclick: () => download(sel),
    });
  if (has("rename")) {
    if (one) items.push({ label: "Rename", icon: "rename", onclick: () => renameItem(one) });
    items.push({ label: "Move to\u2026", icon: "move", onclick: () => moveItems(sel) });
  }
  if (sel.every(canDelete)) items.push("-", { label: "Delete", icon: "trash", danger: true, onclick: () => deleteItems(sel) });
  if (items.length) contextMenu(items, x, y);
}

function backgroundMenu(x, y) {
  const items = [];
  if (has("mkdir") && !state.hits) items.push({ label: "New folder", icon: "newfolder", onclick: mkdir });
  if (has("upload") && !state.hits) {
    items.push({ label: "Upload files", icon: "upload", onclick: () => $("file-input").click() });
    items.push({ label: "Upload a folder", icon: "upload", onclick: () => $("folder-input").click() });
  }
  if (has("view")) {
    if (items.length) items.push("-");
    items.push({ label: "Select all", icon: "list", onclick: selectAll });
    items.push({ label: "Refresh", icon: "refresh", onclick: refresh });
  }
  if (items.length) contextMenu(items, x, y);
}

export function setupBrowser() {
  const btn = (id, ic, label) => {
    const b = $(id);
    b.replaceChildren(icon(ic), el("span", { class: "label", text: label }));
    return b;
  };
  $("btn-back").append(icon("back"));
  $("btn-up").append(icon("up"));
  $("search-box").prepend(icon("search"));
  $("btn-details").append(icon("list"));
  $("btn-grid").append(icon("grid"));
  btn("btn-mkdir", "newfolder", "New folder").onclick = mkdir;
  btn("btn-upload", "upload", "Upload").onclick = (ev) => {
    const r = ev.currentTarget.getBoundingClientRect();
    contextMenu(
      [
        { label: "Files\u2026", icon: "file", onclick: () => $("file-input").click() },
        { label: "Folder\u2026", icon: "folder", onclick: () => $("folder-input").click() },
      ],
      r.left,
      r.bottom + 4,
    );
  };
  btn("btn-dlall", "download", "Download all").onclick = () => download(base);
  btn("btn-dl", "download", "Download").onclick = () => download(selectedItems());
  btn("btn-rename", "rename", "Rename").onclick = () => renameItem(selectedItems()[0]);
  btn("btn-move", "move", "Move").onclick = () => moveItems(selectedItems());
  btn("btn-delete", "trash", "Delete").onclick = () => deleteItems(selectedItems());
  $("btn-clearsel").append(icon("close"));
  $("btn-clearsel").onclick = clearSelection;

  $("btn-back").onclick = () => history.back();
  $("btn-up").onclick = up;
  $("btn-details").onclick = () => {
    state.prefs.layout = "details";
    savePrefs();
    render();
  };
  $("btn-grid").onclick = () => {
    state.prefs.layout = "grid";
    savePrefs();
    render();
  };
  const sort = $("sort");
  sort.replaceChildren(...SORTS.map(([k, l]) => el("option", { value: k, text: l })));
  sort.onchange = () => setSort(sort.value);
  $("sort-dir").onclick = () => {
    state.prefs.sortDir *= -1;
    savePrefs();
    render();
  };

  const search = $("search");
  let t;
  search.addEventListener("input", () => {
    clearTimeout(t);
    t = setTimeout(() => {
      state.query = search.value;
      if (!state.query.trim() && state.hits) return endSearch();
      if (!state.hits) {
        state.selected.clear();
        render();
      }
    }, 80);
  });
  search.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") {
      state.query = search.value;
      deepSearch();
    }
    if (ev.key === "Escape") {
      search.value = "";
      if (state.hits) endSearch();
      else {
        state.query = "";
        render();
      }
      search.blur();
    }
  });

  $("file-input").onchange = (ev) => {
    enqueue(ev.target.files);
    ev.target.value = "";
  };
  $("folder-input").onchange = (ev) => {
    enqueue(fromFolderInput(ev.target.files));
    ev.target.value = "";
  };

  const main = $("browser");
  main.addEventListener("contextmenu", (ev) => {
    if (ev.target.closest("[data-path], input, a, button")) return;
    ev.preventDefault();
    clearSelection();
    backgroundMenu(ev.clientX, ev.clientY);
  });
  $("list").addEventListener("click", (ev) => {
    if (!ev.target.closest("[data-path]") && !ev.ctrlKey && !ev.metaKey) clearSelection();
  });

  // Dropping files or folders from the desktop uploads them.
  const zone = $("dropzone");
  let depth = 0;
  const isFiles = (ev) => ev.dataTransfer && [...ev.dataTransfer.types].includes("Files");
  const host = $("main");
  host.addEventListener("dragenter", (ev) => {
    if (!isFiles(ev) || !has("upload") || state.view !== "files") return;
    ev.preventDefault();
    depth++;
    zone.hidden = false;
  });
  host.addEventListener("dragover", (ev) => {
    if (isFiles(ev) && has("upload")) ev.preventDefault();
  });
  host.addEventListener("dragleave", () => {
    if (--depth <= 0) {
      depth = 0;
      zone.hidden = true;
    }
  });
  host.addEventListener("drop", async (ev) => {
    depth = 0;
    zone.hidden = true;
    if (!isFiles(ev) || !has("upload")) return;
    ev.preventDefault();
    enqueue(await fromDataTransfer(ev.dataTransfer));
  });
}
