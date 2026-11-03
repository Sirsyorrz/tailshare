// Shared between modules. Only main.js and browser.js write to it.
export const state = {
  me: null,
  // "files", or an admin view: "status", "activity", "trash"
  view: "files",
  share: null,
  path: "",
  entries: [],
  // Recursive search results, when a subfolder search is active.
  hits: null,
  truncated: false,
  loading: false,
  query: "",
  filter: "all",
  selected: new Set(),
  anchor: null,
  focus: null,
  prefs: loadPrefs(),
};

function loadPrefs() {
  const def = { layout: "details", sortKey: "name", sortDir: 1 };
  try {
    return { ...def, ...JSON.parse(localStorage.getItem("ts-prefs") || "{}") };
  } catch {
    return def;
  }
}

export function savePrefs() {
  localStorage.setItem("ts-prefs", JSON.stringify(state.prefs));
}

export const has = (p) => !!state.share && state.share.perms.includes(p);

// Lets modules ask for a refresh without importing each other in a cycle.
export const emit = (name, detail) => window.dispatchEvent(new CustomEvent(name, { detail }));
export const on = (name, f) => window.addEventListener(name, (e) => f(e.detail));
