# vender — Architecture

This document describes how `vender` is put together, what each milestone adds,
and the interfaces that later milestones compile against today.

## 1. Goals and non-goals

**Goals**

- A single small binary that gives a Linux / Windows 11 user a working local
  proxy with minimal ceremony.
- Two well-chosen outbound protocols: VLESS + Vision + REALITY (TCP) and
  Hysteria2 (UDP).
- A local **mixed SOCKS5 + HTTP** inbound so applications opt in per app.
- Configuration that is plain YAML, human-readable, and safe on disk.

**Non-goals**

- No TUN / system-wide transparent proxying.
- No subscription marketplace, no telemetry, no accounts.
- No attempt to reimplement the proxy protocols; sing-box does that.

## 2. Process model

```
        +----------------------------+
        |        vender daemon       |   authoritative, headless
        |  (M1)                      |
        |  - loads config.yaml       |
        |  - loads servers.yaml      |
        |  - renders singbox.json    |
        |  - supervises sing-box     |
        |  - run dir: lock + pid     |<----------+
        +-------------+--------------+           |
                      | manages                   | attaches
                      v                           |
        +----------------------------+   +--------+---------+
        |        sing-box child      |   |   vender tui     |
        |  mixed inbound 127.0.0.1:  |   |     (M3)         |
        |  7897 -> selected outbound |   |  Bubble Tea UI   |
        +----------------------------+   +------------------+
                      ^                           ^
                      | local apps                 | CLI commands
        +----------------------------+   +--------+---------+
        |  browser / curl / ...      |   |  vender CLI      |
        +----------------------------+   | import, servers, |
                                         | status, proxy    |
                                         +------------------+
```

The **daemon is the single source of truth**. It owns the sing-box process, the
rendered engine configuration, and the runtime state. The TUI and the
command-line control commands (`vender status`, `vender proxy on|off`) are
clients: they never launch sing-box themselves, they ask the daemon.

This split is what makes the TUI optional and restartable. It is also why M0
shipped the CLI with a clean separation between *configuration* (files on disk)
and *control* (a running daemon), and why M1 kept the daemon a plain foreground
process: the control surface a client needs is the run directory — the lock
says whether a daemon is up, and the generated config plus the listen address
say what it is doing. M3 adds a real control socket for the operations that
cannot be expressed as files (switching server, changing mode, tailing logs),
at which point the clients stop reading files directly.

Two directories, deliberately:

| Directory | Contents | Lifetime |
|---|---|---|
| `os.UserConfigDir()/vender` | `config.yaml`, `servers.yaml` | the user's; editable, backable-up, never overwritten by vender except through `import`/`servers`/`select` |
| `os.UserCacheDir()/vender` | `singbox.json`, `rules/*.srs`, `vender.lock`, `vender.pid` | the daemon's; disposable, regenerated on every start |

`--config-dir` and `--run-dir` override both, which is what keeps the test
suite from touching a real installation.

## 3. Packages

```
cmd/vender/            CLI (cobra): version, import, servers, rules, daemon, status, proxy
internal/config/       config.yaml + servers.yaml + share-link parsing  [M0]
internal/core/         engine contract + sing-box rendering + supervision [M1]
internal/rules/        built-in routing policy + embedded rule sets       [M2]
internal/tui/          Bubble Tea TUI (daemon client)                    [M3]
internal/autostart/    start-at-login registration                       [M4]
docs/                  this document
scripts/               build + fetch helpers (fetch-rules.sh, fetch-sing-box.sh)
```

Dependency direction is strictly downward:

```
cmd/vender     ->  internal/core
               ->  internal/tui (M3)
               ->  internal/config
               ->  internal/rules
internal/core  ->  internal/config
               ->  internal/rules
internal/rules ->  (pure data + embedded assets; imports nothing of vender's)
internal/tui   ->  internal/config (+ a daemon control client, M3)
internal/autostart -> (standalone)
```

No package imports `cmd/`. `internal/config` has no dependency on OS-specific
APIs: it defers the config directory to `os.UserConfigDir()`, which resolves to
`~/.config/vender` on Linux and `%AppData%\vender` on Windows. `internal/core`
owns the two OS-specific seams it needs (the lock and the child process group)
behind build tags, so the CLI stays platform-free above it.

## 4. Configuration layer (`internal/config`)

