# tailshare

Share folders with friends across tailnets. Friends open a web page through a shared
Tailscale node, and tailshare uses their Tailscale identity for per-folder permissions,
with no passwords. Not affiliated with Tailscale.

Early prototype: the headless server and web UI only.

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
  "shares": [
    {
      "name": "footage",
      "path": "/srv/shared/footage",
      "access": {
        "you@github": "editor",
        "friend@example.com": "contributor",
        "editor@example.com": ["view", "download", "upload"]
      }
    }
  ]
}
```

Users are Tailscale login names or `id:<userid>`. Access is a preset or a list.

| Preset | Permissions |
|---|---|
| `viewer` | view, download |
| `contributor` | view, download, upload, mkdir |
| `editor` | everything |
| `dropbox` | upload only |

Individual permissions: `view`, `download`, `upload`, `mkdir`, `overwrite`,
`rename`, `delete`. `"readOnly": true` on a share strips all write permissions.

## Sharing with a friend

1. In the Tailscale admin console, share the `tailshare` machine with them.
2. Allow shared users onto port 443 only, e.g. in the policy file:
   `{"src": ["autogroup:shared"], "dst": ["<tailshare 100.x IP>"], "ip": ["443"]}`
3. Add their login to `access` in the config and reload.
4. They open `https://tailshare.<your-tailnet>.ts.net`.

The server logs each new identity it sees (`identity login=...`), which shows the
login name to put in the config.

## Dev mode

```
./tailshare-server -state /tmp/ts-dev -dev-listen 127.0.0.1:8765 -dev-user you@local
```

Plain HTTP on loopback, no Tailscale, every request is `-dev-user`.
