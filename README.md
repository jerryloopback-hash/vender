# vender

A lightweight, cross-platform personal proxy client for **Linux** and **Windows 11**.

`vender` is a thin, friendly control layer around [sing-box](https://sing-box.sagernet.org/),
which it runs as a managed child process. It exposes a local mixed
**SOCKS5 + HTTP** inbound (default `127.0.0.1:7897`) that any application can use,
and it can *additionally* be switched into an optional **TUN mode** that captures
the host's traffic system-wide. TUN mode is **off by default** and the local
inbound is always there, so the usual way to use vender is still opt-in per
application and easy to reason about.

Two outbound protocols were the original scope; **M7 adds the three the rest of
the world uses** — the ones a commercial provider ("机场") hands out:

| Type | Transport | Notes |
|---|---|---|
| `vless` | TCP | VLESS + Vision + REALITY (also plain `tls` / `none`) |
| `hysteria2` | UDP | Hysteria2, optionally with `salamander` obfuscation |
| `shadowsocks` | TCP | `ss://` in both spellings (SIP002 and fully-base64), including the `2022-blake3-*` ciphers. SIP003 plugins are rejected with a clear message |
| `vmess` | TCP / WS / gRPC | `vmess://` (the v2rayN base64 JSON), with optional TLS |
| `trojan` | TCP / WS / gRPC | `trojan://`, always over TLS |

The pinned engine is **sing-box 1.14.3** (see `scripts/fetch-sing-box.sh`).

## Status

`vender` is at **M7**: the configuration layer and share-link parser (M0), the
engine (M1), **smart routing out of the box** (M2), an **animated terminal UI**
(M3), **start at login on Linux and Windows 11**, **Windows system-proxy
control** and **release packaging** (M4), an optional **TUN mode** — transparent,
system-wide capture on top of the same engine and the same policy (M6), and now
**subscription import, named profiles and the three remaining outbound
protocols** (M7).

M5 hardens the whole thing against a bad link: a tiny **critical list** and an
optional **`rules.yaml`** that no proxy can cut off, a **DNS split that never
depends on the proxy** (FakeIP for proxied names, direct resolution for direct
ones), and **`selected_server: auto`** node failover. The rule sets the default
policy matches against are **embedded in the binary**, so a working default needs
no download at runtime; the TUI attaches to the running daemon over a local
control channel and can switch servers, change mode, switch profile, turn TUN
mode on and off, start and stop the engine, tail its log, show live traffic and
toggle start-at-login.

Everything the CLI offers is implemented; there are no `not implemented` stubs
left.

## Roadmap

| Milestone | Contents |
|---|---|
| M0 | Module skeleton, config load/save, share-link parsing, `version` / `import` / `servers` CLI, docs |
| M1 | Engine: generate the sing-box config, supervise the child process, `daemon`, `status`, `proxy on\|off` |
| M2 | Built-in routing rules (CN direct, ad block, private IPs), split DNS, embedded rule sets, `rules list` |
| M3 | Control channel (loopback TCP + token, `control.json`), live traffic from sing-box's Clash API, animated Bubble Tea TUI (`vender tui`) |
| M4 | **Autostart** (systemd user unit / XDG autostart on Linux; Task Scheduler logon task / Run key on Windows), **Windows system proxy** (WinINET), **release packaging** and a per-user installer |
| **M5** | **Robustness**: critical "must survive a dead proxy" list, user `rules.yaml`, proxy-independent split DNS (FakeIP), `selected_server: auto` failover and node health in the TUI |
| **M6** | **Optional TUN mode** (transparent system-wide capture, `vender tun on\|off\|status`, TUN-aware autostart) |
| **M7** | **Subscription import and profiles**, plus Shadowsocks / VMess / Trojan outbounds (`vender import --url`, `vender profiles …`). Deferred to a later line: latency ranking, log rotation, signed builds |

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
autostart and the Windows registry work use only the standard library —
subscription fetching is `net/http` and adds no module at all.

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
./vender import 'ss://<base64(method:password)>@<host>:8388#My-SS-Node'
./vender import 'vmess://<base64 json>#My-VMess-Node'
./vender import 'trojan://<password>@<host>:443?sni=<sni>#My-Trojan-Node'

# ... or import a whole subscription into a profile (see "Subscriptions and profiles")
./vender import --url 'https://provider.example/sub?token=...' --profile provider

./vender servers list          # table of configured servers, * marks the active one
./vender servers list --links  # print the share link of every server (contains secrets)
./vender servers use My-Node   # make a server active (restarts a running daemon)
./vender servers use auto      # or fail over automatically (within the active profile)
./vender servers rm My-Node    # remove a server by name

./vender profiles list         # every profile, its node count and its subscription URL
./vender profiles use provider # make a profile active
./vender profiles update       # re-fetch the active profile's subscription
./vender profiles rm provider  # remove a profile and its servers

./vender rules list            # the merged routing policy and the embedded rule sets
./vender rules init            # write a commented per-user rules.yaml
./vender version               # build information

./vender daemon                # run the proxy (foreground; Ctrl-C to stop)
./vender status                # is it running, and is the port accepting? (exit code!)
./vender tui                   # attach the terminal UI to the running daemon
./vender proxy on              # also point the desktop's system proxy at vender
./vender proxy off             # restore the system proxy

./vender tun on                # optional: capture the whole host (needs a privilege)
./vender tun status            # setting, device, and whether it can actually start
./vender tun off               # back to the local inbound only

./vender autostart on          # run the daemon at login (systemd unit / Task Scheduler task)
./vender autostart status      # what is registered, and where (exit code!)
./vender autostart off         # remove the registration
```

Every command accepts `--config-dir <path>` (configuration) and `--run-dir
<path>` (runtime state) to point at alternative locations, which is also how
the test suite stays away from your real setup.

The first server imported also becomes the active one; removing the active
server clears the selection.

## Subscriptions and profiles

A commercial provider hands you one URL that returns many nodes. Import it whole
and file it under a **profile**:

```sh
./vender import --url 'https://provider.example/api/v1/client/subscribe?token=...' --profile provider
```

The subscription is fetched **as a v2ray client** (User-Agent `v2rayN/6.0`), so a
panel returns share links rather than an HTML or Clash page, and all three shapes
are auto-detected and accepted:

| Shape | What it looks like |
|---|---|
| base64 of newline-separated share links | the classic v2ray subscription |
| plain newline-separated share links | the same, uncompressed |
| Clash / Mihomo **YAML** | a `proxies:` list (`ss`, `vmess`, `trojan`, `vless`, `hysteria2`) |

Nodes vender cannot use are **skipped with a reason**, per node, and the rest are
imported: a `wireguard` node, an `ss` node that needs a SIP003 plugin, or a link
whose protocol-critical field is missing are all reported by name. The import
summary says exactly what happened (`added N, updated N, unchanged N, skipped N`).

Every server carries a **`profile`** (absent means the built-in `manual`
profile), and new nodes are tagged with the profile you chose — the default is
the subscription URL's host. A profile's metadata is kept in `profiles.yaml`
(the URL and the last fetch time); **membership** lives in `servers.yaml`, so
that file alone always tells you which nodes belong together.

```sh
./vender profiles list           # ACTIVE / PROFILE / NODES / UPDATED / SOURCE URL
./vender profiles use provider   # make it active
./vender profiles update         # re-fetch: add new, update changed, drop gone
./vender profiles rm provider    # remove the profile and its servers
```

**What "active" means.** It changes exactly one thing: with
`selected_server: auto`, the failover group is built over the **active profile's**
servers only, so `profiles use provider` + `servers use auto` means "fail over
between my provider's nodes". Naming a specific server still works and implies
nothing about the profile. `vender profiles use all` widens the group to every
server. A refresh never leaves the selection broken: if `config.yaml` selected a
node the subscription no longer publishes, the selection falls back to `auto`.

`vender profiles rm` refuses to remove the built-in `manual` profile, so there is
always at least one profile left. The TUI's Settings screen lists profiles and
switches them with `p`.

**Compatibility.** A `servers.yaml` written before this feature — no `profile`
key anywhere — loads unchanged as all-manual, and `auto` keeps failing over over
every server, so an existing setup behaves exactly as it did.

## System proxy

```sh
./vender proxy on     # point the desktop's proxy settings at 127.0.0.1:7897
./vender proxy off    # and restore them
```

This is a convenience for applications that honour the desktop proxy settings
but have no proxy option of their own. It only ever changes the *user's* proxy
configuration — never the routes, never the daemon's state — and vender still
recommends configuring the proxy per application (or exporting `http_proxy` /
`https_proxy` / `all_proxy`, which `proxy on` prints). For traffic that cannot
be configured per application, see [TUN mode](#tun-mode) below.

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

Neither platform needs administrator rights — **unless TUN mode is on**, see
[Autostart with TUN mode](#autostart-with-tun-mode).

**Start at boot, not just at login** (Linux): the user manager is normally torn
down when your last session ends. `autostart on` asks logind for lingering
(`loginctl enable-linger`); if polkit refuses the non-interactive call the unit
is still correct and `status` says so in its `detail` line — run
`loginctl enable-linger` yourself to finish the job.

**Windows caveat.** The task is registered hidden and at the least privilege,
but vender is a *console-subsystem* executable, so Task Scheduler does not
guarantee a fully invisible start — a console window can flash at logon. A
windowless start needs a GUI-subsystem build or a `conhost --headless` launcher
and is deferred to M7; the daemon itself does not need a console. The Windows
paths are compile-checked (`GOOS=windows go build ./...`) and their XML, argument
vectors and output parsing are unit-tested, but they cannot be *executed* on a
Linux development host.

The entry only ever runs `vender daemon`, which is headless: one daemon per run
directory, a fresh control token per start, and a clean exit on `SIGTERM`.

### Autostart with TUN mode

The two mechanisms above are **per-user**, and a per-user mechanism cannot be
given a kernel privilege: a systemd *user* manager is unprivileged, so it has
nothing to hand out, and a per-user Run key cannot run a process elevated. With
`engine.tun.enabled: true`, `vender autostart on` therefore switches to the
mechanism that *can*:

| Platform | Mechanism with TUN on | What it costs |
|---|---|---|
| Linux | a **system** unit `/etc/systemd/system/vender.service` with `User=<you>` and `AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW` | one `sudo vender autostart on`. The daemon still runs as you, not as root; `CapabilityBoundingSet` keeps every other capability out of reach |
| Windows | the same logon task, registered as `HighestAvailable` instead of `LeastPrivilege` | one `vender autostart on` from an **elevated** prompt |

If that privileged registration cannot be made — no root, no elevation — `on`
still installs the per-user entry (so the daemon does start at login) and then
**returns an error** naming what TUN mode is still missing. It never reports a
setup that cannot work. `status` tells the same truth at any time: it says which
mechanism is registered and whether that mechanism can create a TUN, and when it
cannot it prints the fix.

The alternative to the system unit is the `setcap` route: keep the per-user
unit and grant the capability to the engine binary itself —

```sh
sudo setcap cap_net_admin,cap_net_raw+ep "$(command -v sing-box)"
```

— which needs no file in `/etc` at all, but is **lost whenever that binary is
replaced**: a sing-box upgrade, a reinstall, or unpacking a fresh vender release
archive over it. `vender tun status` and `vender autostart status` check the
binary's file capabilities too, so they report this arrangement correctly
instead of calling it broken.

`vender autostart off` removes both the user and the system artifacts; the
system one needs root, and if it cannot be removed `off` says so rather than
leaving a root-owned unit that would start the daemon again at the next boot.

## The terminal UI

```
./vender tui
```

An animated Bubble Tea interface with four screens:

| Key | Screen | What it shows |
|---|---|---|
| `1` | **Dashboard** | daemon and engine state, the selected server, the active profile, the mode, uptime, and a **live up/down traffic graph** with human-readable rates and session totals |
| `2` | **Nodes** | every configured server with its profile, type, protocol, endpoint and latency. `enter` makes one active (the daemon restarts the engine in place), `A` switches to auto failover, `t` measures latency |
| `3` | **Logs** | the engine's own output, streamed live, with follow mode (`f`) and a filter (`/`) |
| `4` | **Settings** | the routing mode, the **profiles** (node counts and a `p` key to switch the active one), the listener, the **TUN inbound** (state, device, and whether this machine can actually create it — `u` toggles it), a read-only view of the **active built-in policy**, and the **start-at-login registration** (`a` toggles it) |

Other keys: `tab` / `shift+tab` between screens, `?` for help, `t` to test
latency, `s` / `x` / `R` to start, stop and restart the engine, `m` to cycle
the mode, `p` on Settings to switch the active profile, `u` on Settings to toggle
TUN mode, `a` on Settings to toggle start-at-login, `q` or Ctrl-C to quit.

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
connection: `status`, `servers`, `select`, `mode`, `tun`, `profile`, `latency`,
`start`, `stop`, `restart` and `rules` are request/response, while `logs` and
`traffic` are subscriptions that take their connection over. A request without
the token is rejected. See `docs/ARCHITECTURE.md` §8 for the protocol and for
why it is loopback TCP rather than a unix socket.

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

## TUN mode

```sh
./vender tun on        # capture the whole host through the tunnel
./vender tun status    # is it on, what device, and can this machine actually create one?
./vender tun off       # back to the local inbound only
```

By default vender only proxies applications that you point at
`127.0.0.1:7897`. TUN mode is the opt-in alternative: the engine creates a
virtual network device, installs routes that pull every destination into it, and
carries each connection through the selected server according to the same
routing policy (rule / global / direct). The mixed inbound **stays up**, so both
ways in work at the same time.

```sh
sudo vender autostart on     # the privileged registration TUN mode needs
#   or, keeping the per-user service:
sudo setcap cap_net_admin,cap_net_raw+ep "$(command -v sing-box)"
```

### It needs a privilege, and vender says so before trying

Creating the TUN device is the one thing vender cannot do as an ordinary user:

| Platform | What is needed | How vender reports it |
|---|---|---|
| Linux | `CAP_NET_ADMIN` (and `CAP_NET_RAW`) in the process, or on the `sing-box` binary via `setcap ...+ep` | `vender tun status` prints the verdict and the exact fixes; the daemon refuses to enable TUN *before* stopping a working engine |
| Windows | an elevated (Administrator) process — a WinTun adapter is a driver-level device | the same, with the "register autostart from an elevated prompt" route |

`vender tun status` exits non-zero when TUN is off (so it drops into a script),
and prints, in order: the setting, the device and stack, the resolved engine,
**the privilege verdict**, and what a running daemon thinks. Without the
privilege the engine would otherwise fail with sing-box's own
`configure tun interface: open tun: TUNSETIFF: operation not permitted`, which
says nothing about how to fix it.

### What is rendered, and what to think about

| Setting | Default | Notes |
|---|---|---|
| `stack` | `mixed` | `system` — the kernel stack, reached through the device; `gvisor` — a user-space stack; `mixed` — TCP on gvisor, UDP on the kernel. Always written explicitly, so a change of engine default cannot silently change vender's behaviour |
| `interface_name` | `vender0` | named explicitly so `ip link`, `ip route` and any cleanup have one predictable device to look for. Max 15 characters on Linux |
| `auto_route` | `true` | installs the routes for `0.0.0.0/1` and `128.0.0.0/1` and adds `ip rule`s, so the engine's own marked connections bypass its own tunnel instead of looping. `auto_route: false` captures nothing until you add a route yourself — the safe way to try a TUN on a machine whose connectivity matters |
| `strict_route` | `false` | a kill switch: refuses traffic that would leave outside the tunnel. Useful, and it means a broken engine means *no* connectivity |
| `mtu` | `0` (`auto` → 9000) | lower it if a path MTU problem shows up |
| `route_exclude_address` | empty | CIDRs kept out of the tunnel. The usual entries spare your LAN, a corporate VPN, or mihomo/clash's own `198.18.0.0/15` |
| `route_exclude_address_set` | empty | the same, expressed as embedded rule-set tags (`geoip-cn`, `geosite-cn`, …). vender checks the tag exists and declares it in the engine config. sing-box 1.14 has no `route_exclude_rule` field; this is that knob |
| `address` | empty → engine default | the device's own address. Left alone, sing-box picks a private `/30` that does not collide with the `198.18.0.0/15` a mihomo/clash TUN uses |

Two things that follow from turning it on, stated plainly:

- **DNS is now the engine's job.** In `rule` mode — and, under a TUN, in
  `global` mode too — the generated config starts with `{"action": "sniff"}`
  and `{"protocol": ["dns"], "action": "hijack-dns"}`, so DNS queries stop being
  plain packets to port 53 and are answered by the split-DNS policy instead
  (domestic names via `223.5.5.5`, everything else over the proxied resolver).
  In `direct` mode there are no such actions: direct mode must not intercept
  anything.
- **`global` is still `global`.** Under a TUN, global mode gains those two
  *actions* — without them "everything through the proxy" would be false for the
  one protocol every connection starts with — and still emits **no destination
  rules**.
- **The whole host depends on the engine.** If it cannot reach the server, the
  host loses connectivity, not just one application. That is what `tun off`
  (and, in the other direction, `strict_route`) is for.

### Coexisting with another TUN or VPN

The host's default route is no longer exclusively yours once a TUN is up. Three
practical points:

- **Another TUN already installed** (mihomo, a corporate VPN): do not run two
  `auto_route` TUNs that both claim the default route — they fight, and the
  result is usually "no connectivity at all". Stop the other one first, or keep
  `auto_route: false` and route only what you want.
- **The engine's own connection to the server** must not be routed back into
  its own tunnel. sing-box handles that with fwmarks and `ip rule`s; vender also
  emits `route.auto_detect_interface: true` when a TUN is on and
  `engine.bind_interface` is empty, which binds the outbound to the interface
  holding the default route. Set `bind_interface` instead when the host is
  multi-homed (see [Behind an existing VPN or TUN](#behind-an-existing-vpn-or-tun)).
- **A stale device** from a killed process: `ip link del vender0` removes it, and
  the routes and rules go with it. `vender tun off` + a restart is the supported
  way.

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

Two files live in the config directory, plus two optional ones:

- `config.yaml` — global settings (world-readable, no secrets):

  ```yaml
  log_level: info
  proxy:
      listen: 127.0.0.1:7897
      mode: rule          # rule | global | direct
  engine:
      binary: ""          # empty => next to vender, else PATH
      bind_interface: ""  # e.g. eth0 on a multi-homed host
      failover_interval: 30s       # URLTest probe cadence (auto failover); 5s-10m
      failover_tolerance_ms: 100   # URLTest switch threshold; 0 = engine default
      tun:
          enabled: false        # opt-in; see "TUN mode" below
          stack: mixed          # system | gvisor | mixed
          mtu: 0                # 0 = the engine's own default (9000)
          auto_route: true      # install the routes that pull traffic in
          strict_route: false   # true = refuse traffic that would bypass the tunnel
          interface_name: vender0
          # address: []                       # the device's own address
          # route_exclude_address: []         # CIDRs kept out of the tunnel
          # route_exclude_address_set: []     # rule-set tags kept out (e.g. geoip-cn)
  selected_server: ""     # a name in servers.yaml, or "auto" for failover
  ```

  `engine.failover_interval` and `engine.failover_tolerance_ms` are only
  consulted with `selected_server: auto` (see "Automatic failover" below).
  Shorter interval = faster failover but more probe traffic, since every probe
  is sent *through* its member:

  ```yaml
  engine:
    failover_interval: 30s
    failover_tolerance_ms: 100
  ```

  A disabled `engine.tun` block is inert: it is not validated and nothing is
  rendered from it, so a typo in a feature you are not using cannot stop vender
  from starting.

- `servers.yaml` — the server list, **written with mode `0600`** because it
  holds credentials (UUIDs, passwords, REALITY public keys). Managed by
  `vender import` and `vender servers rm`; hand-editing is supported. The
  `profile` key is optional and absent means the built-in `manual` profile:

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
      - name: Provider-SS
        type: shadowsocks
        server: 203.0.113.20
        port: 8388
        method: aes-256-gcm
        password: ...
        profile: provider          # absent => the built-in "manual" profile
  ```

  Per-type field notes:

  | Type | Fields |
  |---|---|
  | `vless` | `uuid`, `flow`, `security` (`none`/`tls`/`reality`), `sni`, `fingerprint`, `public_key`, `short_id`, `spider_x`, `network` (TCP only), `header_type` |
  | `hysteria2` | `password`, `obfs`, `obfs_password`, `sni`, `insecure`, `pin_sha256`, `alpn` |
  | `shadowsocks` | `method` (the cipher, e.g. `aes-256-gcm` or `2022-blake3-aes-256-gcm`), `password` |
  | `vmess` | `uuid`, `cipher` (the `scy`, `auto` by default), `alter_id`, `security` (`none`/`tls`), `sni`, `fingerprint`, `insecure`, `network` (`tcp`/`ws`/`grpc`), `header_type` (`none`/`http`), `path`, `host`, `alpn` |
  | `trojan` | `password`, `sni`, `security` (always `tls`), `fingerprint`, `insecure`, `network` (`tcp`/`ws`/`grpc`), `path`, `host`, `alpn` |

  Hand-edited files are validated on load, and problems are reported all at once
  with the field that is wrong (for example `ss: method is required for
  shadowsocks`, or `vmess: network \"kcp\" is not supported for vmess`).

- `profiles.yaml` — the **profile registry**: which profiles exist, the
  subscription URL each came from, when it was last fetched, and which one is
  active. It is written `0600` (a subscription URL can carry a token) and it is
  safe to delete: membership is derived from `servers.yaml`, so the worst case is
  losing the URL and going back to the built-in `manual` profile.

  ```yaml
  active: provider
  profiles:
      - name: provider
        url: https://provider.example/api/v1/client/subscribe?token=...
        fetched_at: 2026-10-17T09:30:00+08:00
  ```

- `rules.yaml` — **optional** user routing rules. It is merged *before* the
  built-in policy, so an entry here always wins; only the built-in critical list
  (below) is evaluated before it. vender never creates it on its own:

  ```yaml
  # one entry per line; a bare name means "this domain and all subdomains"
  direct:
      - internal.corp
      - =exact.example      # this exact name only (no subdomains)
      - 10.0.0.0/8          # an IP range
  proxy:
      - blocked.example
  block:
      - ads.example
  ```

  ```sh
  vender rules init      # write a commented rules.yaml (refuses to overwrite)
  vender rules path      # where it lives
  vender rules list      # the merged policy, in evaluation order
  ```

## Routing rules

`proxy.mode: rule` (the default) applies a small, built-in policy. It is derived
from a hand-written configuration that carried ~9500 individual rules — 9018 of
them `DOMAIN-SUFFIX` — and deliberately **aggregates the same intent into
geosite/geoip categories** instead of embedding thousands of domains that would
rot the moment nobody looks at them.

The policy, in evaluation order (first match wins):

| # | Rule | Matches | Goes to |
|---|---|---|---|
| 1 | `critical-direct` | `opencode.ai` | `direct` |
| 2 | `user-direct` | `rules.yaml` `direct:` (if any) | `direct` |
| 3 | `user-proxy` | `rules.yaml` `proxy:` | `proxy` |
| 4 | `user-block` | `rules.yaml` `block:` | `block` |
| 5 | `private-dest-direct` | `ip_is_private` (RFC1918, loopback, link-local) | `direct` |
| 6 | `ads-block` | `geosite-category-ads-all` | `block` |
| 7 | `service-overrides-proxy` | Steam, OneDrive, Microsoft, Google, YouTube, Telegram, Netflix, OpenAI, Bahamut | `proxy` |
| 8 | `cn-direct` | `geosite-cn`, `geoip-cn` | `direct` |
| — | `route.final` | everything else | `proxy` |

Two things about the order are load-bearing:

- **The critical list is first, and tiny.** It holds the domains whose loss would
  make the machine unmanageable rather than merely inconvenient, and that are
  also reliably reachable **directly** on this network. Today that is only the
  coding agent's own model/API plane (`opencode.ai`): it is resolved and dialled
  directly, so a dead proxy can never cut it off, and nothing — not even a
  `rules.yaml` entry — can route it through the proxy. A domain that is not
  reachable directly does not belong here: `api.tavily.com` used to be listed
  and was removed, because forcing it direct timed out on this network while the
  proxy reached it fine. It now follows the normal policy (and is still
  overridable from `rules.yaml`).
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
IPs. DNS is split so that **name resolution never depends on the proxy**:

```
critical list         -> dns-cn      223.5.5.5 over UDP, direct   -> real address
rules.yaml direct:    -> dns-cn      direct                        -> real address
geosite-cn            -> dns-cn      direct                        -> real address
everything else, A/AAAA -> dns-fakeip  local, no upstream          -> fake address
everything else, other  -> dns-cn      direct                        -> real address
```

The proxied side is answered by a local **FakeIP** resolver: it hands out a
synthetic `198.18.0.0/15` address in microseconds, with no query to anyone, and
the engine maps it back to the real name before the proxy dials it. That is the
whole fix for "a slow proxy stalls DNS for everything": there is no longer a
resolver reached *through* the proxy. The one trade-off, stated plainly: a
non-address query (HTTPS/SVCB, TXT) for a foreign name is answered by the
domestic resolver, so the *name* is visible to it. Address queries — the ones
connections actually use — never leave the machine for a proxied domain. See
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) §7.3 for the full reasoning.