### 4.1 Files

| File | Mode | Contents |
|---|---|---|
| `config.yaml` | `0644` | global settings; contains no secrets |
| `servers.yaml` | `0600` | the server list; contains credentials |

The config directory itself is created `0700`. Writes are atomic: content is
written to a temp file in the same directory, chmodded, `fsync`ed, then
`rename`d over the target, so a crash or a concurrent reader never sees a
half-written file. `servers.yaml` also has its mode forced on every save, which
repairs a file whose permissions drifted.

A missing file is not an error — a fresh install loads built-in defaults.

### 4.2 `config.yaml`

```yaml
log_level: info                 # debug | info | warn | error
proxy:
    listen: 127.0.0.1:7897      # host:port of the mixed inbound
    mode: rule                  # rule | global | direct
engine:
    binary: ""                  # empty => bundled next to vender, else PATH
    bind_interface: ""          # pin the proxy outbound to an interface
selected_server: ""             # name of the active entry in servers.yaml
```

`Config.Normalize` fills zero values with defaults and canonicalises casing, so
a partial file behaves like a documented one. `Config.Validate` rejects an
unknown log level, an unknown mode, a malformed or wildcard listen address
(a wildcard listener would expose the proxy, so it is treated as a mistake
rather than a convenience), and an implausible `engine.bind_interface`.

The default listen address is **7897, not 7890**: 7890 is the conventional port
of a system-wide mihomo/clash instance, so defaulting to it would make vender
fail to bind on exactly the hosts that already run a proxy.

### 4.3 `servers.yaml`

```yaml
servers:
    - name: My-Node
      type: vless               # vless | hysteria2
      server: 203.0.113.10      # host or IP
      port: 443
      # --- vless ---
      uuid: ...
      flow: xtls-rprx-vision
      security: reality         # none | tls | reality
      sni: www.example.com
      fingerprint: chrome
      public_key: ...           # REALITY public key (pbk)
      short_id: ...             # REALITY short id (sid)
      spider_x: ...             # REALITY spiderX (spx)
      network: tcp
      header_type: none
      # --- hysteria2 ---
      password: ...
      obfs: salamander
      obfs_password: ...
      insecure: true
      pin_sha256: ...
      alpn: [h3]
```

Validation is per type. A `vless` entry requires a well-formed UUID, and with
`security: reality` it additionally requires `public_key` and `sni`; with
`security: tls` it requires `sni`. A `hysteria2` entry requires `password`, and
`obfs_password` must be present if and only if `obfs` is set. Only `network: tcp`
is accepted today; a `ws`/`grpc` link fails loudly instead of silently
misbehaving. `Server.Normalize` applies safe defaults (`network: tcp`,
`fingerprint: chrome`, `header_type: none`) but deliberately does **not** invent
a `flow`: a REALITY link that omits `flow` means the server expects a plain
stream, and defaulting it would break that server.

`Server.String` is redaction-safe — it prints only type, name and endpoint — so
it can be embedded in errors and logs. The only code that emits credentials is
`Server.ShareLink()` (used by `servers list --links`).

### 4.4 Share-link parsing (`internal/config/sharelink.go`)

`ParseShareLink` accepts VLESS and Hysteria2 links and yields a normalized
`Server`; `Server.ShareLink()` renders the canonical inverse. Both directions
are covered by tests, including round-trips.

**VLESS**

```
vless://<uuid>@<host>:<port>?encryption=none&flow=...&security=reality&sni=...
        &fp=chrome&pbk=...&sid=...&spx=/&type=tcp&headerType=none#Name
```

Accepted parameter aliases (parsing is deliberately liberal so links from any
panel load; unknown parameters are ignored):

| Field | Aliases |
|---|---|
| SNI | `sni`, `serverName`, `servername`, `peer` |
| Fingerprint | `fp`, `fingerprint` |
| Public key | `pbk`, `publicKey`, `public-key` |
| Short id | `sid`, `shortId`, `short-id` |
| SpiderX | `spx`, `spiderX`, `spider-x` |
| Network | `type`, `network` |
| Header type | `headerType`, `header-type` |
| Encryption | `encryption` (must be `none`) |

Query keys are matched case-insensitively. `encryption` other than `none` is
rejected rather than ignored, because it changes what the server expects.
REALITY fields are cleared when `security` is not `reality`, so a stray `pbk`
on a plain-TLS link does not linger in `servers.yaml`.

