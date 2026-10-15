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
        |  - local control socket    |<----------+
        +-------------+--------------+           |
                      | manages                   | attaches
                      v                           |
        +----------------------------+   +--------+---------+
        |        sing-box child      |   |   vender tui     |
        |  mixed inbound 127.0.0.1:  |   |     (M3)         |
        |  7890 -> selected outbound |   |  Bubble Tea UI   |
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

This split is what makes the TUI optional and restartable, and it is why M0
ships the CLI with a clean separation between *configuration* (files on disk,
implemented now) and *control* (a running daemon, M1).

## 3. Packages

```
cmd/vender/            CLI (cobra): version, import, servers, and stubs
internal/config/       config.yaml + servers.yaml + share-link parsing  [M0]
internal/core/         engine contract + sing-box supervision            [M1]
internal/rules/        built-in routing rules -> sing-box route rules    [M2]
internal/tui/          Bubble Tea TUI (daemon client)                    [M3]
internal/autostart/    start-at-login registration                       [M4]
docs/                  this document
scripts/               build helpers
```

Dependency direction is strictly downward:

```
cmd/vender  ->  internal/tui (M3)
            ->  internal/config
internal/core  ->  internal/config
internal/rules ->  (pure data; consumed by core)
internal/tui   ->  internal/config (+ a daemon control client, M3)
internal/autostart -> (standalone)
```

No package imports `cmd/`. `internal/config` has no dependency on OS-specific
APIs: it defers the config directory to `os.UserConfigDir()`, which resolves to
`~/.config/vender` on Linux and `%AppData%\vender` on Windows.

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
    listen: 127.0.0.1:7890      # host:port of the mixed inbound
    mode: rule                  # rule | global | direct
engine:
    binary: ""                  # empty => bundled next to vender, else PATH
selected_server: ""             # name of the active entry in servers.yaml
```

`Config.Normalize` fills zero values with defaults and canonicalises casing, so
a partial file behaves like a documented one. `Config.Validate` rejects an
unknown log level, an unknown mode, and a malformed or wildcard listen address
(a wildcard listener would expose the proxy, so it is treated as a mistake
rather than a convenience).

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

| Command | M0 state |
|---|---|
| `vender version` | implemented |
| `vender import <share-link>` | implemented (`--name`, `--force`) |
| `vender servers list` | implemented (`--links`) |
| `vender servers rm <name>` | implemented |
| `vender daemon` | stub — M1 |
| `vender status` | stub — M1 |
| `vender proxy on\|off` | stub — M1 |
| `vender tui` | stub — M3 |
| `vender autostart on\|off\|status` | stub — M4 |

A stub is a real, documented command that exits with
`<name>: not implemented in this milestone (Mx)`. This keeps the CLI surface and
exit codes stable across milestones, so scripts and the TUI can be written
against the final command set before the implementations land.

## 6. Engine contract (`internal/core`) — implemented in M1

The engine is the only seam between vender and sing-box. M0 ships the interface
and the resolved runtime input; M1 implements it.

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

`core.New` currently returns `ErrNotImplemented`; M1 returns a real
implementation. `RuntimeConfig.Validate` already enforces the invariants M1
relies on (a run dir, a valid listen address, and a selected server whenever the
mode is not `direct`).

**M1 work:** map `RuntimeConfig` to sing-box JSON (`inbounds` = one mixed
inbound; `outbounds` = the selected server plus `direct`; `route` from
`internal/rules`). Supervision details to settle in M1: where to put the run dir
(`os.UserCacheDir()/vender` is the natural choice), how to detect listener
readiness (sing-box startup log lines vs. a TCP dial probe), backoff on crash,
and one daemon per user (a lock file or pidfile in the run dir).

## 7. Routing rules (`internal/rules`) — implemented in M2

M0 defines the data model and the entry point:

```go
// BuiltinRules returns vender's default rule set, in evaluation order.
func BuiltinRules() []Rule
```

`Rule` expresses routing in vender's own terms (`Domains`, `DomainSuffixes`,
`DomainKeywords`, `IPCIDRs`, `Ports`, `ProcessNames`, `Outbound`), so the rule
model never leaks sing-box's JSON schema into the rest of the codebase. M2
converts rules to sing-box `route.rules`, and adds the built-in sets: private
IPs direct, LAN direct, China direct, ads blocked. `BuiltinRules` returns `nil`
until then, which callers must read as "no routing rules configured" (route
everything through the proxy), not as "match nothing".

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
| **M1** | `internal/core` engine + `vender daemon`, `status`, `proxy on\|off` | With a real server: daemon starts sing-box, `127.0.0.1:7890` proxies HTTP and SOCKS5; stopping the daemon frees the port; `status` reflects reality |
| **M2** | `internal/rules` built-in rule sets wired into the generated config | `proxy.mode: rule` sends CN/private destinations direct and everything else through the proxy; `global` and `direct` behave as documented |
| **M3** | `internal/tui` Bubble Tea client | TUI attaches to a running daemon, switches the active server and mode, tails engine logs, quits cleanly; behaves with no daemon |
| **M4** | `internal/autostart` | `autostart on` starts the daemon after login on both platforms; `off` removes it; `status` reports it; all idempotent |
| **M5** | Subscription URLs and bulk import, server latency testing | Importing a subscription adds all of its servers; a latency command reports a reachability ranking |
| **M6** | Hardening and release | Health-based failover across servers, log rotation, reproducible cross-builds, signed Windows binaries, user documentation |

## 11. Engineering conventions

- **Minimal dependencies.** M0 uses only `cobra` and `yaml.v3` plus the standard
  library. The TUI and networking dependencies arrive with the milestone that
  needs them.
- **No OS-specific code.** Everything builds for `linux/amd64` and
  `windows/amd64`; platform differences are handled through standard-library
  abstractions (`os.UserConfigDir`). Where a milestone genuinely needs
  `syscall`-level work (autostart, control sockets), it will be split behind
  build tags inside the relevant package, not sprinkled through the CLI.
- **Secrets.** `servers.yaml` is `0600`; secrets never reach logs or error
  messages; committed tests use only dummy values.
- **Tests reach the edges.** Parsers, validators and file round-trips are tested
  directly, including IPv6 hosts, percent-encoded names, every parameter alias,
  missing/duplicate fields, and file permissions.

[cobra]: https://github.com/spf13/cobra
