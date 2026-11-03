import { $, el, icon, shareURL, fmtSize, fmtDate, previewKind } from "./util.js";
import { state, has } from "./state.js";

let items = [];
let index = -1;

export const lightboxOpen = () => !$("lightbox").hidden;

const fileURL = (kind, p) => shareURL(state.share.name, `${kind}?path=${encodeURIComponent(p)}`);

// open shows item i of list (only previewable items are kept, so
// next/previous skip the rest).
export function openLightbox(list, item) {
  items = list.filter((e) => previewKind(e));
  index = items.indexOf(item);
  if (index < 0) return;
  $("lightbox").hidden = false;
  show();
}

export function closeLightbox() {
  $("lightbox").hidden = true;
  // Stops a playing video.
  $("lb-stage").replaceChildren();
}

export function step(d) {
  if (!items.length) return;
  index = (index + d + items.length) % items.length;
  show();
}

function show() {
  const e = items[index];
  const stage = $("lb-stage");
  const kind = previewKind(e);
  let node;
  if (kind === "image") node = el("img", { src: fileURL("view", e.path), alt: e.name });
  else if (kind === "image-thumb") node = el("img", { src: fileURL("thumb", e.path) + "&size=large&v=" + Date.parse(e.modified), alt: e.name });
  else if (kind === "video") node = el("video", { src: fileURL("view", e.path), controls: true, autoplay: true, playsinline: true });
  else if (kind === "audio") node = el("audio", { src: fileURL("view", e.path), controls: true, autoplay: true });
  else if (kind === "text") {
    node = el("pre", { class: "lb-text", text: "Loading\u2026" });
    fetch(fileURL("view", e.path))
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(r.statusText))))
      .then((t) => (node.textContent = t.length > 2e6 ? t.slice(0, 2e6) + "\n\n[truncated, download for the rest]" : t))
      .catch((err) => (node.textContent = "Couldn't load: " + err.message));
  }
  if (node && node.tagName !== "PRE") {
    node.addEventListener("error", () => stage.replaceChildren(el("div", { class: "lb-error", text: "This file can't be previewed in the browser. Download it instead." })));
  }
  stage.replaceChildren(node);
  $("lb-name").textContent = e.name;
  $("lb-meta").textContent = `${fmtSize(e.size)} \u00b7 ${fmtDate(e.modified)} \u00b7 ${index + 1} of ${items.length}`;
  const dl = $("lb-download");
  dl.hidden = !has("download");
  dl.href = fileURL("download", e.path);
  dl.setAttribute("download", e.name);
  $("lb-prev").hidden = $("lb-next").hidden = items.length < 2;
}

export function setupLightbox() {
  $("lb-close").append(icon("close"));
  $("lb-prev").append(icon("prev"));
  $("lb-next").append(icon("next"));
  $("lb-download").prepend(icon("download"));
  $("lb-close").onclick = closeLightbox;
  $("lb-prev").onclick = () => step(-1);
  $("lb-next").onclick = () => step(1);
  $("lightbox").addEventListener("click", (e) => {
    if (e.target.id === "lightbox" || e.target.id === "lb-stage") closeLightbox();
  });
  // Swipe on phones.
  let x0 = null;
  $("lb-stage").addEventListener("touchstart", (e) => (x0 = e.touches[0].clientX), { passive: true });
  $("lb-stage").addEventListener("touchend", (e) => {
    if (x0 === null) return;
    const dx = e.changedTouches[0].clientX - x0;
    if (Math.abs(dx) > 60) step(dx < 0 ? 1 : -1);
    x0 = null;
  });
}
