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
  selected: Selection;
  host: HostState;
  hostLog: string[];
  background: boolean;
  backgroundSupported: boolean;
  settings: Settings | null;
  drives: DrivesState;

  say: (message: string) => void;
  setShowSettings: (v: boolean) => void;
  select: (s: Selection) => void;
  scan: () => Promise<void>;
  loadHost: () => Promise<void>;
  loadSettings: () => Promise<void>;
  saveSettings: (s: Settings) => Promise<void>;
  loadDrives: () => Promise<void>;
}

export const useStore = create<State>((set, getState) => ({
  message: "ready",
  showSettings: false,
  updateNonce: 0,

  tailnet: null,
  tailnetError: "",
  scanning: false,
  servers: [],
  selected: { kind: "host" },
  host: { phase: "stopped", hostname: "", stateDir: "" },
  hostLog: [],
  background: false,
  backgroundSupported: false,
  settings: null,
  drives: { running: false, mounts: [], transfers: [], log: [], names: {} },

  say: (message) => set({ message }),
  setShowSettings: (showSettings) => set({ showSettings }),
  select: (selected) => set({ selected }),

  loadHost: async () => {
    const snap = await invoke<HostSnapshot>("host_state");
    const was = getState().host.phase;
    set({ host: snap.state, hostLog: snap.log, background: snap.background, backgroundSupported: snap.backgroundSupported });
    // The new node takes a moment to appear to this computer's Tailscale.
    if (snap.state.phase === "running" && was !== "running") {
      setTimeout(() => void getState().scan(), 3000);
      setTimeout(() => void getState().scan(), 15000);
    }
  },

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
