import { fmtDate, fmtSize, get, type ActivityEntry } from "../lib/api";
import { useLoad } from "../lib/hooks";

const speed = (e: ActivityEntry) => (e.bytes && e.seconds ? fmtSize(e.bytes / Math.max(e.seconds, 0.001)) + "/s" : "");

export function Activity({ base }: { base: string }) {
  const { data, error, reload, loading } = useLoad(() => get<ActivityEntry[]>(base, "/api/admin/activity?n=500"), [base]);
  if (error) return <div className="bad">Couldn't load: {error}</div>;
  if (!data) return <div className="dim">Loading…</div>;
  return (
    <div>
      <div className="row">
        <span className="dim small">Newest first. The full log is activity.log in the server's state folder.</span>
        <span className="spacer" />
        <button onClick={() => void reload()} disabled={loading}>
          Refresh
        </button>
      </div>
      {data.length === 0 ? (
        <p className="dim">No activity yet.</p>
      ) : (
        <table className="grid">
          <thead>
            <tr>
              <th>When</th>
              <th>Who</th>
              <th>What</th>
              <th>Share</th>
              <th>Path</th>
              <th className="num">Size</th>
              <th className="num">Speed</th>
            </tr>
          </thead>
          <tbody>
            {data.map((e, i) => (
              <tr key={i}>
                <td className="small nowrap">{fmtDate(e.time)}</td>
                <td className="small">{e.login}</td>
                <td className="small">
                  {e.action}
                  {e.note && <div className="dim">{e.note}</div>}
                </td>
                <td className="small">{e.share}</td>
                <td className="small path" title={e.to ? `${e.path} → ${e.to}` : e.path}>
                  {e.to ? `${e.path} → ${e.to}` : e.path}
                </td>
                <td className="small num">{e.bytes ? fmtSize(e.bytes) : ""}</td>
                <td className="small num">{speed(e)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
