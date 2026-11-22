# tailshare

Share folders with friends across tailnets. Friends open a web page through a
shared Tailscale node, and tailshare uses their Tailscale identity for
per-folder permissions. No accounts, no passwords.

Not affiliated with Tailscale.

> Early stage. The server, web UI and desktop app (including mounting shares as
> drives) work

## How it works

The server runs as its own machine on your tailnet (`tailshare` by default) and
serves `https://tailshare.<tailnet>.ts.net`. You share that machine with friends
from the Tailscale admin console; when they open the page, Tailscale tells the
server who they are, and the config decides what they can do in each folder.

## Web UI

What friends see. It works like a file manager:

- details and thumbnail views, sorting, type filters
- search in the current folder (Enter searches subfolders too)
- multi-select (Ctrl/Shift, checkboxes), right-click menus, drag to move
- preview for images, video, audio and text
- resumable uploads of files or whole folders, any size
- zip downloads of any selection
- changes made elsewhere (drives, other people, the server's disk) show up
  within a few seconds

Thumbnails need download permission. Video thumbnails and HEIC/AVIF photos need
ffmpeg on the server; without it those show an icon. When hosting from the
desktop app, "Video thumbnails" on the Host page downloads it (LGPL build,
about 65 to 80 MB). A headless server uses `-ffmpeg` or the one on PATH.

## Desktop app

`app/` is a Tauri app that bundles the server. It can:

- host from your computer as its own Tailscale machine (approved once in the browser)
- find every tailshare server on your tailnet
- edit shares: folders, people, presets, upload limits, expiry
- show people, policy warnings, the activity log and the trash
- mount shares as drives

"Keep hosting when tailshare is closed" moves the server into a systemd user
service (Linux, with lingering so it starts at boot) or a hidden logon task
(Windows).

### Drives

Any share you can view and download from can be mounted from "Your access" →
Mount: a drive letter on Windows, an empty folder on Linux. Any program can then
open and save files on it directly. The app bundles [rclone](https://rclone.org),
which talks WebDAV to the server's `/dav/` endpoint under the same permissions,
quotas, trash and activity log as the web UI.

- Opened files are cached locally (20 GB by default, set on the Drives page).
- Saves upload in the background a few seconds after the file is closed. The
  "Drives & transfers" page shows progress, the queue and anything the server
  refused.
- Unfinished uploads survive unmounting, quitting and crashes.
- Changes by other people show up within about 20 seconds.

Mounting uses a filesystem driver from the OS: [WinFsp](https://winfsp.dev) on
Windows, FUSE (`fuse3`) on Linux. Nothing else in the app needs them. If
WinFsp is missing, the app offers to download and install it (one UAC prompt).
FUSE comes with most desktop distros; if it's missing, the app shows the
install command.

Upload-only shares can't be mounted. Moving files between two shares is a copy
plus delete.

## Sharing with a friend

1. In the Tailscale admin console, share the `tailshare` machine with them.
2. They open `https://tailshare.<your-tailnet>.ts.net` once. They then show up
   in the app's People page (or the admin Status page) with their `id:`; give
   them access there or add them to `access` in the config.

The default tailnet policy lets shared users in, so no policy change is needed.

### What a shared user can reach

tailshare runs as its own Tailscale machine inside the server process, with its
own network stack. Sharing it exposes only that machine, not the computer it
runs on or the rest of your tailnet, and it only listens on 443 (tailshare) and
80 (redirects to 443). There is no Tailscale SSH and nothing is forwarded to
the host. Once connected, people only see the shares and permissions you gave
them, and file access is confined to each share's folder.

### Tailnet policy (optional)

Only needed if your policy is stricter than the default, or you want to be
extra safe. Add a grant that lets shared users reach tailshare's HTTPS port:

```json
{"src": ["autogroup:shared"], "dst": ["<tailshare 100.x IP>"], "ip": ["tcp:443"]}
```

The desktop app and server log suggest the exact grant for your node. Grants
only add access, so to limit shared users to this grant also change any
`"src": ["*"]` rule to `"src": ["autogroup:member"]`.

If someone has access but the policy blocks them, they're flagged as "blocked by
tailnet policy". The server works this out from the packet filter Tailscale
sends the node, so no API key is needed.

## Headless server

Needs Go 1.27+ and a tailnet with MagicDNS and HTTPS certificates enabled.

```sh
cd server
go build -o tailshare-server ./cmd/tailshare-server
./tailshare-server -bootstrap-admin you@github
```

On first run it prints a Tailscale login URL. Approve it and the node joins your
tailnet.

| Flag | Meaning |
|---|---|
| `-state <dir>` | State directory (default `~/.config/tailshare`) |
| `-config <file>` | Config file (default `<state>/config.json`) |
| `-hostname <name>` | Tailscale machine name (default `tailshare`) |
| `-roots a,b` | Folders the admin API may turn into shares (default: home folder) |
| `-roots-file <file>` | More allowed folders, one per line, re-read on every use |
| `-bootstrap-admin <user>` | Admin to add when the config has none |
| `-ffmpeg <path>` | ffmpeg for thumbnails, tried before PATH |

On Linux/macOS, `kill -HUP <pid>` reloads the config and `kill -USR1 <pid>`
dumps the peers and access rules the node currently sees.

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

Users are Tailscale login names or `id:<userid>`. Prefer `id:`, since login
names can change. The server logs each new identity it sees
(`identity login=... id=...`).

| Preset | Permissions |
|---|---|
| `viewer` | view, download |
| `contributor` | view, download, upload, mkdir |
| `editor` | view, download, upload, mkdir, overwrite, rename, delete |
| `dropbox` | upload only |

Individual permissions: `view`, `download`, `upload`, `mkdir`, `overwrite`,
`rename`, `delete`, `deleteOwn` (only things they uploaded or created). Presets
and permissions can be mixed in a list.

| Option | Meaning |
|---|---|
| `admins` | Users who get the admin pages. Only from devices in your own tailnet unless `allowExternalAdmins` is true. |
| `minFreeGB` | Refuse uploads that would leave less than this free on a share's disk. Default 5. |
| `readOnly` | Strip every write permission on the share. |
| `quotaGB` | Per-user cap on what they've uploaded into the share. |
| `expires` | Last day (inclusive) of a user's access, or an RFC 3339 time. |

## Files on disk

Nothing is written into shared folders. Each one gets a data folder in the
state directory, `shares/<id>/`, holding:

- unfinished uploads
- trash, kept 30 days and restorable from the admin Trash page
- `owners.json`, who uploaded what
- cached thumbnails, remade after 60 days

When a share is removed or moved, its data folder is deleted, trash included
(the app warns first). The shared files themselves are never touched.

Uploads and trash move by rename when the share is on the same disk as the
state directory. Otherwise they are copied, so that disk needs room for
unfinished uploads and for anything in the trash; a delete that wouldn't fit
is refused. Shares from older versions have their `.tailshare/` folder moved
out on start.

The state directory holds the Tailscale node identity, `config.json` and
`activity.log` (append-only, one JSON object per line).

## Development

```sh
cd app
npm install
npm run rclone      # fetch the bundled rclone (checksums pinned in scripts/fetch-rclone.mjs)
npm run tauri dev
```

Needs Go, Rust and Node. `npm run server` builds the bundled server on its own.

To run the server without Tailscale:

```sh
./tailshare-server -state /tmp/ts-dev -dev-listen 127.0.0.1:8765 -dev-user you@local
```

Plain HTTP on loopback; every request is treated as `-dev-user`.

## License

MIT, see [LICENSE](LICENSE). Bundled and downloadable components (rclone,
WinFsp, FFmpeg) are under their own licences, listed in
[LICENSE-THIRD-PARTY.md](LICENSE-THIRD-PARTY.md).