**Hysteria2**

```
hysteria2://<password>@<host>:<port>/?sni=...&obfs=salamander
        &obfs-password=...&insecure=1#Name
hy2://<password>@<host>:<port>/?...#Name        # alias scheme
```

The password comes from the URL userinfo (`user:pass` is rejoined to support
the userpass auth mode); `password`/`auth` query parameters are accepted as a
fallback for panels that place it there. `insecure` accepts `1/0`, `true/false`,
`yes/no`, `on/off`; `alpn` accepts a comma-separated list or repeated
parameters.

**Names.** The server name is the URL fragment (percent-decoded by
`url.Parse`). With no fragment the name falls back to `host:port`, bracketed for
IPv6.

**IPv6.** A link such as `vless://<uuid>@[2001:db8::1]:443?...` stores the bare
literal in `server` and re-brackets on output via `net.JoinHostPort`, so
`Endpoint()` and `ShareLink()` are always correctly bracketed.

`ParseShareLink` reports typed errors: `ErrInvalidLink` for a recognized-but-
broken link, `ErrUnsupportedScheme` for e.g. `ss://`. Messages name the exact
component that failed (missing uuid, missing port, out-of-range port, bad
boolean, unsupported encryption).

## 5. CLI (`cmd/vender`)

Built with [cobra]. Each subcommand is constructed by a method on a shared
`app` struct, which resolves the config `Store`. The global `--config-dir` flag
points the whole CLI at an alternative directory, which is how the CLI is tested
without touching the user's real configuration.

| Command | State |
|---|---|
| `vender version` | implemented (M0) |
| `vender import <share-link>` | implemented (M0) (`--name`, `--force`) |
| `vender servers list` | implemented (M0) (`--links`) |
| `vender servers rm <name>` | implemented (M0) |
| `vender rules list` | implemented (M2) — the ordered policy, DNS split and embedded sets |
| `vender daemon` | implemented (M1) — foreground, one per run dir |
| `vender status` | implemented (M1) — exit code means "usable" |
| `vender proxy on\|off` | implemented (M1) Linux/GNOME; Windows is M4 |
| `vender tui` | stub — M3 |
| `vender autostart on\|off\|status` | stub — M4 |

A stub is a real, documented command that exits with
`<name>: not implemented in this milestone (Mx)`. This keeps the CLI surface and
exit codes stable across milestones, so scripts and the TUI can be written
against the final command set before the implementations land.

## 6. Engine (`internal/core`) — implemented in M1

The engine is the only seam between vender and sing-box. M0 shipped the
interface and the resolved runtime input; M1 implements both.

```go
// RuntimeConfig is the fully resolved input for one engine run.
type RuntimeConfig struct {
    Config     config.Config  // global settings (listen, mode, log level)
    Server     *config.Server // selected outbound; may be nil in mode direct
    BinaryPath string         // resolved sing-box executable
    RunDir     string         // runtime dir: singbox.json, lock file, logs
}

// Engine is the single seam between vender and its proxy engine.
type Engine interface {
    WriteConfig(rc RuntimeConfig) error // render <RunDir>/singbox.json atomically
    Start() error                       // launch; return once the listener is usable
    Stop() error                        // graceful stop, escalate to kill
    Logs() <-chan string                // stream engine output lines
    Healthy() bool                      // running AND inbound accepts connections
}
```

M1 adds exactly one method on the concrete type, `Pid() int`, for the daemon's
own reporting. The interface itself is unchanged, so M3's TUI compiles against
it as written in M0.

Files:

| File | Role |
|---|---|
| `engine.go` | the frozen contract, `RuntimeConfig.Validate`, `New` |
| `render.go` | `RuntimeConfig` → sing-box JSON (pure, golden-tested) |
| `singbox.go` | `Engine` over a supervised child process |
| `binary.go` | engine lookup: config → bundled → PATH |
| `paths.go` | run dir, generated-config path, lock path, pid file |
| `lock.go` + `lock_unix.go` / `lock_windows.go` | one daemon per run dir |
| `process_unix.go` / `process_windows.go` | process group and signal/terminate |

### 6.1 Engine lookup (`binary.go`)