The split is emitted as sing-box 1.14 *actions* (`{"action":"sniff"}`,
`{"action":"hijack-dns"}`) plus a `dns` block; the legacy inbound `sniff` field
the pinned engine removed is never written.

`vender rules list` prints exactly this merged policy, the DNS split and the rule
sets below.

### Automatic failover (`selected_server: auto`)

Point `selected_server` at `auto` and vender renders the **active profile's**
servers as members of a sing-box `urltest` group, instead of pinning one. The
engine then re-tests each node against a "generate 204" URL and carries traffic
over the fastest reachable one, so a degraded node is bypassed without anyone
touching anything:

```sh
vender servers use auto      # turn failover on (a running daemon restarts)
vender profiles use provider # scope it to one profile (see "Subscriptions and profiles")
vender profiles use all      # ... or to every server, whatever its profile
vender servers use My-Node   # pin a single server again
```

The group reuses the `proxy` tag, so the routing policy, `route.final` and the
global-mode DNS resolver are all unchanged. In the **Nodes** screen the header
shows which member is carrying traffic and the latency column is filled from the
engine's own URL-test results; `A` switches to auto. Single-server setups are
completely unaffected — the generated config is byte-identical to before, and an
existing installation (every server in the built-in `manual` profile) keeps
failing over over every server.

