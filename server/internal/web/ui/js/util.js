export const $ = (id) => document.getElementById(id);

// Every node is built with textContent; file and share names come from other
// people and must never be parsed as HTML. No inline styles either: the CSP
// forbids them (CSSOM property writes like el.style.width are fine).
export function el(tag, props = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else if (v === true) n.setAttribute(k, "");
    else n.setAttribute(k, v);
  }
  for (const c of children.flat()) if (c !== null && c !== undefined && c !== false) n.append(c);
  return n;
}

const ICONS = {
  folder: "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z",
  file: "M6 3h8l5 5v12a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z M14 3v5h5",
  image: "M4 5h16v14H4z M4 16l5-5 4 4 2-2 5 5 M15 9.5a1 1 0 1 0 0 .01",
  video: "M3 6h12v12H3z M15 10l6-3v10l-6-3",
  audio: "M9 18V6l10-2v12 M9 18a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z M19 16a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z",
  archive: "M4 4h16v4H4z M5 8h14v12H5z M10 12h4",
  doc: "M6 3h8l5 5v12a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z M14 3v5h5 M8 13h8 M8 17h6",
  back: "M15 5l-7 7 7 7",
  up: "M12 19V5 M5 12l7-7 7 7",
  down: "M12 5v14 M5 12l7 7 7-7",
  grid: "M4 4h7v7H4z M13 4h7v7h-7z M4 13h7v7H4z M13 13h7v7h-7z",
  list: "M4 6h16 M4 12h16 M4 18h16",
  search: "M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13z M15.5 15.5L20 20",
  download: "M12 4v11 M7 10l5 5 5-5 M5 20h14",
  upload: "M12 20V9 M7 14l5-5 5 5 M5 4h14",
  newfolder: "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z M12 11v6 M9 14h6",
  trash: "M4 7h16 M9 7V4h6v3 M6 7l1 13h10l1-13",
  close: "M6 6l12 12 M18 6L6 18",
  more: "M5 12h.01 M12 12h.01 M19 12h.01",
  menu: "M4 6h16 M4 12h16 M4 18h16",
  prev: "M15 5l-7 7 7 7",
  next: "M9 5l7 7-7 7",
  move: "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z M10 14h6 M13 11l3 3-3 3",
  rename: "M4 20h4L19 9l-4-4L4 16z M13 7l4 4",
  refresh: "M20 11a8 8 0 1 0-2.3 5.7 M20 5v6h-6",
};

export function icon(name, cls = "icon") {
  const NS = "http://www.w3.org/2000/svg";
  const s = document.createElementNS(NS, "svg");
  s.setAttribute("viewBox", "0 0 24 24");
  s.setAttribute("class", cls);
  s.setAttribute("aria-hidden", "true");
  const p = document.createElementNS(NS, "path");
  p.setAttribute("d", ICONS[name] || ICONS.file);
  s.append(p);
  return s;
}

export async function api(method, url, body) {
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

export const shareURL = (name, rest) => `/api/shares/${encodeURIComponent(name)}/${rest}`;
export const joinPath = (dir, name) => (dir ? dir + "/" + name : name);
export const parentOf = (p) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");
export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Decimal units, matching the server's messages.
export function fmtSize(n) {
  if (n < 1000) return Math.round(n) + " B";
  const u = ["KB", "MB", "GB", "TB"];
  let i = -1;
  do {
    n /= 1000;
    i++;
  } while (n >= 1000 && i < u.length - 1);
  return n.toFixed(n < 10 ? 1 : 0) + " " + u[i];
}

export function fmtDate(s) {
  const d = new Date(s);
  return d.toLocaleDateString() + " " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

let statusTimer;
export function status(msg, bad = false) {
  const s = $("status-msg");
  s.textContent = msg;
  s.className = bad ? "bad" : "";
  clearTimeout(statusTimer);
  if (msg && !bad) statusTimer = setTimeout(() => (s.textContent = ""), 6000);
}

const EXT = {
  image: "jpg jpeg png gif webp bmp tif tiff heic heif avif svg raw cr2 cr3 nef arw dng psd",
  video: "mp4 m4v mov mkv webm avi wmv flv mts m2ts ts mpg mpeg 3gp",
  audio: "mp3 m4a aac wav flac ogg opus wma aiff",
  doc: "pdf doc docx odt rtf txt md log csv json xls xlsx ods ppt pptx odp epub",
  archive: "zip rar 7z tar gz tgz xz bz2 zst iso",
};
const KIND = {};
for (const [k, list] of Object.entries(EXT)) for (const e of list.split(" ")) KIND[e] = k;

export const extOf = (name) => (name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toLowerCase() : "");
export const kindOf = (e) => (e.dir ? "folder" : KIND[extOf(e.name)] || "other");

export const KIND_LABEL = {
  folder: "Folder",
  image: "Image",
  video: "Video",
  audio: "Audio",
  doc: "Document",
  archive: "Archive",
  other: "File",
};

export function typeLabel(e) {
  if (e.dir) return "Folder";
  const x = extOf(e.name);
  return x ? `${x.toUpperCase()} ${KIND_LABEL[kindOf(e)].toLowerCase()}` : "File";
}

// What the server can make a thumbnail of (video only if it has ffmpeg;
// a failed thumbnail just falls back to the icon).
const THUMB = new Set("jpg jpeg png gif webp bmp tif tiff heic heif avif mp4 m4v mov mkv webm avi wmv flv mts m2ts ts mpg mpeg 3gp".split(" "));
export const hasThumb = (e) => !e.dir && THUMB.has(extOf(e.name));

// Mirrors inlineTypes on the server.
const INLINE = {
  image: new Set("jpg jpeg png gif webp avif bmp".split(" ")),
  video: new Set("mp4 m4v webm mov mkv".split(" ")),
  audio: new Set("mp3 m4a aac ogg opus wav flac".split(" ")),
  text: new Set("txt md log csv json".split(" ")),
};
// How the lightbox shows a file, or null if it can't.
export function previewKind(e) {
  if (e.dir) return null;
  const x = extOf(e.name);
  for (const [k, set] of Object.entries(INLINE)) if (set.has(x)) return k;
  if (THUMB.has(x) && KIND[x] === "image") return "image-thumb";
  return null;
}