`ResolveBinary` searches, in order: `engine.binary` from config.yaml; a
`sing-box` / `sing-box.exe` next to the `vender` executable; `sing-box` on
`PATH`. The winner is returned absolute, and the failure names all three
locations along with the `scripts/fetch-sing-box.sh` hint. The pure core,
`resolveBinary(configured, exeDir, pathEnv)`, takes every ambient input as a
parameter, so the resolution order is unit-tested without a sing-box on the
machine.

### 6.2 Rendering (`render.go`)

`BuildConfig` is pure and deterministic, which is what makes the golden tests
possible. It emits up to five top-level sections — `log`, `dns`, `inbounds`,
`outbounds`, `route` — and nothing else:

- one `mixed` inbound tagged `mixed-in`, with `listen` / `listen_port` split
  from `proxy.listen`;
- the **proxy** outbound (`vless` with `flow` + `tls.utls` + `tls.reality`, or
  `hysteria2` with `password` + `obfs` + `tls`), always tagged `proxy`;
- a `direct` outbound and a `block` outbound, always present so that rules
  written against `rules.OutDirect` / `rules.OutBlock` always have a target;
- `route.final` = `proxy` for `rule`/`global` and `direct` for `direct`;
- in **rule** mode, `route.rule_set` (one `local`/`binary` entry per embedded
  set, with an **absolute** path inside the run dir), then `route.rules`: the
  actions (`sniff`, `hijack-dns`) first, followed by the destination rules from
  `internal/rules.BuiltinRules()`, plus `route.default_domain_resolver` and the
  `dns` section from `internal/rules.BuiltinDNS()`;
- in **global** mode, a `dns` section with a single proxied resolver and **no**
  routing rules — `global` must stay global, so emitting the CN-direct rules
  there would silently turn it into `rule`;
- in **direct** mode, neither: there is no proxy to resolve through, so
  sing-box's own default (the system resolver) is right;
- `engine.bind_interface`, when set, is passed through to the outbound, which
  pins the connection to the proxy server to a specific interface on a
  multi-homed (or TUN-hijacked) host.

Three decisions are worth recording because the pinned engine forced them:

- **No legacy `sniff`.** sing-box 1.13 *removed* the legacy inbound options, so
  emitting `"sniff": true` makes the pinned version refuse the config outright.
  Inbound sniffing is the route action `{"action": "sniff"}`, and M2 renders it
  (followed by `{"protocol": ["dns"], "action": "hijack-dns"}`) as the first two
  route entries.
