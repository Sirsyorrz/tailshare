import { openUrl } from "@tauri-apps/plugin-opener";
import { writeText } from "../lib/clipboard";
import { useStore } from "../state/store";

export const ACCESS_CONTROLS_URL = "https://console.tailscale.com/admin/acls/file";

export function PolicyGrant({ grant }: { grant: string }) {
  return (
    <>
      <pre className="snippet">{grant}</pre>
      <div className="row">
        <button onClick={() => void writeText(grant).then(() => useStore.getState().say("Copied"))}>Copy</button>
        <button onClick={() => void openUrl(ACCESS_CONTROLS_URL)}>Open access controls</button>
      </div>
    </>
  );
}
