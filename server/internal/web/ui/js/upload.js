import { $, el, api, shareURL, joinPath, fmtSize, sleep, status } from "./util.js";
import { state, has, emit } from "./state.js";
import { modal } from "./modal.js";

// Each file is sent in CHUNK-sized PUTs at an explicit offset; the upload id
// is kept in localStorage so picking the same file again after a reload or
// dropped connection continues where it stopped.
const CHUNK = 16 << 20;
const PARALLEL_FILES = 2;

export const transfers = [];
let active = 0;

const resumeKey = (t) => ["ts-up", t.share, t.dir, t.file.name, t.file.size, t.file.lastModified].join("|");
const running = (t) => t.state === "uploading" || t.state === "queued";

export function renderTransfers() {
  const ul = $("transfers");
  ul.replaceChildren();
  const shown = transfers.slice(-50);
  for (const t of shown) {
    const pct = t.file.size ? Math.floor((t.offset / t.file.size) * 100) : 100;
    const bar = el("div", { class: "bar" + (t.state === "done" ? " ok" : t.state === "failed" ? " bad" : "") }, el("div"));
    bar.firstChild.style.width = pct + "%";
    let line = t.state;
    if (t.state === "uploading") line = `${pct}%  ${fmtSize(t.offset)} / ${fmtSize(t.file.size)}${t.rate ? "  " + fmtSize(t.rate) + "/s" : ""}`;
    if (t.state === "done") line = t.finalName !== t.file.name ? "done, saved as " + t.finalName : "done";
    if (t.state === "failed") line = "failed: " + t.error;
    const btn = running(t)
      ? el("button", { text: "Cancel", onclick: () => cancelTransfer(t) })
      : t.state === "failed"
        ? el("button", { text: "Retry", onclick: () => retryTransfer(t) })
        : el("button", { text: "Clear", onclick: () => clearTransfer(t) });
    const label = t.dir && t.dir !== t.baseDir ? joinPath(t.dir.slice(t.baseDir ? t.baseDir.length + 1 : 0), t.file.name) : t.file.name;
    ul.append(el("li", {}, el("div", { class: "tname" }, el("span", { text: label, title: label }), btn), bar, el("div", { class: "dim", text: line })));
  }
  const n = transfers.filter(running).length;
  const done = transfers.filter((t) => !running(t)).length;
  $("transfers-h").hidden = transfers.length === 0;
  $("transfers-clear").hidden = done === 0;
  $("transfers-count").textContent = n ? `${n} left` : "";
  document.title = n ? `(${n}) tailshare` : "tailshare";
}

// items: [{file, rel}] where rel is the folder path inside the upload, or a
// plain FileList / File[] for files going straight into the current folder.
export async function enqueue(items) {
  if (!state.share || !has("upload")) return;
  const list = [...items].map((x) => (x instanceof File ? { file: x, rel: "" } : x));
  if (!list.length) return;
  const share = state.share.name;
  const base = state.path;

  const dirs = [...new Set(list.map((x) => x.rel).filter(Boolean))];
  if (dirs.length && !has("mkdir")) {
    status("You can upload files here but not create folders, so folders can't be uploaded.", true);
    return;
  }

  let replace = false;
  const top = list.filter((x) => !x.rel);
  if (has("overwrite") && top.length) {
    const existing = new Set(state.entries.filter((e) => !e.dir).map((e) => e.name));
    const clashes = top.filter((x) => existing.has(x.file.name));
    if (clashes.length) {
      const choice = await modal({
        title: clashes.length === 1 ? `${clashes[0].file.name} already exists` : `${clashes.length} files already exist`,
        text: "Replace them (old versions go to the trash) or keep both?",
        ok: "Keep both",
        alt: "Replace",
      });
      if (choice === null) return;
      replace = choice === "alt";
    }
  }

  // Parents before children; "already exists" is fine (merging into a folder).
  // A clash with a file of the same name is reported by the uploads that need it.
  for (const d of dirs.flatMap(expand).filter(unique).sort((a, b) => a.split("/").length - b.split("/").length)) {
    const i = d.lastIndexOf("/");
    try {
      await api("POST", shareURL(share, "mkdir"), { path: joinPath(base, i < 0 ? "" : d.slice(0, i)), name: d.slice(i + 1) });
    } catch (e) {
      if (e.status !== 409) {
        status(`Couldn't create folder ${d}: ${e.message}`, true);
        return;
      }
    }
  }
  if (dirs.length) emit("ts:changed", { share, dir: base });

  for (const x of list) transfers.push({ file: x.file, share, dir: joinPath(base, x.rel), baseDir: base, offset: 0, state: "queued", replace: replace && !x.rel });
  renderTransfers();
  pump();
}

function expand(d) {
  const parts = d.split("/");
  return parts.map((_, i) => parts.slice(0, i + 1).join("/"));
}
function unique(v, i, a) {
  return a.indexOf(v) === i;
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
  emit("ts:me");
  emit("ts:changed", { share: t.share, dir: t.dir });
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

export function clearFinished() {
  for (let i = transfers.length - 1; i >= 0; i--) if (!running(transfers[i])) transfers.splice(i, 1);
  renderTransfers();
}

window.addEventListener("beforeunload", (e) => {
  if (transfers.some(running)) e.preventDefault();
});

// Turns a drop into [{file, rel}], walking dropped folders.
export async function fromDataTransfer(dt) {
  const entries = [...dt.items].map((i) => i.webkitGetAsEntry && i.webkitGetAsEntry()).filter(Boolean);
  if (!entries.length) return [...dt.files].map((file) => ({ file, rel: "" }));
  const out = [];
  const walk = async (entry, rel) => {
    if (entry.isFile) {
      const file = await new Promise((res, rej) => entry.file(res, rej));
      out.push({ file, rel });
    } else if (entry.isDirectory) {
      const reader = entry.createReader();
      const sub = joinPath(rel, entry.name);
      // readEntries returns at most ~100 at a time.
      for (;;) {
        const batch = await new Promise((res, rej) => reader.readEntries(res, rej));
        if (!batch.length) break;
        for (const e of batch) await walk(e, sub);
      }
    }
  };
  for (const e of entries) await walk(e, "");
  return out;
}

// From <input webkitdirectory>: webkitRelativePath is "folder/sub/file".
export function fromFolderInput(files) {
  return [...files].map((file) => {
    const p = file.webkitRelativePath || file.name;
    return { file, rel: p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "" };
  });
}