- **No detour on the domestic resolver.** sing-box 1.14 refuses a DNS server
  whose `detour` is the empty `direct` outbound vender emits ("detour to an
  empty direct outbound makes no sense"). The domestic resolver therefore has no
  `detour` at all — absence already means direct — while the foreign resolver
  names the `proxy` outbound explicitly. This was found by running the config,
  not by reading the schema.
- **Direct mode emits no proxy outbound at all.** A `mode: direct` run does not
  reference the selected server, so an unusable server entry cannot break the
  one mode whose whole point is to work without a proxy.

### 6.3 Supervision (`singbox.go`)

`Start` spawns `sing-box run -c <RunDir>/singbox.json` with the run dir as its
working directory and its own process group, captures stdout and stderr through
pipes, and returns only when the mixed inbound accepts a TCP connection. Logs
are delivered to a channel that is closed after the process has exited *and*
both pipe readers have drained, so a consumer that ranges over `Logs()`
terminates instead of leaking a goroutine — and so `Stop` can wait on a single
channel instead of polling. Lines are dropped rather than blocking when nobody
is reading, so a slow consumer can never wedge the child.

Failure modes are made explicit rather than left to time out:

- an occupied listen port is detected *before* spawning — otherwise the
  readiness dial would be answered by the other listener and a failed start
  would look successful;
- a child that dies during startup yields the child's own last output, not a
  bare timeout, because the supervisor keeps a small ring buffer of recent
  lines;
- `Stop` is idempotent, signals the process group with `SIGTERM` and escalates
  to `SIGKILL` after a grace period. On Windows there is no in-band equivalent,
  so it terminates the process (documented in `process_windows.go`).

### 6.4 One daemon per run dir (`lock.go`)

The daemon holds an exclusive advisory lock on `<run dir>/vender.lock` for its
whole lifetime (`flock` on unix, `LockFileEx` on Windows). Because both are
released by the OS when the process dies, the lock can never go stale, which is
why `vender status` uses the lock — not the pid file — to answer "is a daemon
running?". The pid file exists only so status can print a pid.

### 6.5 System proxy (`cmd/vender/proxy*.go`)

`proxy on` writes the GNOME proxy schema (`org.gnome.system.proxy.*`) through
`gsettings`, setting the endpoints before flipping the mode so a failure part
way through leaves the proxy disabled rather than half-configured; `proxy off`
restores `mode: none`. It also prints the `http_proxy` / `https_proxy` /
`all_proxy` exports for shells that ignore the desktop settings. Nothing else is
touched: no TUN, no daemon state. On Windows the Registry `Internet Settings`
path lands with M4, and the command says so instead of pretending.

## 7. Routing rules (`internal/rules`) — implemented in M2

`internal/rules` owns the default policy, the DNS policy and the compiled rule
sets those policies match against. Nothing about sing-box leaks upward: the
package speaks in vender's own terms and `core/render.go` is the only place that
knows the JSON schema.

```go
func BuiltinRules() []Rule       // destination rules, in evaluation order
func BuiltinActions() []Action   // sniff, hijack-dns
func BuiltinDNS() DNS            // split DNS for rule mode
func GlobalDNS() DNS             // proxied-only DNS for global mode

func RuleSets() []RuleSet                      // the embedded sets, pinned table
func ReferencedRuleSets() []string             // the tags the policy needs
func WriteRuleSets(dir string) (int, error)    // extract embedded .srs to disk
func RuleSetPath(runDir, tag string) (string, error)
```

`Rule` expresses routing in vender's own terms (`RuleSets`, `Domains`,
`DomainSuffixes`, `DomainKeywords`, `IPCIDRs`, `IPIsPrivate`, `Ports`,
`ProcessNames`, `Outbound`), and `Rule.Validate` additionally rejects a rule that
names a rule set vender does not embed — a typo fails the build's tests rather
than a user's connection.

### 7.1 The policy

Order is the policy, because the first matching rule wins:

| # | Rule | Matches | Goes to |
|---|---|---|---|
| 1 | `private-dest-direct` | `ip_is_private` | `direct` |
| 2 | `ads-block` | `geosite-category-ads-all` | `block` |
| 3 | `service-overrides-proxy` | steam, onedrive, microsoft, google, youtube, telegram, netflix, openai, bahamut | `proxy` |
| 4 | `cn-direct` | `geosite-cn`, `geoip-cn` | `direct` |
| — | `route.final` | everything else | `proxy` |

The reference config this is derived from carried ~9500 rules (9018
`DOMAIN-SUFFIX`); aggregating them into categories is the whole point — a
category list can be updated by moving one pinned commit, a hand-maintained list
of 9000 domains cannot.

The overrides sit *before* the China rule on purpose: several of those services
have China-facing entries in `geosite-cn`, and if the China rule ran first a
Steam store page or a Microsoft sign-in would be dialled directly and fail.
`private-dest-direct` matches the **destination** only: the mixed inbound only
ever accepts connections from localhost, so matching the source would match
everything and send all traffic direct.

### 7.2 Actions

Sniffing and DNS hijacking are route *actions* in sing-box 1.13+, and they are
emitted as the first two route entries, in this order:

```json
{ "action": "sniff" },
{ "protocol": ["dns"], "action": "hijack-dns" }
```

Sniffing first, because a connection that arrives as a bare IP is just an IP
until the engine looks at its first bytes; without it no domain rule could ever
match. `hijack-dns` then hands a DNS connection to the engine's DNS module
instead of routing it as plain traffic, so an application that resolves names
"through" the proxy still gets the split policy below.

### 7.3 Split DNS

Names are resolved on the same side of the world they will be dialled from —
resolving a Chinese CDN's name through a foreign resolver (or the reverse) is how
"smart routing" produces stale or plainly wrong addresses:

| Queries for | Resolver | Reached |
|---|---|---|
| `geosite-cn` | `dns-cn` (`223.5.5.5`, UDP) | directly |
| everything else | `dns-remote` (Cloudflare DoH) | through `proxy` |

