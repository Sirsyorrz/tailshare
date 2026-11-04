import { create } from "zustand";
import { invoke } from "@tauri-apps/api/core";
import { get, baseOf, type DrivesState, type HostSnapshot, type HostState, type Me, type Settings, type Tailnet } from "../lib/api";

export interface Server {
  base: string;
  version: string;
  me?: Me;
  error?: string;
}

export type Selection = { kind: "host" } | { kind: "drives" } | { kind: "server"; base: string };

interface State {
  message: string;
  showSettings: boolean;
  /// Bumped by Settings > Check; the status bar Updater watches it.
  updateNonce: number;

  tailnet: Tailnet | null;
  tailnetError: string;
  scanning: boolean;
  servers: Server[];
  /// Bumped whenever a server reports a change; open views reload on it.
  revs: Record<string, number>;
  selected: Selection;
  host: HostState;
  hostLog: string[];
  background: boolean;
  backgroundSupported: boolean;
  /// The hosted machine exists on the tailnet, so its name is fixed here.
  registered: boolean;
  settings: Settings | null;
  drives: DrivesState;

  say: (message: string) => void;
  setShowSettings: (v: boolean) => void;
  select: (s: Selection) => void;
  scan: () => Promise<void>;
  loadHost: () => Promise<void>;
  setHost: (h: HostState) => void;
  loadSettings: () => Promise<void>;
  saveSettings: (s: Settings) => Promise<void>;
  loadDrives: () => Promise<void>;
  refreshMe: (base: string) => Promise<void>;
  serverChanged: (base: string) => void;
}

export const useStore = create<State>((set, getState) => ({
  message: "ready",
  showSettings: false,
  updateNonce: 0,

  tailnet: null,
  tailnetError: "",
  scanning: false,
  servers: [],
  revs: {},
  selected: { kind: "host" },
  host: { phase: "stopped", hostname: "", stateDir: "" },
  hostLog: [],
  background: false,
  backgroundSupported: false,
  registered: false,
  settings: null,
  drives: { running: false, mounts: [], transfers: [], log: [], names: {} },

  say: (message) => set({ message }),
  setShowSettings: (showSettings) => set({ showSettings }),
  select: (selected) => set({ selected }),

  loadHost: async () => {
    const snap = await invoke<HostSnapshot>("host_state");
    set({ background: snap.background, backgroundSupported: snap.backgroundSupported, registered: snap.registered, hostLog: snap.log });
    getState().setHost(snap.state);
  },

  setHost: (next) => {
    const prev = getState().host;
    set({ host: next });
    if (next.phase !== "running" || !next.url || next.url === prev.url) return;
    // Started, or renamed in the admin console: the server list, the
    // selected server and the saved name all follow the new address.
    const oldBase = prev.url ? baseOf(prev.url) : "";
    const newBase = baseOf(next.url);
    const sel = getState().selected;
    set((s) => ({
      registered: true,
      servers: oldBase && oldBase !== newBase ? s.servers.filter((x) => x.base !== oldBase) : s.servers,
      selected: sel.kind === "server" && sel.base === oldBase ? { kind: "server", base: newBase } : sel,
    }));
    void getState().loadSettings();
    // The new node takes a moment to appear to this computer's Tailscale.
    void getState().scan();
    setTimeout(() => void getState().scan(), 3000);
    setTimeout(() => void getState().scan(), 15000);
  },

  refreshMe: async (base) => {
    let patch: Partial<Server>;
    try {
      patch = { me: await get<Me>(base, "/api/me"), error: undefined };
    } catch (e) {
      patch = { error: String((e as Error).message ?? e) };
    }
    set((s) => ({ servers: s.servers.map((x) => (x.base === base ? { ...x, ...patch } : x)) }));
  },
  serverChanged: (base) => set((s) => ({ revs: { ...s.revs, [base]: (s.revs[base] ?? 0) + 1 } })),

  loadDrives: async () => {
    try {
      set({ drives: await invoke<DrivesState>("drives_state") });
    } catch {
      // Polled; a failed poll just keeps the last state.
    }
  },

  loadSettings: async () => set({ settings: await invoke<Settings>("get_settings") }),
  saveSettings: async (s) => {
    await invoke("set_settings", { s });
    set({ settings: s });
  },

  // Finds tailshare servers by asking every online peer. The hosted node and
  // servers added by hand are always included.
  scan: async () => {
    if (getState().scanning) return;
    set({ scanning: true });
    try {
      let tailnet: Tailnet;
      try {
        tailnet = await invoke<Tailnet>("tailnet_status");
        set({ tailnet, tailnetError: "" });
      } catch (e) {
        set({ tailnet: null, tailnetError: String(e) });
        return;
      }
      if (!tailnet.running) return;
      const { settings, host } = getState();
      const bases = new Set<string>();
      for (const p of tailnet.peers) if (p.online) bases.add(`https://${p.dns}/`);
      for (const b of settings?.servers ?? []) bases.add(b);
      if (host.url) bases.add(baseOf(host.url));
      const found = await invoke<{ base: string; version: string }[]>("discover", { bases: [...bases] });
      const servers: Server[] = await Promise.all(
        found.map(async (f) => {
          try {
            return { ...f, me: await get<Me>(f.base, "/api/me") };
          } catch (e) {
            return { ...f, error: String((e as Error).message ?? e) };
          }
        }),
      );
      servers.sort((a, b) => a.base.localeCompare(b.base));
      set({ servers });
    } finally {
      set({ scanning: false });
    }
  },
}));
