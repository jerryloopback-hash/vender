# vender

A lightweight, cross-platform personal proxy client for **Linux** and **Windows 11**.

`vender` is a thin, friendly control layer around [sing-box](https://sing-box.sagernet.org/),
which it runs as a managed child process. It exposes a local mixed
**SOCKS5 + HTTP** inbound (default `127.0.0.1:7897`) that any application can use.
There is **no TUN mode** — nothing is routed system-wide, so the proxy is opt-in
per application and easy to reason about.

Two outbound protocols are supported:

| Type | Transport | Notes |
|---|---|---|
| `vless` | TCP | VLESS + Vision + REALITY (also plain `tls` / `none`) |
| `hysteria2` | UDP | Hysteria2, optionally with `salamander` obfuscation |

The pinned engine is **sing-box 1.14.3** (see `scripts/fetch-sing-box.sh`).

## Status

`vender` is at **M2**: the configuration layer and share-link parser (M0) and the
engine (M1) are complete, and **smart routing now works out of the box** —
`proxy.mode: rule` sends Chinese and private destinations direct, blocks ads and
trackers, forces a short list of foreign services through the proxy, and resolves
DNS along the same split. The rule sets it matches against are **embedded in the
binary**, so a working default needs no download at runtime. Commands that belong
to a later milestone are present but exit with an explicit `not implemented in
this milestone (Mx)` message.

## Roadmap

| Milestone | Contents |
|---|---|
| M0 | Module skeleton, config load/save, share-link parsing, `version` / `import` / `servers` CLI, docs |
| M1 | Engine: generate the sing-box config, supervise the child process, `daemon`, `status`, `proxy on\|off` |
| **M2** | Built-in routing rules (CN direct, ad block, private IPs), split DNS, embedded rule sets, `rules list` |
| M3 | Bubble Tea animated TUI that attaches to the running daemon |
| M4 | Autostart (systemd user unit / XDG autostart on Linux; Registry Run key / Task Scheduler on Windows), Windows system-proxy control |
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

## Installing sing-box

`vender` does not embed the proxy engine; it runs a sing-box binary it finds at
runtime. Fetch the pinned release:

```sh
scripts/fetch-sing-box.sh            # linux/amd64 + windows/amd64
scripts/fetch-sing-box.sh linux-amd64
scripts/fetch-sing-box.sh --print-config   # show the engine.binary line to use
```

The version and the SHA-256 of both archives are pinned inside the script, so
the download is reproducible and a substituted upstream release is rejected.
The result lands (git-ignored) in:

```
dist/sing-box/linux-amd64/sing-box
dist/sing-box/windows-amd64/sing-box.exe
```

`vender` looks for the engine in this order:

1. `engine.binary` in `config.yaml`;
2. a `sing-box` (`sing-box.exe` on Windows) **next to the vender executable** —
   copy the fetched files there for a self-contained install;
3. `sing-box` on `PATH`.

If none is found, the error names all three locations.

## Usage

```sh
# Add a server from a share link (the name comes from the link's #fragment)
./vender import 'vless://<uuid>@<host>:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=<sni>&fp=chrome&pbk=<public-key>&sid=<short-id>&type=tcp#My-Node'
./vender import 'hysteria2://<password>@<host>:8443/?sni=<sni>&obfs=salamander&obfs-password=<secret>&insecure=1#My-Node-Hy2'

./vender servers list          # table of configured servers, * marks the active one
./vender servers list --links  # print the share link of every server (contains secrets)
./vender servers rm My-Node    # remove a server by name
./vender rules list            # the built-in routing policy and the embedded rule sets
./vender version               # build information

./vender daemon                # run the proxy (foreground; Ctrl-C to stop)
./vender status                # is it running, and is the port accepting? (exit code!)
./vender proxy on              # also point the desktop's system proxy at vender
./vender proxy off             # restore the system proxy
```

Every command accepts `--config-dir <path>` (configuration) and `--run-dir
<path>` (runtime state) to point at alternative locations, which is also how
the test suite stays away from your real setup.

The first server imported also becomes the active one; removing the active
server clears the selection.

### Behind an existing VPN or TUN

If a VPN/TUN interface has a higher route metric than your physical link, the
connection to the proxy server is swallowed by it and the handshake fails with
something like `REALITY authentication failed` or `received real certificate`.
Pin the outbound to the real interface:

```yaml
engine:
    bind_interface: wlp0s20f3      # from `ip -brief addr`
```

`bind_interface` is passed straight through to sing-box and only affects the
connection to the proxy server, not the local inbound.

## Configuration

Configuration lives in the platform user config directory:

| Platform | Location |
|---|---|
| Linux | `~/.config/vender/` (or `$XDG_CONFIG_HOME/vender`) |
| Windows | `%AppData%\vender\` |

Runtime state — the generated `singbox.json`, the daemon lock and its pid file —
lives in the platform **cache** directory instead, so nothing writes into your
editable configuration:

| Platform | Location |
|---|---|
| Linux | `~/.cache/vender/` (or `$XDG_CACHE_HOME/vender`) |
| Windows | `%LocalAppData%\vender\` |

Two files live in the config directory:

- `config.yaml` — global settings (world-readable, no secrets):

  ```yaml
  log_level: info
  proxy:
      listen: 127.0.0.1:7897
      mode: rule          # rule | global | direct
  engine:
      binary: ""          # empty => next to vender, else PATH
      bind_interface: ""  # e.g. eth0 on a multi-homed host
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

## Routing rules

`proxy.mode: rule` (the default) applies a small, built-in policy. It is derived
from a hand-written configuration that carried ~9500 individual rules — 9018 of
them `DOMAIN-SUFFIX` — and deliberately **aggregates the same intent into
geosite/geoip categories** instead of embedding thousands of domains that would
rot the moment nobody looks at them.

The policy, in evaluation order (first match wins):

| # | Rule | Matches | Goes to |
|---|---|---|---|
| 1 | `private-dest-direct` | `ip_is_private` (RFC1918, loopback, link-local) | `direct` |
| 2 | `ads-block` | `geosite-category-ads-all` | `block` |
| 3 | `service-overrides-proxy` | Steam, OneDrive, Microsoft, Google, YouTube, Telegram, Netflix, OpenAI, Bahamut | `proxy` |
| 4 | `cn-direct` | `geosite-cn`, `geoip-cn` | `direct` |
| — | `route.final` | everything else | `proxy` |

Two things about the order are load-bearing:

- **The service overrides must come before the China rule.** Several of those
  services have China-facing entries in `geosite-cn`; if the China rule ran
  first, a Steam store page or a Microsoft sign-in would be dialled directly and
  fail. The overrides are a short, commented list in
  [`internal/rules/rules.go`](internal/rules/rules.go) precisely so that adding
  one is a deliberate act.
- **Private destinations are matched by destination only.** Matching the *source*
  would be meaningless — the local inbound only ever accepts connections from
  localhost, so `source_ip_is_private` would match everything and send all
  traffic direct.

Sniffing is what makes domain rules work for connections that arrive as bare
IPs, and DNS is resolved along the same split as the traffic:

```
geosite-cn            -> dns-cn      223.5.5.5 over UDP, direct
everything else       -> dns-remote  Cloudflare DoH, through the proxy
```

Both are emitted as sing-box 1.14 *actions* (`{"action":"sniff"}`,
`{"action":"hijack-dns"}`) plus a `dns` block; the legacy inbound `sniff` field
the pinned engine removed is never written. The proxied resolver is addressed by
IP and reached through the proxy outbound, so a foreign query never appears on
the local network.

`vender rules list` prints exactly this policy, the DNS split and the rule sets
below.

### Embedded rule sets ("全部内置")

The rule sets are **compiled sing-box rule sets (`.srs`) embedded in the vender
binary** with `go:embed`. Nothing is downloaded at run time and no data
directory is needed: at start-up the daemon writes the embedded bytes to
`<run dir>/rules/` (the only thing sing-box's `route.rule_set` can read) and
points the generated config at them.

| Rule set | Kind | Size |
|---|---|---|
| `geosite-cn` | geosite | 443,264 B |
| `geoip-cn` | geoip | 37,245 B |
| `geosite-google` | geosite | 7,991 B |
| `geosite-category-ads-all` | geosite | 7,394 B |
| `geosite-microsoft` | geosite | 6,956 B |
| `geosite-youtube` | geosite | 1,144 B |
| `geosite-steam` | geosite | 800 B |
| `geosite-openai` | geosite | 435 B |
| `geosite-netflix` | geosite | 344 B |
| `geosite-onedrive` | geosite | 272 B |
| `geosite-telegram` | geosite | 248 B |
| `geosite-bahamut` | geosite | 121 B |
| **total** | | **506,214 B (494.3 KiB)** |

Provenance:

- **Source** `MetaCubeX/meta-rules-dat`, branch `sing`, layout
  `geo/geosite/<name>.srs` and `geo/geoip/<name>.srs`. It ships both families in
  one repository at one commit, which is why it is preferred over
  `SagerNet/sing-geosite` + `sing-geoip`.
- **Pinned** to the commit `d1c433471f5b` (2026-10-15) — a commit, not a branch
  and not the moving `latest` tag.
- **Verified**: `scripts/fetch-rules.sh` pins the SHA-256 of every file and
  checks it on download *and* on `--check`; a unit test re-hashes the embedded
  bytes so a corrupted asset fails `go test`, not a user's connection.

The `.srs` files are **committed** to the repository. That is a deliberate
choice over generating an embedded `.go` at fetch time: `go build ./...` then
works from a clean clone with no extra step, `go test` can verify the assets,
and a diff of the binary assets shows up in review.

```sh
scripts/fetch-rules.sh              # re-fetch into internal/rules/assets/
scripts/fetch-rules.sh --list       # the pinned table (tag, source, sha256)
scripts/fetch-rules.sh --check      # verify the committed files against it
scripts/fetch-rules.sh --print-size # the embedded total
```

### What is deliberately not here yet

- **One outbound.** There is a single `proxy` outbound. Proxy groups, node
  selection by rule and per-rule outbound choice are a later milestone.
- **No per-user rules file.** A file to add or override entries would slot in
  exactly where `rules.BuiltinRules()` returns its slice; the ordering, the
  conversion and the CLI listing already work off that slice.
- **No `mode: global` rules.** `global` means everything through the proxy, so
  it emits no routing rules at all — only a proxied DNS resolver. `direct`
  emits no proxy outbound and no DNS block.

## What the daemon does

`vender daemon` is the single source of truth. For one run it:

1. reads `config.yaml` + `servers.yaml` and validates the selected server;
2. takes an advisory lock on `<run dir>/vender.lock` — a second daemon on the
   same run dir fails immediately, and the lock is released by the OS even if
   the daemon is killed, so it can never go stale;
3. in rule mode extracts the embedded rule sets into `<run dir>/rules/` (before
   the config that references them);
4. renders `singbox.json` **atomically** (`0600`, since it holds credentials)
   with one `mixed` inbound, the selected outbound, a `direct` outbound, a
   `block` outbound, the built-in rules, their actions and the split DNS policy;
5. starts `sing-box run -c <run dir>/singbox.json` in its own process group and
   returns only once the inbound port actually accepts TCP connections;
6. streams sing-box's output to its own standard output, prefixed `sing-box:`;
7. on `SIGINT`/`SIGTERM`, stops sing-box gracefully (`SIGTERM`, then `SIGKILL`
   after a grace period), removes the pid file and releases the lock.

`vender status` reads the same lock, so its exit code means exactly "the proxy
is up" and is safe to use in scripts or a health check.

## License

Personal project; not yet licensed for redistribution.
