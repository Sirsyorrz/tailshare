# tailshare

Share folders with friends across tailnets. Friends open a web page through a shared
Tailscale node, and tailshare uses their Tailscale identity for per-folder permissions,
with no passwords. Not affiliated with Tailscale.

Early stage. The headless server, web UI and desktop app work; mounting as a
drive and released builds are still to come.

## Web UI

What friends see. It works like a file manager: details and thumbnail views,
sorting by name, date, type or size, type filters, search in the current
folder (Enter searches subfolders too), multi-select with Ctrl/Shift and
checkboxes, right-click menus, drag items onto folders or the breadcrumbs to
move them, a preview lightbox for images, video, audio and text, uploads of
files or whole folders (resumable, any size), and zip downloads of any
selection. Back/forward and reloads keep your place.

Thumbnails need download permission. Video thumbnails, and HEIC/AVIF photos,
need `ffmpeg` on the server's PATH; without it those show an icon.

## Desktop app

`app/` is a Tauri app that bundles the server. It can host from your computer
(as its own Tailscale machine, approved once in the browser), finds every
tailshare server on your tailnet, and gives admins a share editor (folders,
people, presets, upload limits, expiry), a People page with policy warnings,
the activity log and the trash.

```
cd app
npm install
npm run tauri dev
```

Needs Go, Rust and Node. `npm run server` builds the bundled server on its own.

"Keep hosting when tailshare is closed" moves the server into a systemd user
service (Linux, with lingering so it starts at boot) or a hidden logon task
(Windows). The app then follows it through `status.json` and `server.log` in
its state folder.

## Run the server

Needs Go 1.24+ and a tailnet with MagicDNS and HTTPS certificates enabled.

```
cd server
go build -o tailshare-server ./cmd/tailshare-server
./tailshare-server
```

On first run it prints a Tailscale login URL. Approve it and the node `tailshare`
joins your tailnet, then it serves `https://tailshare.<tailnet>.ts.net`.

State (node identity and `config.json`) lives in `~/.config/tailshare` unless
`-state` is given. Reload config with `kill -HUP <pid>`.

## Config

```json
{
  "admins": ["you@github"],
  "minFreeGB": 20,
  "shares": [
    {
      "name": "footage",
      "path": "/srv/shared/footage",
      "access": {
        "you@github": "editor",
        "id:1234567890": ["contributor", "deleteOwn"],
        "editor@example.com": ["view", "download", "upload"]
      },
      "quotaGB": { "id:1234567890": 500 },
      "expires": { "editor@example.com": "2026-12-31" }
    }
  ]
}
```

Users are Tailscale login names or `id:<userid>` (preferred, since login names can
change). The server logs each new identity it sees (`identity login=... id=...`),
and the admin Status page lists people who accepted a share with their `id:`.

| Preset | Permissions |
|---|---|
| `viewer` | view, download |
| `contributor` | view, download, upload, mkdir |
| `editor` | view, download, upload, mkdir, overwrite, rename, delete |
| `dropbox` | upload only |

Individual permissions: `view`, `download`, `upload`, `mkdir`, `overwrite`,
`rename`, `delete`, `deleteOwn` (only things they uploaded or created). Presets and
names can be mixed in a list.

| Option | Meaning |
|---|---|
| `admins` | Logins that get the admin pages (status, activity, trash). Only from devices in your own tailnet unless `allowExternalAdmins` is true. |
| `minFreeGB` | Refuse uploads that would leave less than this free on a share's disk. Default 5. |
| `readOnly` | Strip every write permission on the share. |
| `quotaGB` | Per user cap on what they have uploaded into the share. |
| `expires` | Last day (inclusive) of a user's access, or an RFC 3339 time. |

## Files on disk

Each share gets a hidden `.tailshare/` folder: unfinished uploads, trash (kept 30
days, restorable from the admin Trash page), `owners.json` (who uploaded what)
and cached thumbnails (remade after 60 days; read-only shares don't
cache).
The state directory holds the Tailscale node identity, `config.json` and
`activity.log` (one JSON object per line, append only).

## Sharing with a friend

1. In the Tailscale admin console, share the `tailshare` machine with them.
2. Allow shared users onto port 443 only, e.g. in the policy file:
   `{"src": ["autogroup:shared"], "dst": ["<tailshare 100.x IP>"], "ip": ["443"]}`
3. They open `https://tailshare.<your-tailnet>.ts.net` once. They show up on the
   admin Status page with their `id:`; add it to `access` and reload.

If someone has access but the policy blocks them, the server logs a warning and
the Status page marks them "blocked by tailnet policy". It works this out from the
access rules Tailscale sends the node, so no API key is needed.

`-roots /srv,/mnt/media` limits which folders the admin API may turn into shares
(default: your home folder). `kill -USR1 <pid>` dumps the peers and access rules the node currently sees.

## Dev mode

```
./tailshare-server -state /tmp/ts-dev -dev-listen 127.0.0.1:8765 -dev-user you@local
```

Plain HTTP on loopback, no Tailscale, every request is `-dev-user`.