`final: dns-remote`, `strategy: prefer_ipv4`. Two sing-box details were
established by running the engine, not by reading the schema: the domestic
resolver must **not** name a `detour` (1.14 rejects a detour to the empty
`direct` outbound), and the DNS rule sets are looked up in the same
`route.rule_set` list as the routing rules, so `ReferencedRuleSets()` feeds both.

`route.default_domain_resolver` points at `dns-cn`. It is used when a rule needs
a destination's address (`ip_is_private`, `geoip-cn`) or when a `direct`
outbound dials a name; a proxied outbound never resolves locally at all — it
hands the name to the server — so a proxied lookup can never leak here. That was
verified against a live run: foreign destinations reach the proxy with no local
resolution at all.

### 7.4 Embedded rule sets ("全部内置")

`internal/rules/assets/*.srs` holds the compiled sing-box rule sets (binary
"SRS"), embedded with `go:embed`. sing-box's `route.rule_set` can only read
files, so `core.WriteConfig` materialises them into `<run dir>/rules/` before
writing the config that points at them, and skips files whose content already
matches. The generated paths are **absolute**, otherwise
`sing-box check -c <run dir>/singbox.json` would only work from the run dir.

| | |
|---|---|
| Source | `MetaCubeX/meta-rules-dat`, branch `sing` (`geo/geosite/*.srs`, `geo/geoip/*.srs`) |
| Pin | commit `d1c433471f5b` (2026-10-15) — a commit, never a branch or `latest` |
| Integrity | per-file SHA-256, checked by `scripts/fetch-rules.sh` on download and by `--check`, and re-checked against the embedded bytes by `internal/rules`' tests |
| Size | 12 sets, **506,214 B (494.3 KiB)** |
| Committed? | yes. `go build ./...` therefore works from a clean clone with no extra step, `go test` can verify the assets, and changes to binary data are reviewable as a diff. The alternative (generating an embedded `.go` at fetch time) would have made a clean clone and CI depend on a network fetch. |

`scripts/fetch-rules.sh` re-fetches or verifies them (`--list`, `--check`,
`--print-size`). A test parses the script and asserts its pinned table matches
`RuleSets()`, so the two cannot drift. The hand-maintained list of *which*
categories the policy uses is the only thing a human edits.

`vender rules list` prints the actions, the ordered rules, the DNS split and the
embedded sets with their provenance and sizes.

### 7.5 Deliberate simplifications

- **One outbound.** A single `proxy` outbound; no proxy groups and no per-rule
  node selection yet.
- **No per-user rules file.** Adding or overriding entries would slot in exactly
  where `BuiltinRules()` returns its slice; ordering, conversion and the CLI
  listing all work off that slice already, so the seam is the function, not a
  new code path.
- **No `mode: global` rules.** `global` emits no routing rules at all, and
  `direct` emits no proxy outbound and no `dns` section.
- **Category granularity.** `geosite-microsoft` covers GitHub too, so GitHub is
  proxied; that is the upstream list's composition, and it is the behaviour most
  users want from a China-facing client anyway. `geosite-cn` is one category
  rather than a hand-tuned set of exceptions.

## 8. TUI (`internal/tui`) — implemented in M3

The TUI is a daemon client, entered through a single function:

```go
// Run starts the interactive TUI and blocks until the user quits or ctx is
// cancelled.
func Run(ctx context.Context, opts Options) error
```

`Options` carries the config directory, the daemon control endpoint (a unix
socket on Linux / named pipe on Windows; empty means the platform default), and
the poll interval. `Run` returns `ErrNotImplemented` until M3.

**M3 plan:** a Bubble Tea model with three panes — server list (switch active
server), status (daemon health, active server, mode, live engine log tail from
`Engine.Logs()`), and settings (listen address, mode). Traffic through the local
inbound cannot be measured without an engine-side counter, so any "speed"
animation reflects connection activity gleaned from engine logs unless M1
exposes richer metrics. The TUI must work identically whether the daemon was
started by hand or by autostart, and must degrade gracefully (clear message, not
a crash) when no daemon is running.

## 9. Autostart (`internal/autostart`) — implemented in M4

```go
func Enable() error              // idempotent
func Disable() error             // idempotent
func Status() (State, error)
```

Per platform:

- **Linux** — a `systemd --user` unit (`vender.service`, `WantedBy=default.target`)
  when systemd is available; otherwise an XDG autostart desktop entry at
  `$XDG_CONFIG_HOME/autostart/vender.desktop`.