Two `engine` knobs tune the probing:

```yaml
engine:
  failover_interval: 30s        # default; 5s-10m inclusive
  failover_tolerance_ms: 100    # default; 0-5000, 0 = engine default
```

`failover_interval` is how often each member is probed. Every probe is a real
HTTP request sent **through** that member, so the interval trades failover speed
against probe traffic: shorter reacts sooner to a node going bad but keeps every
member busier (and can load a fragile link). The default is `30s`. The allowed
range is `5s`–`10m`; anything else is rejected at start-up. `failover_tolerance_ms`
is the latency gap that makes the group switch members; the default (100 ms) sits
above sing-box's own 50 ms so two nodes of similar latency do not flap.

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

- **No per-rule outbound choice.** The proxy is either the selected server or,
  in auto mode, one URLTest group over the active profile's servers; there is no
  "Chinese traffic through node A, the rest through node B".
- **No SIP003 plugins for Shadowsocks.** A link that needs one (obfs, v2ray-plugin)
  is rejected with a message naming the plugin rather than connecting without the
  obfuscation the server expects. Ask your provider for a plain node, or use a
  VMess/Trojan node with a WS transport instead.
- **No latency ranking or log rotation yet**, and Windows builds are not signed:
  those remain for a later line.
- **No `mode: global` rules.** `global` means everything through the proxy, so
  it emits no routing rules at all — only a proxied DNS resolver. `direct` emits
  no proxy outbound and no DNS block.

