# vender

A lightweight, cross-platform personal proxy client for **Linux** and **Windows 11**.

`vender` is a thin, friendly control layer around [sing-box](https://sing-box.sagernet.org/),
which it runs as a managed child process. It exposes a local mixed
**SOCKS5 + HTTP** inbound (default `127.0.0.1:7890`) that any application can use.
There is **no TUN mode** — nothing is routed system-wide, so the proxy is opt-in
per application and easy to reason about.

Two outbound protocols are supported:

| Type | Transport | Notes |
|---|---|---|
| `vless` | TCP | VLESS + Vision + REALITY (also plain `tls` / `none`) |
| `hysteria2` | UDP | Hysteria2, optionally with `salamander` obfuscation |

## Status

`vender` is at **M0**: the CLI surface, configuration layer and share-link
parser are complete and tested. The proxy engine itself arrives in M1 — see
the roadmap below. Commands that belong to a later milestone are present but
exit with an explicit `not implemented in this milestone (Mx)` message.

## Roadmap

| Milestone | Contents |
|---|---|
| **M0** | Module skeleton, config load/save, share-link parsing, `version` / `import` / `servers` CLI, docs |
| M1 | Engine: generate the sing-box config, supervise the child process, `daemon`, `status`, `proxy on\|off` |
| M2 | Built-in routing rules (CN direct, ad block, private IPs) and `proxy.mode: rule` |
| M3 | Bubble Tea animated TUI that attaches to the running daemon |
| M4 | Autostart (systemd user unit / XDG autostart on Linux; Registry Run key / Task Scheduler on Windows) |
| M5 | Subscription links and bulk import, server latency testing |
| M6 | Hardening & release: health-based failover, log rotation, signed builds |

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the design and the
interfaces between milestones.

## Build

Requires **Go 1.26+**.

```sh
go build ./...                 # compile everything
go build -o vender ./cmd/vender
go test ./...                  # unit tests
go vet ./...                   # static checks
```

Cross-compile for Windows:

```sh
GOOS=windows GOARCH=amd64 go build -o vender.exe ./cmd/vender
```

`scripts/build.sh` builds both targets into `dist/` with version metadata
injected via `-ldflags`.

## Usage

```sh
# Add a server from a share link (the name comes from the link's #fragment)
./vender import 'vless://<uuid>@<host>:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=<sni>&fp=chrome&pbk=<public-key>&sid=<short-id>&type=tcp#My-Node'
./vender import 'hysteria2://<password>@<host>:8443/?sni=<sni>&obfs=salamander&obfs-password=<secret>&insecure=1#My-Node-Hy2'

./vender servers list          # table of configured servers, * marks the active one
./vender servers list --links  # print the share link of every server (contains secrets)
./vender servers rm My-Node    # remove a server by name
./vender version               # build information
```

The first server imported also becomes the active one; removing the active
server clears the selection.

Any command accepts `--config-dir <path>` to point at an alternative
configuration directory (useful for testing and for portable installs).

## Configuration

Configuration lives in the platform user config directory:

| Platform | Location |
|---|---|
| Linux | `~/.config/vender/` (or `$XDG_CONFIG_HOME/vender`) |
| Windows | `%AppData%\vender\` |

Two files live there:

- `config.yaml` — global settings (world-readable, no secrets):

  ```yaml
  log_level: info
  proxy:
      listen: 127.0.0.1:7890
      mode: rule          # rule | global | direct
  engine:
      binary: ""
  selected_server: ""
  ```

- `servers.yaml` — the server list, **written with mode `0600`** because it
  holds credentials (UUIDs, passwords, REALITY public keys). Managed by
  `vender import` and `vender servers rm`; hand-editing is supported:

  ```yaml
  servers:
      - name: My-Node
        type: vless
        server: 203.0.113.10
        port: 443
        uuid: 11111111-1111-1111-1111-111111111111
        flow: xtls-rprx-vision
        security: reality
        sni: www.example.com
        fingerprint: chrome
        public_key: DUMMY-PUBLIC-KEY
        short_id: 0123456789abcdef
        network: tcp
        header_type: none
  ```

Hand-edited files are validated on load, and problems are reported all at once
with the field that is wrong.

## License

Personal project; not yet licensed for redistribution.