- **Windows** — the per-user Registry Run key
  (`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`), or a Task Scheduler
  entry when an elevated/triggered start is wanted.

`State` reports the mechanism and the concrete artifact (unit path, desktop file,
registry value), so `vender autostart status` can explain exactly what is
installed. All entry points return `ErrNotImplemented` until M4.

## 10. Milestone plan

| Milestone | Deliverable | Acceptance |
|---|---|---|
| **M0** | Skeleton, `internal/config` (config, servers, share links), CLI (`version`, `import`, `servers`), docs, unit tests, cross-compilation | `go build ./...`, `GOOS=windows go build ./...`, `go test ./...`, `go vet ./...` all clean; stubs print their milestone |
| **M1** | `internal/core` engine + `vender daemon`, `status`, `proxy on\|off` | With a real server: daemon starts sing-box, `127.0.0.1:7897` proxies HTTP and SOCKS5; stopping the daemon frees the port; `status` reflects reality |
| **M2** | `internal/rules` built-in policy, actions, split DNS and embedded rule sets, wired into the generated config; `vender rules list` | `proxy.mode: rule` sends CN/private destinations direct, blocks ads, proxies a documented override list and everything else, with matching split DNS; `sing-box check` passes for both server types and a live run shows direct vs proxied vs blocked; `global` and `direct` behave as documented |
| **M3** | `internal/tui` Bubble Tea client | TUI attaches to a running daemon, switches the active server and mode, tails engine logs, quits cleanly; behaves with no daemon |
| **M4** | `internal/autostart` | `autostart on` starts the daemon after login on both platforms; `off` removes it; `status` reports it; all idempotent |
| **M5** | Subscription URLs and bulk import, server latency testing | Importing a subscription adds all of its servers; a latency command reports a reachability ranking |
| **M6** | Hardening and release | Health-based failover across servers, log rotation, reproducible cross-builds, signed Windows binaries, user documentation |

## 11. Engineering conventions

- **Minimal dependencies.** vender uses only `cobra` and `yaml.v3` plus the
  standard library: no networking library (that is sing-box's job) and no TUI
  dependency until M3. Windows-specific calls that the standard library does not
  expose (`LockFileEx`) go through `syscall.NewLazyDLL`, which loads from
  `System32` only.
- **No OS-specific code.** Everything builds for `linux/amd64` and
  `windows/amd64`; platform differences are handled through standard-library
  abstractions (`os.UserConfigDir`, `os.UserCacheDir`). Where `syscall`-level
  work is genuinely needed, it is split behind build tags inside the relevant
  package — `internal/core/lock_{unix,windows}.go`,
  `internal/core/process_{unix,windows}.go`,
  `cmd/vender/proxy_{linux,windows}.go` — and never sprinkled through the CLI.
- **Secrets.** `servers.yaml` is `0600`; the generated `singbox.json` is `0600`
  too, because it repeats the credentials; secrets never reach logs or error
  messages (`Server.String` is redaction-safe); committed tests use only dummy
  values, and the one test that exercises the system-proxy command substitutes
  the platform implementation so `go test` cannot rewrite a developer's desktop.
- **Tests reach the edges.** Parsers, validators and file round-trips are tested
  directly, including IPv6 hosts, percent-encoded names, every parameter alias,
  missing/duplicate fields, and file permissions. The engine is tested against a
  fake binary — the test binary itself, re-entered through an environment
  variable — so the lifecycle (readiness, log streaming, idempotent stop, port
  release, restart, a child that dies, a port that never opens, an occupied
  port) is covered without sing-box or a network. Rendering is covered by golden
  files under `internal/core/testdata/`, refreshable with
  `go test ./internal/core -update`. The built-in policy is covered by its own
  tests: rule order (the order *is* the policy), that every referenced rule set
  is embedded, that the embedded bytes still hash to the pinned checksums, and
  that `scripts/fetch-rules.sh` and `internal/rules` pin the same table.
- **Committed binary assets.** The embedded `.srs` rule sets are the only
  binary data in the repository. They are committed rather than generated at
  fetch time so a clean clone builds, tests and runs offline; their provenance
  and checksums are pinned in both `scripts/fetch-rules.sh` and
  `internal/rules/assets.go`, and a test asserts the two agree.

[cobra]: https://github.com/spf13/cobra
