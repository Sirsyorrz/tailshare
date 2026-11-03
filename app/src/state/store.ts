import { create } from "zustand";
import { invoke } from "@tauri-apps/api/core";
import { get, baseOf, type HostState, type Me, type Settings, type Tailnet } from "../lib/api";

export interface Server {
  base: string;
  version: string;
  me?: Me;
  error?: string;
}

export type Selection = { kind: "host" } | { kind: "server"; base: string };

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
  settings: Settings | null;

  say: (message: string) => void;
  setShowSettings: (v: boolean) => void;
  select: (s: Selection) => void;
  scan: () => Promise<void>;
  loadSettings: () => Promise<void>;
  saveSettings: (s: Settings) => Promise<void>;
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
  settings: null,

  say: (message) => set({ message }),
  setShowSettings: (showSettings) => set({ showSettings }),
  select: (selected) => set({ selected }),

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
