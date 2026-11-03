import { useEffect, useState } from "react";
import { get, type Browse } from "../lib/api";

/// Browses folders on the server itself (not this computer), limited to
/// the server's allowed roots.
export function FolderPicker({
  base,
  start,
  onPick,
  onClose,
}: {
  base: string;
  start?: string;
  onPick: (path: string) => void;
  onClose: () => void;
}) {
  const [at, setAt] = useState<Browse | null>(null);
  const [error, setError] = useState("");

  const go = async (path: string) => {
    try {
      setAt(await get<Browse>(base, "/api/admin/browse?path=" + encodeURIComponent(path)));
      setError("");
    } catch (e) {
      setError(String((e as Error).message));
      if (path) void go("");
    }
  };

  useEffect(() => {
    void go(start ?? "");
  }, []);

  const join = (dir: string) => (at?.path ? at.path.replace(/[\\/]$/, "") + (at.path.includes("\\") ? "\\" : "/") + dir : dir);

  return (
    <div className="modal-back" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-h">
          <b>Pick a folder to share</b>
          <span className="spacer" />
          <button onClick={onClose}>Cancel</button>
        </div>
        <div className="modal-body pad">
          <div className="row">
            <code className="grow">{at?.path || "Allowed folders"}</code>
            {at?.parent !== undefined && <button onClick={() => void go(at.parent!)}>Up</button>}
            {at?.path && !at.parent && <button onClick={() => void go("")}>Up</button>}
          </div>
          {error && <div className="bad small">{error}</div>}
          <ul className="picker">
            {at?.dirs.map((d) => (
              <li key={d} onDoubleClick={() => void go(join(d))} onClick={() => void go(join(d))}>
                {"\u25B8 "}
                {d}
              </li>
            ))}
            {at && at.dirs.length === 0 && (
              <li className="dim">{at.path ? "No folders inside this one." : "No allowed folders yet. Add one in Settings."}</li>
            )}
          </ul>
          <div className="dim small">
            Only folders inside the server's allowed locations are shown. For this computer, change them in Settings.
          </div>
        </div>
        <div className="modal-foot">
          <span className="spacer" />
          <button className="primary" disabled={!at?.path} onClick={() => at?.path && onPick(at.path)}>
            Share this folder
          </button>
        </div>
      </div>
    </div>
  );
}