## What the daemon does

`vender daemon` is the single source of truth. For one run it:

1. reads `config.yaml` + `servers.yaml` and validates the selected server;
2. takes an advisory lock on `<run dir>/vender.lock` — a second daemon on the
   same run dir fails immediately, and the lock is released by the OS even if
   the daemon is killed, so it can never go stale;
3. in rule mode extracts the embedded rule sets into `<run dir>/rules/` (before
   the config that references them);
4. renders `singbox.json` **atomically** (`0600`, since it holds credentials)
   with one `mixed` inbound (plus a `tun` inbound when TUN mode is on), the
   selected outbound, a `direct` outbound, a `block` outbound, the built-in
   rules, their actions, the split DNS policy and the experimental Clash API
   (loopback, with a generated secret) the TUI reads live traffic from;
5. checks, when TUN mode is on, that this process can create a TUN device, and
   fails with the exact fix instead of sing-box's bare `operation not permitted`;
6. starts `sing-box run -c <run dir>/singbox.json` in its own process group and
   returns only once the inbound port actually accepts TCP connections;
7. publishes its control endpoint as `<run dir>/control.json` (`0600`) and
   serves it, so `vender tui` can attach;
8. streams sing-box's output to its own standard output, prefixed `sing-box:`,
   and to every attached client;
9. on `SIGINT`/`SIGTERM`, stops sing-box gracefully (`SIGTERM`, then `SIGKILL`
   after a grace period), removes the pid file and the control endpoint, and
   releases the lock.

While it runs, a client can also switch the active server, change the routing
mode, turn TUN mode on and off, and start/stop/restart the engine in place — the daemon re-renders the
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
