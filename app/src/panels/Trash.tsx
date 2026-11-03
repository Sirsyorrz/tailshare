import { ask } from "@tauri-apps/plugin-dialog";
import { call, fmtDate, fmtSize, get, type AdminStatus, type TrashItem } from "../lib/api";
import { useLoad } from "../lib/hooks";
import { useStore } from "../state/store";

export function Trash({ base }: { base: string }) {
  const { data, error, reload } = useLoad(async () => {
    const st = await get<AdminStatus>(base, "/api/admin/status");
    return Promise.all(
      st.shares
        .filter((s) => s.online)
        .map(async (s) => ({
          share: s.name,
          items: await get<TrashItem[]>(base, `/api/admin/shares/${encodeURIComponent(s.name)}/trash`),
        })),
    );
  }, [base]);

  if (error) return <div className="bad">Couldn't load: {error}</div>;
  if (!data) return <div className="dim">Loading…</div>;

  const act = async (share: string, it: TrashItem, restore: boolean) => {
    const path = `/api/admin/shares/${encodeURIComponent(share)}/trash/${it.id}`;
    try {
      if (restore) {
        const r = await call<{ path: string }>(base, "POST", path + "/restore");
        useStore.getState().say(`Restored to ${share}/${r.data.path}`);
      } else {
        if (!(await ask(`Permanently delete ${it.path}? This can't be undone.`, { title: "Delete forever", kind: "warning" }))) return;
        await call(base, "DELETE", path);
      }
      await reload();
    } catch (e) {
      useStore.getState().say("Failed: " + (e as Error).message);
    }
  };

  return (
    <div>
      <p className="dim small">Deleted and replaced files are kept for 30 days.</p>
      {data.map(({ share, items }) => (
        <div key={share}>
          <h3>{share}</h3>
          {items.length === 0 ? (
            <div className="dim small">Empty.</div>
          ) : (
            <table className="grid">
              <thead>
                <tr>
                  <th>Path</th>
                  <th>Deleted by</th>
                  <th>When</th>
                  <th className="num">Size</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {items.map((it) => (
                  <tr key={it.id}>
                    <td className="path">
                      {it.dir ? "\u25B8 " : ""}
                      {it.path}
                    </td>
                    <td className="small">{it.by}</td>
                    <td className="small nowrap">{fmtDate(it.at)}</td>
                    <td className="small num">{fmtSize(it.size)}</td>
                    <td className="right nowrap">
                      <button onClick={() => void act(share, it, true)}>Restore</button>{" "}
                      <button className="danger" onClick={() => void act(share, it, false)}>
                        Delete forever
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      ))}
    </div>
  );
}
