import { useCallback, useEffect, useRef, useState } from "react";
import { get } from "./api";
import { useStore } from "../state/store";

/// Loads data for a view and reloads when deps change. `reload` re-runs it.
export function useLoad<T>(load: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const run = useCallback(async () => {
    setLoading(true);
    try {
      setData(await load());
      setError("");
    } catch (e) {
      setError(String((e as Error).message ?? e));
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  useEffect(() => {
    void run();
  }, [run]);
  return { data, error, loading, reload: run, setData };
}

/// Re-runs `reload` whenever the server reports a change, and every
/// `everyMs` for state the server doesn't announce (who's online). Nothing
/// happens while `paused`, so unsaved edits aren't thrown away.
export function useLive(base: string, reload: () => Promise<void>, opts: { paused?: boolean; everyMs?: number } = {}) {
  const rev = useStore((s) => s.revs[base] ?? 0);
  const latest = useRef({ reload, paused: !!opts.paused });
  latest.current = { reload, paused: !!opts.paused };
  const seen = useRef(rev);
  useEffect(() => {
    if (rev === seen.current || latest.current.paused) return;
    seen.current = rev;
    void latest.current.reload();
  }, [rev, opts.paused]);
  useEffect(() => {
    if (!opts.everyMs) return;
    const t = setInterval(() => !latest.current.paused && !document.hidden && void latest.current.reload(), opts.everyMs);
    return () => clearInterval(t);
  }, [opts.everyMs]);
}

interface Revision {
  config: string;
  activity?: string;
}

/// Long-polls each server's /api/changes so the app follows edits made in
/// the browser, by other admins, or by people uploading and deleting.
export function useWatchServers() {
  const bases = useStore((s) => s.servers.map((x) => x.base).join("\n"));
  useEffect(() => {
    const stops = bases
      .split("\n")
      .filter(Boolean)
      .map((base) => {
        let stopped = false;
        void (async () => {
          let have: Revision | null = null;
          while (!stopped) {
            try {
              const q = have ? `?config=${have.config}&activity=${have.activity ?? ""}` : "";
              const next: Revision = await get<Revision>(base, "/api/changes" + q);
              if (stopped) return;
              if (have) {
                const st = useStore.getState();
                if (next.config !== have.config) await st.refreshMe(base);
                if (next.config !== have.config || next.activity !== have.activity) st.serverChanged(base);
              }
              have = next;
            } catch {
              // Offline, or an older server without /api/changes.
              await new Promise((r) => setTimeout(r, 10_000));
            }
          }
        })();
        return () => (stopped = true);
      });
    return () => stops.forEach((f) => f());
  }, [bases]);
}
