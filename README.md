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

`vender` is at **M4**: the configuration layer and share-link parser (M0), the
engine (M1), **smart routing out of the box** (M2), an **animated terminal UI**
(M3), and now **start at login on Linux and Windows 11**, **Windows system-proxy
control** and **release packaging** (M4). The rule sets the default policy
matches against are **embedded in the binary**, so a working default needs no
download at runtime; the TUI attaches to the running daemon over a local control
channel and can switch servers, change mode, start and stop the engine, tail its
log, show live traffic and turn start-at-login on and off.

Everything the CLI offers is implemented; there are no `not implemented` stubs
left.

## Roadmap

| Milestone | Contents |
|---|---|
| M0 | Module skeleton, config load/save, share-link parsing, `version` / `import` / `servers` CLI, docs |
| M1 | Engine: generate the sing-box config, supervise the child process, `daemon`, `status`, `proxy on\|off` |
| M2 | Built-in routing rules (CN direct, ad block, private IPs), split DNS, embedded rule sets, `rules list` |
| M3 | Control channel (loopback TCP + token, `control.json`), live traffic from sing-box's Clash API, animated Bubble Tea TUI (`vender tui`) |
| **M4** | **Autostart** (systemd user unit / XDG autostart on Linux; Task Scheduler logon task / Run key on Windows), **Windows system proxy** (WinINET), **release packaging** and a per-user installer |
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

### Release archives and the installer

```sh
scripts/package.sh              # dist/release/vender-<version>-{linux-amd64.tar.gz,windows-amd64.zip}
scripts/package.sh linux-amd64  # one target
scripts/install.sh              # install into ~/.local/bin (vender + sing-box)
scripts/install.sh --autostart  # ... and register it to start at login
scripts/install.sh --uninstall  # remove both, and the autostart entry
```

Each archive unpacks to `vender-<version>-<target>/` with the `vender` binary,
the matching **sing-box** binary, `README.md`, `LICENSE`, `NOTICE` and
`LICENSE.sing-box`. `package.sh` fetches the pinned sing-box automatically if
`dist/sing-box/` is empty (it is 80 MB and git-ignored, so a fresh clone has to
get it from somewhere).

