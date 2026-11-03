import { $, el, icon, api, shareURL, joinPath, parentOf } from "./util.js";

// modal resolves to the input value (or true), "alt" for the alternate
// button, or null on cancel.
export function modal({ title, text = "", input = null, ok = "OK", alt = null, danger = false, select = true }) {
  return new Promise((resolve) => {
    $("modal-title").textContent = title;
    $("modal-text").textContent = text;
    const inp = $("modal-input");
    inp.hidden = input === null;
    inp.value = input ?? "";
    $("modal-ok").textContent = ok;
    $("modal-ok").className = danger ? "danger" : "primary";
    $("modal-alt").hidden = !alt;
    $("modal-alt").textContent = alt ?? "";
    $("modal-back").hidden = false;
    (input !== null ? inp : $("modal-ok")).focus();
    if (input !== null && select) {
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

export const modalOpen = () => !$("modal-back").hidden || !$("picker-back").hidden;

export function closeModal() {
  if (!$("modal-back").hidden) $("modal-cancel").click();
  if (!$("picker-back").hidden) $("picker-cancel").click();
}

// items: [{label, icon, onclick, danger, disabled}] or "-" for a divider.
export function contextMenu(items, x, y) {
  closeMenu();
  const menu = el(
    "div",
    { class: "ctxmenu", id: "ctxmenu", role: "menu" },
    items.map((it) =>
      it === "-"
        ? el("div", { class: "sep" })
        : el(
            "button",
            {
              class: it.danger ? "danger" : "",
              disabled: it.disabled,
              role: "menuitem",
              onclick: () => {
                closeMenu();
                it.onclick();
              },
            },
            icon(it.icon || "file"),
            el("span", { text: it.label }),
          ),
    ),
  );
  document.body.append(menu);
  const r = menu.getBoundingClientRect();
  menu.style.left = Math.min(x, innerWidth - r.width - 4) + "px";
  menu.style.top = Math.min(y, innerHeight - r.height - 4) + "px";
  menu.querySelector("button:not(:disabled)")?.focus();
}

export function closeMenu() {
  document.getElementById("ctxmenu")?.remove();
}

export const menuOpen = () => !!document.getElementById("ctxmenu");

document.addEventListener("pointerdown", (e) => {
  const m = document.getElementById("ctxmenu");
  if (m && !m.contains(e.target)) closeMenu();
});
window.addEventListener("blur", closeMenu);
window.addEventListener("resize", closeMenu);

// pickFolder lets the user choose a destination folder inside a share.
// Resolves to the path ("" is the share root) or null.
export function pickFolder(share, start, title, exclude = []) {
  return new Promise((resolve) => {
    let at = start;
    const list = $("picker-list");
    const back = $("picker-back");
    $("picker-title").textContent = title;

    const load = async () => {
      $("picker-path").textContent = "/" + at;
      $("picker-up").disabled = at === "";
      list.replaceChildren(el("li", { class: "dim", text: "Loading\u2026" }));
      try {
        const ents = await api("GET", shareURL(share, "list?path=" + encodeURIComponent(at)));
        const dirs = ents.filter((e) => e.dir && !exclude.includes(joinPath(at, e.name)));
        list.replaceChildren(
          ...(dirs.length
            ? dirs.map((d) =>
                el(
                  "li",
                  {
                    tabindex: "0",
                    onclick: () => {
                      at = joinPath(at, d.name);
                      load();
                    },
                  },
                  icon("folder", "icon folder"),
                  el("span", { text: d.name }),
                ),
              )
            : [el("li", { class: "dim", text: "No folders here." })]),
        );
      } catch (e) {
        list.replaceChildren(el("li", { class: "bad", text: e.message }));
      }
    };
    const done = (v) => {
      back.hidden = true;
      $("picker-ok").onclick = $("picker-cancel").onclick = $("picker-up").onclick = null;
      resolve(v);
    };
    $("picker-up").onclick = () => {
      at = parentOf(at);
      load();
    };
    $("picker-ok").onclick = () => done(at);
    $("picker-cancel").onclick = () => done(null);
    back.hidden = false;
    load();
  });
}
