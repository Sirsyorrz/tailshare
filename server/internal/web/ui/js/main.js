import { $, el, icon, api, fmtSize, status } from "./util.js";
import { state, on } from "./state.js";
import { closeModal, modalOpen, menuOpen, closeMenu } from "./modal.js";
import { renderTransfers, clearFinished } from "./upload.js";
import { setupLightbox, lightboxOpen, closeLightbox, step } from "./lightbox.js";
import { setupBrowser, load, render, onKey } from "./browser.js";
import { renderAdminNav, showAdmin } from "./admin.js";

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
on("ts:me", refreshMe);
on("ts:view", () => {
  renderShares();
  renderAdminNav();
  render();
});

function shareDetail(s) {
  const parts = [permLabel(s.perms)];
  if (s.expires) parts.push("until " + new Date(new Date(s.expires).getTime() - 1).toLocaleDateString());
  return parts.join(" \u00b7 ");
}

function renderShares() {
  const ul = $("shares");
  ul.replaceChildren();
  for (const s of state.me.shares) {
    const active = state.view === "files" && state.share && state.share.name === s.name;
    const li = el(
      "li",
      { class: active ? "active" : "", onclick: () => (location.hash = "#" + encodeURIComponent(s.name)) },
      el("span", { class: "sname", text: s.name }),
      el("span", { class: "perms", text: shareDetail(s) }),
    );
    if (s.quota) {
      const bar = el("div", { class: "bar" + (s.used / s.quota > 0.9 ? " bad" : "") }, el("div"));
      bar.firstChild.style.width = Math.min(100, (s.used / s.quota) * 100) + "%";
      li.append(bar, el("span", { class: "perms", text: `${fmtSize(s.used)} of ${fmtSize(s.quota)} used` }));
    }
    ul.append(li);
  }
}

// The hash is the single source of truth for where we are, so back/forward
// and reloads work: "#share/sub/dir", or "#!status" for admin views.
async function route() {
  document.body.classList.remove("nav-open");
  const h = location.hash.slice(1);
  if (h.startsWith("!") && state.me.admin) {
    state.view = h.slice(1);
    state.share = null;
    $("admin-view").hidden = false;
    $("browser").hidden = true;
    return showAdmin(state.view);
  }
  state.view = "files";
  $("admin-view").hidden = true;
  const [first, ...rest] = h.split("/").map((p) => {
    try {
      return decodeURIComponent(p);
    } catch {
      return p;
    }
  });
  let s = state.me.shares.find((x) => x.name === first);
  if (!s && !first && state.me.shares.length === 1) s = state.me.shares[0];
  const path = s && s.name === first ? rest.filter(Boolean).join("/") : "";
  const changedShare = s !== state.share;
  state.share = s || null;
  renderShares();
  renderAdminNav();
  if (!s) {
    state.path = "";
    state.entries = [];
    return render();
  }
  if (changedShare) state.path = null;
  await load(path);
}

function typing(t) {
  return t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
}

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    if (menuOpen()) return closeMenu();
    if (modalOpen()) return closeModal();
    if (lightboxOpen()) return closeLightbox();
  }
  if (modalOpen() || menuOpen()) return;
  if (lightboxOpen()) {
    if (e.key === "ArrowLeft") step(-1);
    if (e.key === "ArrowRight") step(1);
    return;
  }
  if (typing(e.target)) return;
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "f") {
    if (!$("search-box").hidden) {
      e.preventDefault();
      $("search").focus();
      $("search").select();
    }
    return;
  }
  if (e.key === "/" && !$("search-box").hidden) {
    e.preventDefault();
    $("search").focus();
    return;
  }
  if (onKey(e)) e.preventDefault();
});

async function init() {
  setupBrowser();
  setupLightbox();
  $("transfers-clear").onclick = clearFinished;
  $("btn-nav").append(icon("menu"));
  $("btn-nav").onclick = () => document.body.classList.toggle("nav-open");
  $("nav-scrim").onclick = () => document.body.classList.remove("nav-open");
  // Mouse back button / Alt+Left are handled by the browser through the hash.

  try {
    state.me = await api("GET", "/api/me");
  } catch (e) {
    status("Couldn't reach tailshare: " + e.message, true);
    return;
  }
  const u = state.me.user;
  $("whoami").textContent = (u.display && u.display !== u.login ? `${u.display} (${u.login})` : u.login) + ` on ${u.node}`;
  renderTransfers();
  window.addEventListener("hashchange", route);
  await route();
}

init();