`install.sh` needs no root: it copies both binaries into `~/.local/bin` — side by
side, which is one of the places vender looks for the engine — and can register
start-at-login in the same step. See [Licence](#licence) for why the sing-box
documentation travels with the binaries.

Dependencies are deliberately few: `cobra` and `yaml.v3` for the CLI, plus
`bubbletea`, `lipgloss` and `bubbles` for the TUI. The control channel,
autostart and the Windows registry work use only the standard library.

## Installing sing-box

A vender **release archive already contains** the matching sing-box binary
(`scripts/package.sh`), and `scripts/install.sh` copies it next to `vender`, so
a normal install needs nothing else. When building from source, fetch the pinned
release:

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
./vender tui                   # attach the terminal UI to the running daemon
./vender proxy on              # also point the desktop's system proxy at vender
./vender proxy off             # restore the system proxy

./vender autostart on          # run the daemon at login (systemd unit / Task Scheduler task)
./vender autostart status      # what is registered, and where (exit code!)
./vender autostart off         # remove the registration
```

Every command accepts `--config-dir <path>` (configuration) and `--run-dir
<path>` (runtime state) to point at alternative locations, which is also how
the test suite stays away from your real setup.

The first server imported also becomes the active one; removing the active
server clears the selection.

## System proxy

```sh
./vender proxy on     # point the desktop's proxy settings at 127.0.0.1:7897
./vender proxy off    # and restore them
```

This is a convenience for applications that honour the desktop proxy settings
but have no proxy option of their own. It only ever changes the *user's* proxy
configuration — no TUN, no VPN, no daemon state — and vender still recommends
configuring the proxy per application (or exporting `http_proxy` / `https_proxy`
/ `all_proxy`, which `proxy on` prints).

| Platform | What is written |
|---|---|
| Linux | the GNOME proxy schema through `gsettings` (`org.gnome.system.proxy.*`), with loopback in `ignore-hosts`; `off` restores `mode: none` |
| Windows | the per-user WinINET settings `HKCU\...\Internet Settings` (`ProxyEnable`, `ProxyServer` = `http=...;https=...;socks=...`, `ProxyOverride`), then `InternetSetOption` to make running applications re-read them; `off` clears only `ProxyEnable` |

Neither touches WinHTTP's own configuration (`netsh winhttp`), because a browser
does not use it.

## Start at login (autostart)

```sh
./vender autostart on          # register, and start it now
./vender autostart status      # what is registered, and where
./vender autostart status --json
./vender autostart off         # remove it
```

`on` and `off` are idempotent, `status` exits non-zero when nothing is
registered (so it drops straight into a health check), and the TUI's Settings
screen has the same thing on the `a` key — including when no daemon is running,
which is exactly when you tend to want it.

| Platform | Mechanism | Artifact |
|---|---|---|
| Linux | `systemd --user` unit (primary) | `~/.config/systemd/user/vender.service`, `ExecStart=<abs vender> daemon`, `Restart=on-failure`, `RestartSec=3`, `WantedBy=default.target`; then `systemctl --user enable --now` |
| Linux | XDG autostart desktop entry (fallback, used only when no systemd user manager answers) | `$XDG_CONFIG_HOME/autostart/vender.desktop` |
| Windows | Task Scheduler **logon task** (primary) | `\vender\daemon`, registered from XML: logon trigger, `InteractiveToken`, `LeastPrivilege`, hidden, no time limit |
| Windows | per-user Registry Run key (fallback when the task cannot be created) | `HKCU\...\CurrentVersion\Run`, value `vender` |

Neither platform needs administrator rights.

**Start at boot, not just at login** (Linux): the user manager is normally torn
down when your last session ends. `autostart on` asks logind for lingering
(`loginctl enable-linger`); if polkit refuses the non-interactive call the unit
is still correct and `status` says so in its `detail` line — run
`loginctl enable-linger` yourself to finish the job.

**Windows caveat.** The task is registered hidden and at the least privilege,
but vender is a *console-subsystem* executable, so Task Scheduler does not
guarantee a fully invisible start — a console window can flash at logon. A
windowless start needs a GUI-subsystem build or a `conhost --headless` launcher
and is deferred to M6; the daemon itself does not need a console. The Windows
paths are compile-checked (`GOOS=windows go build ./...`) and their XML, argument
vectors and output parsing are unit-tested, but they cannot be *executed* on a
Linux development host.

The entry only ever runs `vender daemon`, which is headless: one daemon per run
directory, a fresh control token per start, and a clean exit on `SIGTERM`.

## The terminal UI

```
./vender tui
```

An animated Bubble Tea interface with four screens:

| Key | Screen | What it shows |
|---|---|---|
| `1` | **Dashboard** | daemon and engine state, the selected server, the mode, uptime, and a **live up/down traffic graph** with human-readable rates and session totals |
| `2` | **Nodes** | every configured server with its type, protocol, endpoint and latency. `enter` makes one active (the daemon restarts the engine in place), `t` measures latency |
| `3` | **Logs** | the engine's own output, streamed live, with follow mode (`f`) and a filter (`/`) |
| `4` | **Settings** | the routing mode, the listener, a read-only view of the **active built-in policy**, and the **start-at-login registration** (`a` toggles it) |

Other keys: `tab` / `shift+tab` between screens, `?` for help, `t` to test
latency, `s` / `x` / `R` to start, stop and restart the engine, `m` to cycle
the mode, `a` on Settings to toggle start-at-login, `q` or Ctrl-C to quit.

The TUI is a **client**: it never runs sing-box itself, it asks the daemon. So
it can attach and detach at any time, and two of them can be open at once. It
also **does not need a daemon to start** — Nodes and Settings are built from
your configuration files, the Dashboard prints the exact command to start the
daemon, and attaching is retried once a second.

Traffic and latency come from sing-box's own experimental Clash API, which
vender switches on for its daemon on a loopback address with a generated
secret. Nothing outside the daemon talks to it.

### How the TUI talks to the daemon

The daemon publishes a small endpoint at `<run-dir>/control.json` (mode `0600`)
holding a loopback TCP address and a random token, regenerated on every start
and deleted on exit. Clients speak newline-delimited, versioned JSON over that
connection: `status`, `servers`, `select`, `mode`, `latency`, `start`, `stop`,
`restart` and `rules` are request/response, while `logs` and `traffic` are
subscriptions that take their connection over. A request without the token is
rejected. See `docs/ARCHITECTURE.md` §8 for the protocol and for why it is
loopback TCP rather than a unix socket.

`vender tui` piped somewhere (no terminal) prints a one-line-per-field snapshot
instead of failing.

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
   `block` outbound, the built-in rules, their actions, the split DNS policy and
   the experimental Clash API (loopback, with a generated secret) the TUI reads
   live traffic from;
5. starts `sing-box run -c <run dir>/singbox.json` in its own process group and
   returns only once the inbound port actually accepts TCP connections;
6. publishes its control endpoint as `<run dir>/control.json` (`0600`) and
   serves it, so `vender tui` can attach;
7. streams sing-box's output to its own standard output, prefixed `sing-box:`,
   and to every attached client;
8. on `SIGINT`/`SIGTERM`, stops sing-box gracefully (`SIGTERM`, then `SIGKILL`
   after a grace period), removes the pid file and the control endpoint, and
   releases the lock.

While it runs, a client can also switch the active server, change the routing
mode, and start/stop/restart the engine in place — the daemon re-renders the
configuration and restarts sing-box without the daemon itself going away.

`vender status` reads the same lock, so its exit code means exactly "the proxy
is up" and is safe to use in scripts or a health check.

## Licence

`vender` is licensed under the **GNU General Public License, version 3 or
later** — see [`LICENSE`](LICENSE). That choice keeps distribution simple,
because vender runs and bundles the GPL-licensed [sing-box](https://github.com/SagerNet/sing-box)
engine, and the two are then under the same terms.

vender does not link sing-box as a library; it executes it as a separate
process. Release archives still ship the engine next to the binary for a
self-contained install, so they carry sing-box's own licence text as
`LICENSE.sing-box` and a [`NOTICE`](NOTICE) that spells out the bundling, the
upstream copyright, the one extra term upstream adds, the pinned version, and
where the corresponding source lives. Nothing is silently dropped.

No credential ever lives in the repository: `servers.yaml` is written `0600`,
the generated `singbox.json` is `0600`, and the committed tests use only dummy
values.
