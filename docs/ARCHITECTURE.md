# vender — Architecture

This document describes how `vender` is put together, what each milestone adds,
and the interfaces that later milestones compile against today.

## 1. Goals and non-goals

**Goals**

- A single small binary that gives a Linux / Windows 11 user a working local
  proxy with minimal ceremony.
- Five outbound protocols: VLESS + Vision + REALITY (TCP), Hysteria2 (UDP), and
  — since M7 — Shadowsocks, VMess and Trojan, which is what a commercial
  provider ("机场") serves.
- A local **mixed SOCKS5 + HTTP** inbound so applications opt in per app, which
  stays the default and the primary way to use vender.
- An **optional TUN mode** (M6) for the traffic that cannot be configured per
  application, sharing the same engine, the same policy and the same control
  channel as the mixed inbound.
- Configuration that is plain YAML, human-readable, and safe on disk.

**Non-goals**

- TUN mode **off by default**, and never switched on by vender itself: it changes
  the host's routing table, so it is an explicit `vender tun on`.
- No subscription marketplace, no telemetry, no accounts. A subscription is a
  URL *the user already has*; vender fetches it and forgets the rest.
- No attempt to reimplement the proxy protocols; sing-box does that.
- No SIP003 Shadowsocks plugins (M7): a link that needs one is rejected by name
  rather than connecting without the obfuscation the server expects.
- No attempt to reimplement the kernel's job: the routing rules, the fwmarks and
  the device are sing-box's, driven by vender's own `engine.tun` block.

## 2. Process model

```
        +----------------------------+
        |        vender daemon       |   authoritative, headless
        |  (M1, control channel M3)  |
        |  - loads config.yaml       |
        |  - loads servers.yaml      |
        |  - renders singbox.json    |
        |  - supervises sing-box     |
        |  - run dir: lock, pid,     |
        |    control.json (M3)       |<----------+
        +-------------+--------------+           |
                      | manages                   | attaches (control channel)
                      v                           |
        +----------------------------+   +--------+---------+
        |        sing-box child      |   |   vender tui     |
        |  mixed inbound 127.0.0.1:  |   |     (M3)         |
        |  7897 -> selected outbound |   |  Bubble Tea UI   |
        |  + optional tun inbound    |   |  (M6: tun toggle)|
        |    (M6, vender0)           |   |                  |
        |  clash api 127.0.0.1:<auto>|   +------------------+
        +----------------------------+            ^
                      ^                           | CLI commands
                      | local apps                |
        +----------------------------+   +--------+---------+
        |  browser / curl / ...      |   |  vender CLI      |
        +----------------------------+   | import, servers, |
                                         | profiles, status |
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
say what it is doing.

M3 adds a real control channel (§8) for the operations a file cannot express —
switching server, changing mode, starting and stopping the engine, tailing
engine output, streaming live traffic — and it is from that point on that
clients stop reading the generated config directly. The one thing the TUI still
reads itself is the *user's* configuration (`config.yaml`, `servers.yaml`) and
the built-in policy, which is what lets the Nodes and Settings screens be
useful with no daemon running at all.

Two directories, deliberately:

| Directory | Contents | Lifetime |
|---|---|---|
| `os.UserConfigDir()/vender` | `config.yaml`, `servers.yaml`, `profiles.yaml` | the user's; editable, backable-up, never overwritten by vender except through `import`/`servers`/`profiles`/`select` |
| `os.UserCacheDir()/vender` | `singbox.json`, `rules/*.srs`, `vender.lock`, `vender.pid`, `control.json` | the daemon's; disposable, regenerated on every start |

`--config-dir` and `--run-dir` override both, which is what keeps the test
suite from touching a real installation.

## 3. Packages

```
cmd/vender/            CLI (cobra): version, import, servers, profiles, rules,
                       daemon, status, proxy, autostart, tui
internal/config/       config.yaml + servers.yaml + profiles.yaml + share-link
                       parsing                                             [M0, M7]
internal/subscription/ subscription fetching + parsing (links, base64, Clash) [M7]
internal/core/         engine contract + sing-box rendering + supervision [M1]
                       + the Clash API client used for traffic and latency  [M3]
internal/rules/        built-in routing policy + embedded rule sets        [M2]
internal/control/      daemon control channel: protocol, server, client,
                       and the controller that owns the engine             [M3]
internal/tui/          Bubble Tea TUI (a control-channel client)           [M3]
internal/autostart/    start-at-login registration                         [M4]
docs/                  this document
scripts/               build + release helpers (build.sh, package.sh,
                       install.sh, fetch-rules.sh, fetch-sing-box.sh)
```

Dependency direction is strictly downward:

```
cmd/vender     ->  internal/core
               ->  internal/control
               ->  internal/tui (M3)
               ->  internal/config
               ->  internal/subscription (M7)
               ->  internal/rules
internal/core  ->  internal/config
               ->  internal/rules
internal/subscription -> internal/config (only for the share-link parser; the
                     parsing is pure, Fetch is net/http)
internal/rules ->  (pure data + embedded assets; imports nothing of vender's)
internal/control -> internal/core
                 -> internal/config
                 -> internal/rules
internal/tui   ->  internal/control
               ->  internal/config
               ->  internal/rules
               ->  internal/autostart  (M4: the Settings screen registers it)
internal/autostart -> (standalone; it reads $HOME/$XDG_CONFIG_HOME and runs
                   systemctl/loginctl or schtasks/reg, and nothing else)
```

No package imports `cmd/`. `internal/config` has no dependency on OS-specific
APIs: it defers the config directory to `os.UserConfigDir()`, which resolves to
`~/.config/vender` on Linux and `%AppData%\vender` on Windows. `internal/core`
owns the two OS-specific seams it needs (the lock and the child process group)
behind build tags, so the CLI stays platform-free above it. M3's control
channel needed no new platform seam at all — that is the whole argument for
loopback TCP (§8).

## 4. Configuration layer (`internal/config`)

### 4.1 Files

| File | Mode | Contents |
|---|---|---|
| `config.yaml` | `0644` | global settings; contains no secrets |
| `servers.yaml` | `0600` | the server list; contains credentials |
| `profiles.yaml` | `0600` | the profile registry (M7): which profiles exist, each one's subscription URL and last fetch time, and which one is active. `0600` because a subscription URL often carries a token |
| `rules.yaml` | `0644` | optional per-user routing rules (M5); absent means "built-in policy, unmodified" |

The config directory itself is created `0700`. Writes are atomic: content is
written to a temp file in the same directory, chmodded, `fsync`ed, then
`rename`d over the target, so a crash or a concurrent reader never sees a
half-written file. `servers.yaml` also has its mode forced on every save, which
repairs a file whose permissions drifted.

A missing file is not an error — a fresh install loads built-in defaults.
`rules.yaml` is the one file vender never creates on its own: an absent file
means the built-in policy applies unchanged, and only `vender rules init` (or a
user's editor) writes it.

### 4.2 `config.yaml`

```yaml
log_level: info                 # debug | info | warn | error
proxy:
    listen: 127.0.0.1:7897      # host:port of the mixed inbound
    mode: rule                  # rule | global | direct
engine:
    binary: ""                  # empty => bundled next to vender, else PATH
    bind_interface: ""          # pin the proxy outbound to an interface
    failover_interval: 30s      # URLTest probe cadence (auto); 5s-10m
    failover_tolerance_ms: 100  # URLTest switch threshold; 0 = engine default
    tun:                        # M6: optional system-wide capture
        enabled: false          # off by default
        stack: mixed            # system | gvisor | mixed
        mtu: 0                  # 0 => the engine's own default (9000)
        auto_route: true        # install the routes that pull traffic in
        strict_route: false     # refuse traffic that would bypass the tunnel
        interface_name: vender0
        address: []                     # enabled + unspecified => IPv4/IPv6 defaults
        route_exclude_address: []        # CIDRs kept out of the tunnel
        route_exclude_address_set: []    # rule-set tags kept out of the tunnel
selected_server: ""             # a name in servers.yaml, or "auto" (failover
                                # within the active profile; see §4.5)
```

`selected_server: auto` renders a URLTest group over the servers of the **active
profile** instead of a single `proxy` outbound, so a degraded node is bypassed
without anyone switching anything. `Config.IsAutoServer` recognises the sentinel
(case-insensitively), and `Config.SelectionInProfile` is the single place the
daemon and the controller agree on what it means. With no `profiles.yaml` the
active profile is the built-in `manual` one, every server is manual, and the
pool is therefore every server — byte-identical to M5's behaviour. See §4.5.

`engine.tun.auto_route` is a `*bool` so that "absent" means the documented
default (`true`) while an explicit `auto_route: false` is honoured: a plain
`bool` cannot tell the two apart once YAML has decoded them, and the difference
here is whether the host's routing table is touched. `Config.Normalize` fills
the pointer in, so what is written back to `config.yaml` is always explicit and
self-documenting.

A **disabled** `engine.tun` block is validated by nobody and rendered by nothing.
That is deliberate: a typo in a feature that is switched off must not stop vender
from starting, and it keeps the TUN knob-set additive.

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
      type: vless               # vless | hysteria2 | shadowsocks | vmess | trojan
      server: 203.0.113.10      # host or IP
      port: 443
      profile: provider         # M7; absent => the built-in "manual" profile
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
      # --- shadowsocks ---
      method: aes-256-gcm       # or 2022-blake3-aes-256-gcm, ... (M7)
      # --- vmess (M7) ---
      cipher: auto              # the vmess "scy"
      alter_id: 0               # the vmess "aid"
      # --- vmess + trojan transport (M7) ---
      path: /ray                # ws path / grpc service name / http path
      host: cdn.example.com     # ws / http Host
```

Validation is per type. A `vless` entry requires a well-formed UUID, and with
`security: reality` it additionally requires `public_key` and `sni`; with
`security: tls` it requires `sni`. A `hysteria2` entry requires `password`, and
`obfs_password` must be present if and only if `obfs` is set. Only vless
`network: tcp` is accepted; a vless `ws`/`grpc` link fails loudly instead of
silently misbehaving (VMess and Trojan do support `tcp`/`ws`/`grpc`). A
`shadowsocks` entry requires a `method` vender knows (checked against the list
sing-box accepts, so a typo is reported by vender, not the engine) and a
`password`. A `vmess` entry requires a UUID and accepts `none`/`tls`; a
`trojan` entry requires a password and is always TLS. REALITY is a VLESS-only
transport: a `vmess` entry with `security: reality` is rejected rather than
silently rendered as plain TLS.

`Server.Normalize` applies safe defaults (`network: tcp`,
`fingerprint: chrome`, `header_type: none`, `cipher: auto` for vmess) but
deliberately does **not** invent a `flow`: a REALITY link that omits `flow` means
the server expects a plain stream, and defaulting it would break that server. It
also folds `profile: manual` back to absent, so the implicit profile never
appears in the file and a pre-M7 `servers.yaml` round-trips byte for byte.

`Server.String` is redaction-safe — it prints only type, name and endpoint — so
it can be embedded in errors and logs. The only code that emits credentials is
`Server.ShareLink()` (used by `servers list --links`).

### 4.4 Share-link parsing (`internal/config/sharelink.go`)

`ParseShareLink` accepts every supported scheme and yields a normalized
`Server`; `Server.ShareLink()` renders the canonical inverse. Both directions
are covered by tests, including round-trips.

| Scheme | Form |
|---|---|
| `vless` | `vless://<uuid>@<host>:<port>?<params>#Name` |
| `hysteria2` / `hy2` | `hysteria2://<password>@<host>:<port>/?<params>#Name` |
| `ss` | SIP002 `ss://<base64(method:password)>@<host>:<port>/?<params>#Name` **and** legacy `ss://<base64(method:password@host:port)>#Name` |
| `vmess` | `vmess://<base64(json)>` (the v2rayN object) |
| `trojan` | `trojan://<password>@<host>:<port>?<params>#Name` |

`ss://` and `vmess://` are **not** parsed with `net/url`: their payload is a
base64 blob that legitimately contains `/` and `+`, which `net/url` would read as
a path or a space. Both are scanned by hand, and any of the four base64
spellings (standard/URL-safe, padded/unpadded) is accepted, because panels
disagree and the difference is never meaningful.

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

**Shadowsocks**

SIP002 puts the credentials in the userinfo as base64 (`method:password`); the
legacy form puts *everything* in one base64 blob and carries no userinfo at all.
The parser distinguishes the two by the presence of a literal `@` (which cannot
occur in the base64 alphabet). A percent-encoded `method:password` userinfo is
accepted too, as some panels emit it. `?plugin=…` is **rejected by name**:
vender does not implement SIP003 plugins, and connecting without the
obfuscation the server expects would fail in a way that looks like a dead node.

**VMess**

The payload is a JSON object (`v, ps, add, port, id, aid, scy, net, type, host,
path, tls, sni, alpn`). It is decoded leniently because panels disagree on
types: `port` and `aid` may be numbers or quoted numbers, `tls` may be `true` or
`"tls"`, `alpn` may be a comma-separated string or an array. `net` is mapped to
vender's transport (`tcp`, `ws`, `grpc`; `http`/`h2` fold into `tcp` with an
`http` header, since that is what they mean on the wire); `kcp`/`quic` and other
transports vender does not support are rejected with the offending field named.
For gRPC the service name travels in `path`, which is where v2rayN puts it.

**Trojan**

`type` is the transport (`tcp`, `ws`, `grpc`), `path`/`host` are the transport's
destination, `allowInsecure`/`insecure`/`skip-cert-verify` are accepted as the
insecure flag. Security is always TLS; there is no plaintext Trojan.

**Names.** The server name is the URL fragment (percent-decoded by
`url.Parse`), or the `ps` field for VMess. With no name the name falls back to
`host:port`, bracketed for IPv6.

**IPv6.** A link such as `vless://<uuid>@[2001:db8::1]:443?...` stores the bare
literal in `server` and re-brackets on output via `net.JoinHostPort`, so
`Endpoint()` and `ShareLink()` are always correctly bracketed.

`ParseShareLink` reports typed errors: `ErrInvalidLink` for a recognized-but-
broken link, `ErrUnsupportedScheme` for e.g. `wireguard://`. Messages name the
exact component that failed (missing uuid, missing port, out-of-range port, bad
boolean, unsupported encryption, a plugin that is not implemented).

### 4.5 Profiles and subscriptions (`internal/config/profiles.go`, `internal/subscription`)

A **profile** is a named group of servers. It exists for exactly one purpose:
`selected_server: auto` otherwise has no way to know which nodes belong together,
and a user with both hand-typed nodes and a provider's subscription wants
"fail over between my provider's nodes", not "between all sixteen of them".

**Membership lives on the server; metadata lives in a separate file.** Each
`Server` carries `profile` (absent ⇒ the built-in `manual` profile), and
`profiles.yaml` records only where a group came from: the subscription URL, the
last fetch time, and the active profile's name. Three consequences are the whole
argument for this split:

- `servers.yaml` alone is sufficient to know which servers belong together, so a
  hand-edited file (or one copied between machines) cannot end up with a profile
  that references servers it does not have;
- `profiles.yaml` is safe to lose — the worst case is forgetting the URL and
  falling back to `manual` — which is why it can be a small, disposable file;
- a pre-M7 `servers.yaml` has no `profile` key, every entry loads as `manual`,
  and the active profile therefore defaults to `manual`, so `auto` keeps
  covering every server. **That is the compatibility guarantee, and it is
  asserted by a test.**

```go
const ProfileManual = "manual"  // built in; never stored; cannot be removed
const ProfileAll    = "all"     // "every server"; a reserved *name*, not a profile

type Profile struct { Name, URL string; FetchedAt time.Time; DNS *ProfileDNS }
type ProfileSet struct { Active string; Profiles []Profile }

func (ps *ProfileSet) ActiveName() string            // never empty
func (ps *ProfileSet) Get(name string) (*Profile, bool)
func (ps *ProfileSet) Upsert(p Profile) *Profile
func (ps *ProfileSet) Remove(name string) bool

func KnownProfiles(ss *ServerSet, ps *ProfileSet) []string   // manual + registry + server-carried
func KnownProfileName(ss *ServerSet, ps *ProfileSet, name string) (string, bool)

func (ss *ServerSet) InProfile(name string) []Server          // "" / "all" => every server
func (ss *ServerSet) CountInProfile(name string) int
func (ss *ServerSet) ProfileNames() []string
func (ss *ServerSet) RemoveProfile(name string) (int, error)
func (ss *ServerSet) UpsertProfileServers(profile string, servers []Server, force bool) (ImportStats, error)
func (ss *ServerSet) RemoveServersExcept(profile string, keep []string) []string

func (s *Store) LoadProfiles() (*ProfileSet, error)
func (s *Store) SaveProfiles(*ProfileSet) error
func (s *Store) SelectProfile(name string) (string, error)
func (s *Store) RemoveProfile(name string) (int, error)
func (s *Store) ProfileImport(profile, url string, at time.Time, servers []Server, replace, force bool) (ImportStats, string, error)
```

`KnownProfiles` is a **union**, because either half can exist alone: a
hand-edited `servers.yaml` can name a profile the registry has never heard of,
and an imported profile can have all its servers deleted by hand. `profiles list`
prints that union, so what you see is what `auto` can actually use.

**Import semantics.** `UpsertProfileServers` updates a name that already exists
*inside the profile* (that is what re-importing means), leaves an entry that is
already present verbatim alone (so a repeated import does not churn
`servers.yaml`), and reports a name held by a *different* profile as a conflict
instead of silently stealing it — `--force` overrides, `--name-prefix` avoids it
altogether. `ProfileImport(replace: true)` is the refresh path: it drops the
profile's stale nodes first. When a removed node was `selected_server`, the
selection falls back to `auto`, because a `selected_server` naming a server that
no longer exists would make the daemon refuse to start.

**The subscription package** (`internal/subscription`) is deliberately split so
that only one function touches the network:

| Function | Network? | Role |
|---|---|---|
| `Parse(data) (Result, Format, error)` | no | auto-detect and parse; `Result` carries the usable servers and one `Skipped{Node, Reason}` per unusable node |
| `ParseLinks(text)` | no | newline-separated share links (blank/`#`/`//` lines ignored; duplicates dropped by name) |
| `ParseClash(data)` | no | Clash / Mihomo YAML with a `proxies:` list |
| `Fetch(ctx, client, url)` | yes | `http.Client` + `User-Agent: v2rayN/6.0`, ctx- and size-bounded |
| `DeriveProfileName(url)` | no | the default profile name (the URL's host) |

Auto-detection order: a `proxies:` key ⇒ Clash YAML; else plain links; else
base64-of-links; else YAML. The v2ray user agent matters — an unknown agent is
often served an HTML page or a Clash config instead of links — and a Clash
response is handled anyway, which is why the YAML shape is a first-class parser
rather than a fallback.

Parsing is pure and tested against fixtures with **no network at all**: the
fetch tests inject an `http.Client` with a fake `RoundTripper` and assert the user
agent, so `go test` never opens a socket (not even loopback). Nodes that cannot
be used are skipped *with a reason* rather than failing the subscription — a
provider's list routinely contains a protocol vender does not implement, and a
user should get the other 40 nodes.

### 4.6 Profile DNS policy (`internal/config/profiledns.go`)

A commercial provider's node hostnames are special "entry" domains, e.g.
`1.k.o.5.j.m.k.d.1.hk01-ae5.entry.v51124-6.qpon`, whose **public** answers are a
decoy: a blackhole from outside China, and a shared "China" address that does
not carry the provider's traffic. The provider's Clash config states which
resolver answers for them:

```yaml
dns:
  nameserver-policy:
    +.v51124-6.qpon: tcp://f892aaba…fyi:8080
```

M7 imported only `proxies:` and ignored that, so every Shadowsocks node was
dialled at the wrong address and the tunnel carried no traffic. M8 captures the
policy at import and stores it next to the profile's URL:

```go
type DNSNamespacePolicy struct { Domain, Server string }

type ProfileDNS struct {
    NamespacePolicy []DNSNamespacePolicy // dns.nameserver-policy, ordered
    Nameservers     []string             // dns.nameserver   (captured)
    Fallback        []string             // dns.fallback     (captured)
    Raw             string               // the source `dns:` block, for audit
}

func ParseResolverAddress(s string) (ResolverAddress, error) // tcp/udp/tls/https/bare
func NormalizeDNSPattern(p string) (string, bool)            // +.x / *.x / .x / x -> x
func (d *ProfileDNS) Match(host string) string               // longest suffix wins
func (d *ProfileDNS) SetNamespacePolicy(domain, server string) error
func (ps *ProfileSet) DNSMap() map[string]*ProfileDNS
func (s *Store) SetProfileDNS(name string, dns *ProfileDNS) (string, error)
```

`Profile.DNS` is nil when a profile has none, and the whole feature is a no-op
then: an absent policy renders nothing at all. The shape written to
`profiles.yaml` is documented on the type; the `Raw` block keeps what the
provider actually said even if vender's parsing changes.

Three decisions are worth recording:

- **Only `nameserver-policy` is applied.** It is an explicit, per-domain
  instruction, and it is what distinguishes the provider's entry domains from
  everything else. `nameserver`/`fallback` are captured because they are part of
  what the provider said, but pointing *every* node hostname at them would
  reroute nodes that intentionally resolve through public DNS — the same
  subscription carries VMess nodes under `entry.rtysjur.quest` and
  `entry.fr0528.art` that work fine today — so vender does not.
- **Patterns are normalized to an explicit suffix.** Clash's `+.`/`*.`/`.`
  prefixes all become "this name and its subdomains"; a rule-set key
  (`geosite:cn`, `rule-set:…`), which cannot be matched against a hostname, is
  dropped from the applied policy but stays visible in `Raw`.
- **A hand-set policy is sticky.** `ProfileImport` with a nil policy leaves an
  existing one alone, so `profiles update` of a subscription that (now) declares
  nothing cannot silently undo a `profiles set-dns` fix.

`vender profiles list` gains a `DNS` column, `vender profiles show` prints the
policy in full, and `vender profiles set-dns <name> --domain D --server S`
(repeatable, `--clear`) declares or fixes it by hand.

## 5. CLI (`cmd/vender`)

Built with [cobra]. Each subcommand is constructed by a method on a shared
`app` struct, which resolves the config `Store`. The global `--config-dir` flag
points the whole CLI at an alternative directory, which is how the CLI is tested
without touching the user's real configuration.

| Command | State |
|---|---|
| `vender version` | implemented (M0) |
| `vender import <share-link>` | implemented (M0) (`--name`, `--force`) |
| `vender import --url <subscription> [--profile N] [--name-prefix P]` | implemented (M7) — fetch, parse (links / base64 / Clash), file under a profile, per-node summary |
| `vender servers list` | implemented (M0) (`--links`) |
| `vender servers use <name\|auto>` | implemented (M5) — selects a server, or `auto` for failover; restarts a running daemon |
| `vender servers rm <name>` | implemented (M0) |
| `vender profiles list` | implemented (M7; DNS column M8) — name, node count, DNS policy summary, source URL, last-updated, active |
| `vender profiles show [name]` | implemented (M8) — one profile in full, including its DNS policy |
| `vender profiles use <name\|all>` | implemented (M7) — makes a profile active; restarts a running daemon |
| `vender profiles update [name]` | implemented (M7) — re-fetch and reconcile |
| `vender profiles set-dns <name>` | implemented (M8) — set/override the profile's node-hostname DNS policy (`--clear`) |
| `vender profiles rm <name>` | implemented (M7) — removes a profile's servers; refuses the built-in `manual` |
| `vender rules list` | implemented (M2; merged policy in M5) — the ordered policy, DNS split and embedded sets |
| `vender rules path` / `vender rules init` | implemented (M5) — the user rules file (`rules.yaml`) |
| `vender daemon` | implemented (M1) — foreground, one per run dir |
| `vender status` | implemented (M1) — exit code means "usable" |
| `vender proxy on\|off` | implemented (M1 Linux/GNOME, M4 Windows/WinINET) |
| `vender tun on\|off\|status` | implemented (M6) — persists `engine.tun.enabled`, restarts the engine through the daemon, `status` prints the privilege verdict and exits non-zero when TUN is off |
| `vender tui` | implemented (M3) — the TUI; prints a snapshot when not a terminal |
| `vender autostart on\|off\|status` | implemented (M4) — `status` exits non-zero when disabled, `--json` is machine-readable |

A stub is a real, documented command that exits with
`<name>: not implemented in this milestone (Mx)`. This keeps the CLI surface and
exit codes stable across milestones, so scripts and the TUI can be written
against the final command set before the implementations land.

## 6. Engine (`internal/core`) — implemented in M1, TUN inbound in M6

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
  from `proxy.listen`, and — when TUN mode is on — a `tun` inbound tagged
  `tun-in` (§6.5), which shares the struct and the `omitempty` tags so the
  TUN-off output is byte-identical to M1/M2's;
- the **proxy** outbound (`vless` with `flow` + `tls.utls` + `tls.reality`,
  `hysteria2` with `password` + `obfs` + `tls`, `shadowsocks` with
  `method` + `password`, `vmess` with `uuid` + `security` + `alter_id`, or
  `trojan` with `password` + `tls`; vmess and trojan also carry a
  `transport` — `ws` with a `Host` header, `grpc` with a `service_name`, or
  `http` for an HTTP-framed TCP stream), always tagged `proxy` — or, in
  `selected_server: auto` mode, one outbound per server in the **active
  profile's pool** plus a `urltest` group **also tagged `proxy`** (§7.5), so
  nothing downstream changes. The renderer only ever sees the pool
  (`RuntimeConfig.Servers`), so `core` needs no knowledge of profiles at all;
- a `direct` outbound and a `block` outbound, always present so that rules
  written against `rules.OutDirect` / `rules.OutBlock` always have a target;
- `route.final` = `proxy` for `rule`/`global` and `direct` for `direct`;
- in **rule** mode, `route.rule_set` (one `local`/`binary` entry per embedded
  set, with an **absolute** path inside the run dir), then `route.rules`: the
  actions (`sniff`, `hijack-dns`) first, followed by the destination rules from
  `internal/rules.EffectiveRules(user)` (critical + user + built-in), plus
  `route.default_domain_resolver` and the `dns` section from
  `internal/rules.RuleModeDNS(user)` (§7.3);
- in **global** mode, a `dns` section with a single proxied resolver and **no**
  routing rules — `global` must stay global, so emitting the CN-direct rules
  there would silently turn it into `rule`;
- in **direct** mode, neither: there is no proxy to resolve through, so
  sing-box's own default (the system resolver) is right;
- `engine.bind_interface`, when set, is passed through to the outbound, which
  pins the connection to the proxy server to a specific interface on a
  multi-homed (or TUN-hijacked) host.
- a server whose profile declared a DNS policy for its hostname carries a
  `domain_resolver` naming the profile's resolver, and that resolver is added to
  `dns.servers` (§7.7). Nothing else changes when no policy applies.

M3 adds a sixth top-level section, `experimental`, and only when the caller
asks for it:

- `Route`, `Outbounds` and the rest are unchanged; `RuntimeConfig.ClashListen`
  non-empty asks for sing-box's **Clash API**, which is what gives the TUI live
  traffic counters and a latency measurement that goes *through* the tunnel
  (§8.3). It is validated rather than passed through blindly: the address must
  be loopback and a secret is mandatory, because an unauthenticated local API
  can switch the active outbound. Leaving `ClashListen` empty emits no
  `experimental` section at all, which keeps every M1/M2 golden file and any
  hand-written config byte-identical.

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

### 6.5 TUN mode (M6)

TUN mode is an *inbound*, not a second engine: `BuildConfig` emits a `tun`
inbound next to the ever-present `mixed` one, and everything downstream — the
outbound, the built-in policy, the split DNS, the Clash API — is unchanged. A
user who turns TUN on keeps `127.0.0.1:7897` working, and `vender tun off`
restores exactly the M1/M2 document, byte for byte (a golden test asserts that:
TUN off emits nothing TUN-shaped, not even the device name).

```go
func buildTunInbound(t config.TunConfig) singboxInbound
func declaredRuleSets(cfg config.Config) []string  // policy sets (rule mode) ∪ TUN exclusions
func validateTunRuleSets(cfg config.Config) error
func CheckTUNPrivilege(engineBinary string) TUNPrivilege
```

Five decisions are worth recording, because each one was forced by something
concrete:

- **`address`, never `inet4_address`.** The pinned engine refuses the legacy
  pair by name ("removed in sing-box 1.12.0"). The modern `address` is a list of
  CIDRs and is left empty by default, so sing-box picks its own private `/30`;
  that default does not collide with the `198.18.0.0/15` a mihomo/clash TUN uses.
- **`stack` is always written**, even when it equals the engine's default. The
  engine accepts `stack: "bogus"` without complaint, so a typo would otherwise
  be a silent behaviour change; vender validates the value itself and emits it
  explicitly.
- **`global` mode gains the two actions, and nothing else, under a TUN.**
  Without `hijack-dns`, DNS would leave the host as raw packets to port 53 and
  "everything through the proxy" would be false for the one protocol every
  connection starts with; without `sniff`, the proxied outbound would only ever
  see raw IPs. Actions select no outbound, so global stays global — the golden
  case asserts that it emits exactly those two rules and no destination rules.
  `direct` mode gets no actions at all: it must not intercept anything.
- **`route.auto_detect_interface` is emitted when a TUN is up** and
  `engine.bind_interface` is not set. It binds the engine's own outbound to the
  interface holding the default route, which is what keeps the connection *to
  the proxy server* from being routed back into the engine's own tunnel.
- **Rule-set exclusions are declared, not assumed.** `route_exclude_address_set`
  names tags that must appear in `route.rule_set` or `sing-box check` refuses the
  config, so `declaredRuleSets` returns the union of the policy's sets (rule
  mode) and the TUN exclusions, and `WriteConfig` extracts the `.srs` files
  whenever that list is non-empty — including in `global` and `direct` mode,
  which otherwise declare none. `RuntimeConfig.Validate` rejects a tag vender
  does not embed, with the same "typos fail in tests, not in a user's tunnel"
  rule the policy itself follows.

**Privileges.** `CheckTUNPrivilege` is the only platform-specific piece of M6,
behind `privilege_{linux,windows,other}.go`. On Linux it answers "will
`sing-box run` get CAP_NET_ADMIN?" by checking, in order of cost: euid 0; the
`CapEff` mask in `/proc/self/status`; and the `security.capability` xattr of the
engine binary (`setcap ...+ep`), which the kernel grants at exec time. The xattr
decode is pure and unit-tested against a real value (a copy of ping's), including
the case that matters — permitted-but-**not**-effective capabilities, which still
fail `capable(CAP_NET_ADMIN)`.

`engine.Start` consults it *before* spawning, so the failure is one sentence
naming the three legitimate fixes instead of the engine's own
`configure tun interface: open tun: TUNSETIFF: operation not permitted`. On
Windows the check reads `TokenElevation` through `advapi32.dll` with the same
`syscall.NewLazyDLL` route the lock already takes, so it adds no dependency; it
is the one part of M6 that cannot be executed off Windows.

**Verifying TUN mode without endangering the host.** A second `auto_route` TUN
next to a working one hijacks the default route and the two fight, so the
verification recipe is deliberately narrow:

1. **Config level, always.** Golden files for TUN off/on in all three modes, plus
   `sing-box check -c <generated>` for each — the only check that can catch a
   field name the engine does not know. `internal/core/singboxcheck_test.go`
   does exactly that against a real engine binary and **skips** when none is
   present, so a clean clone still passes `go test ./...`.
2. **Live, in a network namespace** (preferred; needs root once):
   `ip netns add vendertest`, `ip link add … type veth peer …`, an address and
   an `iptables -t nat -A POSTROUTING -s … -j MASQUERADE` on the host side, then
   run `sing-box run -c <generated>` *inside* the namespace. Nothing the engine
   does there can touch the host's routes.
3. **Live, fallback**, when namespaces are not available: `auto_route: false`,
   `ip route add <one-test-ip>/32 dev vender0` for a single address, curl that
   one address, then `ip link del vender0`. The default route is never touched.
4. **Otherwise, stop.** If neither is possible — no root, unprivileged user
   namespaces disabled, a TUN already installed by something else — the honest
   answer is that the *data path* is unverified. What can still be proven is
   that the generated config is accepted and initialised by the real engine
   (it reaches the device and fails on `TUNSETIFF: operation not permitted`,
   which is exactly the privilege boundary), and that vender's own pre-flight
   check intercepts that case with the actionable message first.

### 6.6 System proxy (`cmd/vender/proxy*.go`)

`proxy on` writes the GNOME proxy schema (`org.gnome.system.proxy.*`) through
`gsettings`, setting the endpoints before flipping the mode so a failure part
way through leaves the proxy disabled rather than half-configured; `proxy off`
restores `mode: none`. It also prints the `http_proxy` / `https_proxy` /
`all_proxy` exports for shells that ignore the desktop settings. Nothing else is
touched: no TUN, no daemon state.

Windows (M4) writes the per-user WinINET settings — `ProxyEnable`,
`ProxyServer` (`http=host:port;https=...;socks=...`) and `ProxyOverride` under
`HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings` — and then
notifies WinINET with `InternetSetOption(INTERNET_OPTION_SETTINGS_CHANGED)`
followed by `INTERNET_OPTION_REFRESH`. Without the notification a running
browser keeps the old configuration, which is the usual reason a registry-only
implementation "does not work". `off` clears only `ProxyEnable`, leaving
whatever was in `ProxyServer` behind the switch, exactly as the Linux side
leaves the endpoints behind `mode: none`.

The registry is reached through `advapi32.dll` loaded with
`syscall.NewLazyDLL` (System32 only), and the WinINET DLL the same way — no new
dependency, matching `internal/core`'s use of `LockFileEx`. WinHTTP has its own
configuration (`netsh winhttp import proxy source=ie`) which a browser does not
use, so vender does not touch it. The ordering, the value rendering and the
no-partial-write guarantee live in `cmd/vender/winproxy.go`, which carries no
build tag and is unit-tested with a fake registry on any platform.

## 7. Routing rules (`internal/rules`) — implemented in M2

`internal/rules` owns the default policy, the DNS policy and the compiled rule
sets those policies match against. Nothing about sing-box leaks upward: the
package speaks in vender's own terms and `core/render.go` is the only place that
knows the JSON schema.

```go
func BuiltinRules() []Rule         // destination rules, in evaluation order
func CriticalRules() []Rule        // the "must survive a dead proxy" list
func CriticalDirectSuffixes() []string
func EffectiveRules(u *UserRules) ([]Rule, error) // critical + user + builtin
func BuiltinActions() []Action     // sniff, hijack-dns
func BuiltinDNS() DNS              // split DNS for rule mode (no user rules)
func RuleModeDNS(u *UserRules) DNS // split DNS for rule mode, merged
func GlobalDNS() DNS               // proxied-only DNS for global mode

// User rules (rules.yaml)
func ParseUserRules(data []byte) (*UserRules, error)
func LoadUserRules(path string) (*UserRules, error) // missing file => (nil, nil)
func WriteUserRules(path string, force bool) (bool, error)
func (u *UserRules) Validate() error
func (u *UserRules) Rules() ([]Rule, error)
func (u *UserRules) DirectSuffixes() (domains, suffixes []string)

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

Order is the policy, because the first matching rule wins. Since M5 there are
three layers, in this order:

1. the built-in **critical list** (`opencode.ai`) — always
   first, so no rule, built-in or user, can route a control-plane domain through
   a dead proxy;
2. the user's optional `rules.yaml`, merged *before* the built-in policy;
3. the built-in policy.

| # | Rule | Matches | Goes to |
|---|---|---|---|
| 1 | `critical-direct` | `domain_suffix` opencode.ai | `direct` |
| 2 | `user-direct` | `rules.yaml` `direct:` (only if non-empty) | `direct` |
| 3 | `user-proxy` | `rules.yaml` `proxy:` | `proxy` |
| 4 | `user-block` | `rules.yaml` `block:` | `block` |
| 5 | `private-dest-direct` | `ip_is_private` | `direct` |
| 6 | `ads-block` | `geosite-category-ads-all` | `block` |
| 7 | `service-overrides-proxy` | steam, onedrive, microsoft, google, youtube, telegram, netflix, openai, bahamut | `proxy` |
| 8 | `cn-direct` | `geosite-cn`, `geoip-cn` | `direct` |
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

#### The critical list

`criticalDirectSuffixes` is deliberately tiny, and an entry has to satisfy **two**
tests, not one:

1. losing it would make the machine *unmanageable* (not merely inconvenient)
   while the proxy is unreachable; **and**
2. it is reliably reachable *directly* on the user's network.

`opencode.ai` — the coding agent's model/API plane — satisfies both: cutting it
off would strand an agent that cannot fix the very problem it is diagnosing, and
the direct path to it works. `api.tavily.com` used to be listed and fails (2):
forcing it direct times out on this network while the proxy reaches it fine, so
the entry *broke* the API instead of protecting it. It was removed and now
follows the normal policy (`built-in policy → final: proxy`), where a user can
still override it from `rules.yaml`. A domain that fails (2) must never be in the
unoverrideable list. The list is evaluated before everything, and the same
domains are added to the DNS policy so they are *resolved* directly too (see
§7.3).

#### The user rules file (`rules.yaml`)

```yaml
direct:
  - internal.corp        # internal.corp and every subdomain (domain_suffix)
  - =exact.example       # exactly exact.example, no subdomains
  - 10.0.0.0/8           # an IP range (ip_cidr)
proxy:
  - blocked.example
block:
  - ads.example
```

`internal/rules/userrules.go` parses it. An entry is one of: a bare domain (a
suffix rule, covering subdomains), `.suffix`, `=exact`, a CIDR, or an explicit
`domain:` / `suffix:` / `cidr:` prefix. The three lists are compiled in the order
`direct`, `proxy`, `block`, so a domain that appears in two lists resolves by
order (direct wins). The whole file is validated up front: a typo is reported
instead of silently ignored, and the daemon refuses to start rather than run with
a half-understood policy.

The file is optional and never created implicitly. `vender rules init` writes a
commented template; `vender rules path` prints where it would live; `vender rules
list` prints the merged policy (critical + user + built-in) in evaluation order.

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

### 7.3 Split DNS, and why it can no longer hang

**The failure this design fixes.** The M2 policy resolved every non-Chinese name
through a Cloudflare DoH resolver reached *through the proxy* (`detour: proxy`).
That makes name resolution depend on the proxy being healthy. When the link to
the VPS degraded, DNS for everything non-CN stalled — a domain that should have
gone direct could not even be *resolved*, because its query was queued behind a
dead proxy. The machine became unusable, including `www.baidu.com` and the
agent's own model API.

The M5 rule is: **a DNS query must never depend on the proxy.** Two mechanisms
enforce it:

- **Real, direct resolution for what will go direct.** The critical list, the
  user's direct list and `geosite-cn` are answered by `dns-cn`
  (`223.5.5.5`, UDP, reached directly). These names get correct, immediately
  usable addresses.
- **FakeIP for what will be proxied.** Everything else, for address queries
  (`A`/`AAAA`), is answered by a local `fakeip` resolver: a synthetic address
  from `198.18.0.0/15`, handed out in microseconds with **no upstream query at
  all**. The engine maps the fake address back to the domain before dialling, so
  the proxy still receives the real name and the server resolves it where the
  domain actually lives. This also means a poisoned domestic answer can never be
  used for a proxied connection.

| Queries for | Resolver | Reached | Result |
|---|---|---|---|
| critical list | `dns-cn` | directly | real address |
| user `direct:` domains | `dns-cn` | directly | real address |
| `geosite-cn` | `dns-cn` | directly | real address |
| everything else, `A`/`AAAA` | `dns-fakeip` | local | fake address, domain preserved |
| everything else, other types | `dns-cn` (`final`) | directly | real address |

`final: dns-cn` and `strategy: prefer_ipv4`. The last row is the one deliberate
trade-off: a non-address query (HTTPS/SVCB, TXT, MX) for a foreign name is
answered by the domestic resolver, so the *name* is visible to it. That is a
minor metadata exposure, accepted because the alternative — a foreign resolver
reached through the proxy — is exactly the failure being fixed. Address queries,
which are what connections actually use, never leave the machine.

`route.default_domain_resolver` still points at `dns-cn`, and that is
load-bearing: it is what a `direct` outbound uses to resolve a name, so a direct
dial gets a real address and **never** a fake one. A proxied outbound never
resolves locally at all — it hands the name to the server.

Two sing-box details were established against the pinned engine, not guessed: the
domestic resolver must **not** name a `detour` (1.14 rejects a detour to the
empty `direct` outbound), and `route.default_domain_resolver` is *required* in
1.14 (its absence is a hard error unless an env var suppresses the check). Both
were caught by `sing-box check`, and the whole policy was then exercised live
through the engine's own Clash API `/dns/query` endpoint with a deliberately
dead proxy: `www.baidu.com` and `opencode.ai` returned real addresses in <0.1 s,
`google.com` returned a fake address in <0.1 s, while the *old* configuration
took ~5 s and failed on the same two foreign names.

The DNS rules mirror the routing split by construction: `RuleModeDNS(user)`
builds the critical and user-direct rules from the same lists the routing policy
uses, so the two cannot drift. A user `direct:` CIDR is not mirrored into DNS —
sing-box 1.14 cannot match a CIDR in a DNS rule without response matching — and
does not need to be: a direct outbound dialling a *name* re-resolves it through
`dns-cn` anyway, and a connection that already arrives as an IP in the CIDR never
needs DNS.

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

### 7.5 Node failover (`selected_server: auto`)

`selected_server: auto` makes `core.buildAutoOutbounds` render **the active
profile's servers** as their own outbounds (tagged by server name), plus a
`urltest` group **tagged `proxy`**. The pool is decided upstream, by
`Config.SelectionInProfile(servers, activeProfile)` (§4.5), so the renderer's
contract is unchanged: it renders whatever `RuntimeConfig.Servers` holds.
Because the group reuses the `proxy` tag, nothing else changes: every rule that
says "proxy", the `route.final`, and (in global mode) the DNS `detour` all keep
referring to `proxy`, which is now the group.

```
outbounds: [ UpCloud-SJ, UpCloud-SJ-Hy2, {urltest proxy -> [UpCloud-SJ, UpCloud-SJ-Hy2]}, direct, block ]
```

The knobs are `url: http://www.gstatic.com/generate_204` (the same probe as the
latency test, reached *through* the member so a node that connects but cannot
carry traffic is judged unhealthy), plus `interval` and `tolerance` taken from
`engine.failover_interval` (default `30s`, range `5s`–`10m`) and
`engine.failover_tolerance_ms` (default `100` ms, `0` = engine default, range
`0`–`5000`). The interval is a genuine trade-off because each probe is a real
HTTP request sent *through* its member: shorter reacts faster to a node going bad
(the previous hardcoded `1m` could leave a bad node carrying traffic for up to a
minute) but runs more probe traffic and can load a fragile link. The tolerance is
above sing-box's 50 ms default so two nodes of similar latency do not flap.

```yaml
engine:
  failover_interval: 30s
  failover_tolerance_ms: 100
```

A malformed or out-of-range knob is rejected by `config.EngineConfig.Validate`
before the engine is spawned, so a typo fails loudly instead of silently
changing failover behaviour.

Single-server behaviour is unchanged in shape: with a server *name*, the config
still has a single `proxy` outbound (the golden files pin it), and the daemon
passes the one resolved server in `RuntimeConfig.Server`. Auto mode passes the
whole list in `RuntimeConfig.Servers` and leaves `Server` nil; `Config.Selection`
decides which of the two a mode needs, so `core` never has to know about the
sentinel.

A server name that collides with a reserved tag (`proxy`, `direct`, `block`,
`mixed-in`, `tun-in`) is prefixed `server-` so it cannot shadow vender's own
outbounds. `ServerOutboundTag` is the one function that computes this, used by
the renderer and by the controller's failover reporting.

### 7.6 Deliberate simplifications

- **Failover is the engine's, not vender's.** vender renders a `urltest` group
  and then only *reports* what the engine chose (via the Clash API, §8.3); it
  does not implement its own health checks or switch nodes itself. That keeps one
  source of truth for "which node is active".
- **No `mode: global` rules.** `global` emits no routing rules at all, and
  `direct` emits no proxy outbound and no `dns` section. Global mode with `auto`
  is allowed; the DNS resolver is then reached through the group.
- **Category granularity.** `geosite-microsoft` covers GitHub too, so GitHub is
  proxied; that is the upstream list's composition, and it is the behaviour most
  users want from a China-facing client anyway. `geosite-cn` is one category
  rather than a hand-tuned set of exceptions.
- **User rules add no rule sets.** A user entry is a domain/suffix/CIDR, so the
  embedded set list (and therefore the extraction cost) is unchanged by
  `rules.yaml`.

### 7.7 Resolving a node's own hostname (M8)

The client-facing split DNS above answers *the user's applications'* queries.
The engine dialling an outbound is a different path: it must resolve the node's
`server` address itself, and for a provider's `entry` domains that must be the
provider's resolver (§4.6). In sing-box 1.14 the lever is the outbound's
**`domain_resolver`**, a dial field that names a DNS server tag; the route's
`default_domain_resolver` is the fallback for everything that names none.

`core.buildProfileDNSPlan` turns the active policies into that:

```
dns.servers:  … , {tag: dns-system, type: local},
              {tag: profile-dns-1, type: tcp, server: <provider host>,
               server_port: 8080, domain_resolver: {server: dns-system}}
outbounds:    {type: shadowsocks, …, server: <entry host>,
               domain_resolver: {server: profile-dns-1}}
```

Five points are load-bearing:

- **Per node, not per profile.** For each rendered server the plan looks up the
  policy of *the profile that server belongs to* and matches its hostname
  against the namespace policy (longest suffix wins). A node under a domain the
  policy does not cover gets no `domain_resolver` and keeps the built-in
  resolver — which is why this subscription's VMess nodes are unaffected.
- **Tags are stable and ordered** (`profile-dns-1`, … by first use), so the
  rendered document is deterministic and golden-testable.
- **A name-based resolver needs a bootstrap.** The provider's resolver is itself
  reached by name (`…fyi`), so it needs a `domain_resolver` of its own. vender
  adds one `local`-type server, `dns-system`, which uses the host's own resolver
  with no query that could be captured by the engine's own tunnel or depend on
  the policy being bootstrapped. An IP-literal resolver does not point at it.
- **The resolvers are reached directly.** No `detour` is emitted, so a query
  never leaves through the proxy; and when `engine.bind_interface` is set the
  resolver's dial is pinned to the same interface as the outbound, because on a
  multi-homed (or TUN) host it is part of the engine's own dial path.
- **No policy is byte-identical to M7.** The plan is empty unless a rendered
  node matches a policy, and the only fields it can add are the ones above; a
  golden test asserts that an absent, an empty, and an unrelated policy all
  render exactly like M7.

FakeIP is untouched and unrelated: it answers *client* address queries for
proxied domains and never leaves the machine, while the node's own hostname is
resolved at dial time by the provider's resolver. `global` mode gains a
`default_domain_resolver` naming `dns-system` when a policy applies, because the
extra resolvers otherwise leave the `direct` outbound's dial field without one —
a hard error in sing-box 1.14.

`sing-box check` (skipped without an engine binary, like §6.5) validates the
`domain_resolver`, `local`, `tcp`-with-`server_port` and `bind_interface` shapes
against the pinned binary in rule and global mode, single and auto.

## 8. Control channel (`internal/control`) — implemented in M3

The daemon's local API. Everything the TUI, and anything else that wants to
drive vender, does to a running daemon goes through here.

```go
// Daemon side: the controller owns the engine, the server side serves the API.
ctrl, _ := control.NewController(control.ControllerOptions{Store: store, RunDir: runDir, Engine: engine, ...})
srv, _  := control.NewServer(control.ServerOptions{Handler: ctrl, RunDir: runDir})
go srv.Serve()

// Client side.
cli, _ := control.Dial(ctx, runDir)     // reads <runDir>/control.json
st,  _ := cli.Status(ctx)
logs, _ := cli.FollowLogs(ctx)          // a subscription owns its connection
```

### 8.1 Transport, and why not a unix socket

**TCP on 127.0.0.1 with an OS-assigned port and a random 128-bit token.** The
daemon writes `{v, pid, addr, token, started, runDir}` to `<run_dir>/control.json`
with mode `0600`, and removes the file on exit so a stale token cannot outlive
its daemon.

A unix domain socket on Linux and a named pipe on Windows would be the more
idiomatic choice, and the alternative was considered seriously. It was rejected
because it is two implementations behind build tags whose *security* properties
differ: a unix socket is protected by the file mode of the socket inode, while
a Windows named pipe is protected by its DACL and by who may open it at all —
which means the "is this channel private?" argument has to be made twice, in
two idioms, and tested twice. Loopback TCP needs neither: the kernel refuses
any off-host connection to `127.0.0.1`, and every request must present the
token, which lives in a `0600` file inside a `0700` directory. The same code,
the same tests, the same guarantees on both platforms — which is exactly what
M4's Windows packaging needs.

The token is regenerated on every daemon start and never written to a log or an
error message.

### 8.2 Protocol

Newline-delimited JSON, one request per line, exactly one response per request.
Every message carries `{"v":1,...}`; a client speaking another version is
rejected with an explicit error rather than misparsed.

```
-> {"v":1,"auth":"<token>","id":7,"cmd":"select","args":{"name":"LA"}}
<- {"v":1,"id":7,"ok":true,"data":{"action":"select","status":{...}}}
<- {"v":1,"id":7,"ok":false,"kind":"not_found","error":"control: server not found: ..."}
```

Responses carry a machine-readable `kind` (`auth`, `bad_request`, `not_found`,
`busy`, `internal`) so a client can react without matching message text.

A **subscription** is a request whose response acknowledges the stream, after
which the connection belongs to it:

```
-> {"v":1,"auth":"<token>","cmd":"traffic"}
<- {"v":1,"ok":true,"stream":"traffic"}
<- {"v":1,"stream":"traffic","data":{"up":1024,"down":65536,"totalDown":12407}}
<- {"v":1,"stream":"traffic","ka":true}          # heartbeat, skipped by clients
```

| Command | Args | Reply |
|---|---|---|
| `ping` | — | daemon identity (cheapest liveness probe) |
| `status` | — | engine state, mode, selection, uptime, pids, Clash endpoint |
| `servers` | — | the server list, with the active one flagged |
| `select` | `{name}` | persists the choice, restarts the engine |
| `mode` | `{mode}` | persists `rule`/`global`/`direct`, restarts the engine |
| `profile` | `{name}` | M7: persists the active profile in `profiles.yaml`, restarts the engine (with `auto`, that changes the group's members) |
| `tun` | `{enabled}` | persists `engine.tun.enabled`, restarts the engine; **refuses before touching the engine** when this daemon cannot create a TUN device |
| `latency` | `{name?}` | round trip in ms (through the tunnel for the active server) |
| `start` / `stop` / `restart` | — | engine lifecycle inside the running daemon |
| `rules` | — | the active policy: actions, ordered rules, DNS, rule sets |
| `proxies` | — | M5: the failover group's state (auto? current member) and per-server health |
| `logs` (stream) | — | engine output, with the daemon's lifecycle notes |
| `traffic` (stream) | — | one sample per second from the engine's Clash API |

### 8.3 The controller, and where the numbers come from

`Controller` is the daemon's brain: it owns the engine, re-renders
`RuntimeConfig` from the files on disk on every restart (so a `vender import`
in another terminal is picked up by the next restart), and fans engine output
out to every subscriber.

Three decisions in it are worth recording:

- **Reads and mutations are separate locks.** A server switch stops and starts
  sing-box inside one call, which takes seconds. That call holds only the
  operation mutex, so `status` on another connection is answered from a
  snapshot, immediately — the daemon stays responsive while the engine blinks.
  A test asserts it (`TestServerStaysResponsiveDuringARestart`).
- **Subscribers are dropped from, never blocked on.** Each subscription is a
  buffered channel; a client that stops reading loses samples instead of
  wedging the daemon. This is the same policy `core` applies to the child's
  stdout. A new subscriber is first handed the last 250 log lines, so attaching
  the TUI an hour into a session still shows history.
- **Traffic and latency come from sing-box's own counters.** Local counters on
  the mixed inbound cannot see the proxied payload, so M3 turns on sing-box's
  experimental **Clash API** (`127.0.0.1`, an OS-assigned port decided by the
  daemon, a generated secret) and reads `/traffic`, `/connections` and
  `/proxies/<tag>/delay` through `core.ClashClient`. The endpoint is only
  reachable from the daemon; the TUI never talks to it.
  Latency is measured **through the tunnel** for the active server, which is
  what a user switching servers actually wants to know; for any other server
  (vender has a single outbound) it falls back to a bare TCP handshake and says
  so — `(tcp)` — because on a host where a TUN hijacks outbound TCP that number
  can be optimistic to the point of being meaningless.
- **Failover state is the engine's, read back through the Clash API (M5).**
  `/proxies` reports, for a `urltest` group, which member is `now` active and,
  for each member, its last measured delay (`history`). `Controller.Proxies`
  joins that with `servers.yaml` and answers `CmdProxies` with one row per
  server: `active`, `alive`, `delayMs`. In auto mode a named member can also be
  latency-probed *through the tunnel* with `/proxies/<member-tag>/delay`, so the
  Nodes screen can measure a specific node rather than only the group.

Streams reconnect on their own: if the engine restarts, the traffic poller
backs off and reconnects with no involvement from any client.

**TUN is refused before the engine is touched.** `Controller.SetTun` runs the
privilege check *before* it persists anything or restarts anything, because the
alternative — stop a working proxy, then fail to start the TUN — leaves the user
with neither. A client that is refused (a `bad_request`, not an internal error)
can therefore report "nothing was changed; the engine keeps running" and be
right. Every `status` reply carries the daemon's **own** privilege verdict
(`tun.privilegesOk` / `tun.privilegeDetail`), so a client does not have to
guess: the TUI's warning uses the daemon's answer when attached and the local
machine's when it is not, and `vender tun status` prints both the setting and the
verdict.

## 9. TUI (`internal/tui`) — implemented in M3, TUN toggle in M6

The TUI is a control-channel client, entered through a single function:

```go
// Run starts the interactive TUI and blocks until the user quits or ctx is
// cancelled. It never requires a daemon to be up.
func Run(ctx context.Context, opts Options) error

type Options struct {
    ConfigDir       string // config.yaml / servers.yaml (read directly)
    RunDir          string // where the daemon published control.json
    ControlSocket   string // optional explicit "host:port" override
    RefreshInterval int    // status poll, in seconds (0 = 1s)
}
```

### 9.1 Screens

| Key | Screen | Contents |
|---|---|---|
| `1` | **Dashboard** | daemon and engine state, selected server, active profile, mode, uptime, latency badge, and an animated up/down traffic graph with a load gauge, human-readable rates and session totals |
| `2` | **Nodes** | table of configured servers (profile, name, type, protocol, endpoint, latency); `enter` selects and lets the daemon restart the engine, `A` switches to auto failover, `t` measures latency, `r` reloads from disk. In **auto** mode the header shows the active profile and how many servers the group scopes to, plus which member is carrying traffic, and the latency column is fed by the engine's own URL-test results, so node health is visible without probing each one by hand. |
| `3` | **Logs** | the engine's own output as streamed by the daemon, in a scrolling viewport with follow mode (`f`) and a substring filter (`/`) |
| `4` | **Settings** | the routing mode (rule/global/direct), the **profiles** — every known profile with its node count and (redacted) subscription URL, and `p` to switch the active one — the inbounds — the mixed listener and the **TUN inbound** with its device, its knobs, the privilege verdict and an explicit warning when it is on but cannot work (`u` toggles it) — the engine paths, the **merged routing policy** rendered from `internal/rules` (critical + `rules.yaml` + built-in, with the selected server and whether failover is active), and the **start-at-login registration** with `a` to toggle it |

Global keys: `tab`/`shift+tab` (next/previous screen), `?` (help overlay), `q`
(or Ctrl-C) to quit. On the Dashboard: `t` latency, `s`/`x`/`R` start/stop/
restart the engine, `m` cycle the mode, `r` refresh. The engine controls exist
so the daemon does not have to be restarted to stop and start the proxy.

### 9.2 Behaviour with no daemon

The TUI starts anyway and is useful: Nodes and Settings are built from
`config.yaml`, `servers.yaml` and `internal/rules`, so they are complete with
nothing running. The Dashboard says what is missing and prints the exact
command that fixes it (`vender daemon`), and attaching is retried once a
second — start the daemon later and the TUI simply picks it up. Every action
that needs the daemon explains itself in a toast instead of failing silently.

### 9.3 Animation, colour and terminals

State is a value (`Model`); `View` is pure. That is what makes the view tests
possible: a test builds a model by hand and compares `View()` against a golden
file, with no terminal and no daemon.

- Motion is driven by two ticks: a data tick (status poll, 1s) and an animation
  tick (8/s) for the spinner and for the tab highlight, which slides from the
  old screen to the new one over a few frames. The traffic graph is a sparkline
  scaled to the window peak, with a floor so an idle connection is a flat trace
  rather than a full-height bar.
- Colour lives in one `theme` built from a `lipgloss.Renderer`. A renderer over
  `io.Discard` has an ASCII profile whose styles are no-ops, so the same `View`
  code produces plain text — that is the `NO_COLOR` path, the non-TTY path, and
  the golden-test path, all at once. Box drawing is deliberately *not*
  conditional on colour.
- With no terminal at all, `Run` prints one status snapshot and exits 0 instead
  of failing or clearing a terminal it cannot restore.
- Bubble Tea owns the terminal: it enters the alternate screen, and exits it,
  shows the cursor again, and restores the line discipline on `q`, Ctrl-C and
  SIGTERM. The CLI deliberately does **not** install its own signal handler for
  `vender tui`: cancelling the context on the same signal Bubble Tea handles
  races with Bubble Tea's own signal goroutine and can leave the process stuck
  on the way out. One owner of the signal, and it is the component holding the
  terminal. `Run` still honours a cancelled context, which is what embedding
  and tests use.

### 9.4 Dependencies

Three TUI libraries, and nothing else: `bubbletea`, `lipgloss`, `bubbles`
(spinner, viewport, table, progress, help). The control channel adds no
dependency at all — it is `net`, `encoding/json`, `crypto/rand` and
`crypto/subtle`. Everything stays cross-compilable to `windows/amd64` with no
build tags in either package.

## 10. Autostart (`internal/autostart`) — implemented in M4, TUN mechanism in M6

```go
func Enable() error              // idempotent
func Disable() error             // idempotent
func Status() (State, error)     // never fails merely because nothing is registered

type State struct {
    Enabled   bool      // is vender registered to start at login?
    Mechanism Mechanism // systemd-user | xdg-autostart | windows-task | windows-run-key
    Target    string    // the concrete artifact: unit path, desktop file, task name, registry value
    Detail    string    // why this mechanism, whether lingering is on, what could not be arranged
}
```

The whole entry is a one-line problem: **run `vender daemon` at login**. The
daemon is headless, so nothing here needs a console or a TTY; it takes a
per-run-dir lock, so a login that races a manual start is harmless; it exits on
`SIGTERM`, which is what `systemctl --user stop` sends; and its control channel
is loopback TCP, so a client started in a later session still reaches it. `State`
carries the concrete artifact rather than a boolean, so `vender autostart status`
(and the TUI Settings panel) can say exactly what is installed.

### 10.1 Linux

| | |
|---|---|
| Primary | `~/.config/systemd/user/vender.service` (`$XDG_CONFIG_HOME` honoured), `ExecStart=<abs vender> daemon`, `Restart=on-failure`, `RestartSec=3`, `WantedBy=default.target`; then `systemctl --user daemon-reload` and `enable --now` |
| Fallback | `$XDG_CONFIG_HOME/autostart/vender.desktop` when no systemd *user manager* answers |
| Boot without login | `loginctl enable-linger <user>`, best-effort; `Status.Detail` reports whether lingering is on |
| Removal | `systemctl --user disable --now`, then the unit file; the desktop entry too, so a machine that switched mechanisms cannot end up with both |

systemd availability is *probed*, not assumed: `systemctl` must exist and
`systemctl --user is-system-running` must answer. A manager that is up but
`degraded` still accepts units, so it counts as available; a bus-connection
failure does not, and the XDG entry is used instead. The XDG entry assumes a
desktop session that reads `$XDG_CONFIG_HOME/autostart` (GNOME, KDE, XFCE all
do); it cannot start anything without a graphical login, which is why it is the
fallback rather than the primary.

Lingering is what turns "at login" into "at boot". Without it the user manager
is torn down when the last session ends. It needs polkit, so `enable` treats a
refusal as a note in `Detail` rather than a failure — the unit is still correct.

### 10.2 Windows

| | |
|---|---|
| Primary | a Task Scheduler **logon task** at `\vender\daemon`, registered from an XML definition with `schtasks /Create /XML`: `LogonTrigger`, `InteractiveToken`, `RunLevel=LeastPrivilege`, `Hidden=true`, no execution time limit |
| Fallback | the per-user Registry Run key `HKCU\...\CurrentVersion\Run`, value `vender` = `"<abs vender.exe>" daemon` |
| Removal | both: `schtasks /Delete`, `reg delete`; a "not found" result is success |

Neither path needs administrator rights. The task is preferred because it can be
registered hidden and with the least privilege, and because a scheduled task is
not silently disabled by the per-application "startup apps" toggle the way a Run
value can be. The task definition is *staged as a UTF-16LE file* because
`schtasks /Create /XML` requires that encoding.

**Limitations, stated plainly.** None of this can be executed on the Linux host
vender is developed on: it is compile-checked with `GOOS=windows go build ./...`
and every decision that can be made outside Windows is unit-tested (the XML, the
argument vectors, UTF-16 encoding, and the parsing of `reg query` /
`schtasks /Query` output, including the UTF-16LE that both tools write to a
pipe). Furthermore, Task Scheduler's `Hidden` setting hides the task from the
library view and suppresses the default window in the common case, but it does
not guarantee that a **console-subsystem** executable shows no console at all; a
fully windowless start needs a GUI-subsystem build or a `conhost --headless`
launcher. That is deliberately left to M7 (signed Windows binaries) rather than
half-done here. The daemon itself works with or without a console.

The XML/argument construction and the output parsing are in `windows.go`, which
**carries no build tag** — it is pure string work plus an injectable runner — so
those tests run on Linux too; `autostart_windows.go` is a three-line wrapper over
the real `schtasks`/`reg`.

### 10.3 The TUN mechanism (M6)

The default registrations are per-user, and a per-user mechanism cannot be given
a kernel privilege: a systemd *user* manager is unprivileged (it has nothing to
hand out), and a Run key value cannot run a process elevated. With
`engine.tun.enabled: true`, `EnableWithOptions(Options{TUN: true})` therefore
switches mechanisms:

| Platform | With TUN on | Cost |
|---|---|---|
| Linux | `/etc/systemd/system/vender.service`: `User=<invoking user>`, `Environment=HOME=<their home>`, `AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW`, `CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW`, `WantedBy=multi-user.target` | one `sudo vender autostart on`. The combination `User=` + `AmbientCapabilities=` is the one that works: systemd sets the ambient set as root and ambient capabilities survive the setuid to the target user. The daemon runs as the user, not as root, and the bounding set keeps every other capability out of reach |
| Windows | the same logon task, `RunLevel=HighestAvailable` | one `vender autostart on` from an elevated prompt |

Three decisions in it:

- **The privileged path never silently degrades.** If the system unit cannot be
  installed, `on` still makes the per-user registration (the daemon *does* start
  at login), then returns an error naming what TUN still needs. Installing a
  user unit and reporting success would be a lie the user only discovers weeks
  later, at the next login.
- **The attribute is the truth, not the mechanism.** `Status` reports which
  mechanism is registered *and* whether that mechanism can create a TUN, quoting
  the caller's verdict (which comes from `core.CheckTUNPrivilege`, so it also
  recognises a `setcap`-granted engine binary). On Windows it reads
  `<RunLevel>` back out of the queried task definition rather than assuming.
- **`Options`, not a dependency.** autostart does not import `internal/config`:
  the caller reads `config.yaml`, asks `core` for the privilege verdict, and
  passes two fields. The package stays standalone — it reads `$HOME`, runs
  `systemctl`/`loginctl`/`schtasks`/`reg` and nothing else — which is what keeps
  its tests drivable with an isolated `HOME` and a stub runner.

`Enable()`/`Disable()`/`Status()` keep their signatures and mean
`...WithOptions(Options{})`, so the M4 API and its tests are unchanged.

### 10.4 What the CLI and the TUI do

`vender autostart on|off|status` is a thin shell over the three entry points;
`status` exits non-zero when nothing is registered (the same contract as
`vender status`) and `--json` prints the `State`. The TUI Settings screen reads
the same `Status` and toggles with `a`; because the registration belongs to the
user and not to the daemon, the TUI calls it **directly** rather than over the
control channel — which is what lets a user enable start-at-login while no
daemon is running, the situation in which the idea usually occurs. The call goes
through a `tea.Cmd`, so neither the file I/O nor `systemctl` can block the UI,
and `View` stays a pure function of the model.

Both implementations are driven in tests through injected seams (an isolated
`HOME`/`XDG_CONFIG_HOME` and a stub `systemctl`/`loginctl` on Linux; a fake
runner on Windows), so `go test` cannot register a real unit or rewrite a real
registry.

## 10.5 Subscriptions and profiles (M7)

`internal/config/profiles.go` owns the profile model and the merge rules, and
`internal/subscription` owns the wire formats. Three decisions are worth
recording:

- **The subscription body is data, not a file.** `Parse` returns a `Result` of
  usable servers plus one `Skipped{Node, Reason}` per node it could not use, so
  the CLI can print "added 4, skipped 1: ss needs a SIP003 plugin" instead of
  failing the whole import. Selecting a nice-looking subset (the first 40 nodes)
  is explicitly *not* done: the engine's URL test is a better judge of which node
  is good, and a subscription that changes does so under the user's feet.
- **`Fetch` is the only networked function, and the only place the user agent
  matters.** Panels branch on it and serve an HTML or Clash page to an unknown
  agent; `v2rayN/6.0` gets links. The response is bounded (`MaxBody`) and honours
  the caller's context, and the URL is redacted before it reaches a terminal or a
  log (`redactURL`), because a subscription URL is a bearer token in practice.
- **The TUI switches profiles through the daemon, not by writing the file.** With
  `auto` the active profile changes the engine's outbound set, so a client that
  edited `profiles.yaml` behind a running daemon would show a state the engine
  does not have. `CmdProfile` is therefore a control command (§8.2), exactly like
  `CmdTun`; the *list* of profiles is read from the local snapshot, which is what
  keeps the panel useful with no daemon.

The compatibility guarantee is stated once and tested: **`profile` is optional,
absent means `manual`, and `manual` is the default active profile, so an existing
`servers.yaml` + `config.yaml` pair keeps rendering exactly what it rendered
before M7** — a URLTest group over every server.

## 11. Release packaging (M4)

`scripts/package.sh` produces two archives in `dist/release/`:

```
vender-<version>-linux-amd64.tar.gz
vender-<version>-windows-amd64.zip
```

Each contains a top-level `vender-<version>-<target>/` directory with the
`vender` binary, the matching **sing-box** binary, `README.md`, `LICENSE`,
`NOTICE` and `LICENSE.sing-box`. `scripts/build.sh` still builds the two
binaries into `dist/`; `package.sh` reuses it, fetches the pinned sing-box for a
target that is missing (it is 80 MB and git-ignored, so a clean clone has to get
it from somewhere), and stages the archives with sorted entries, a fixed mtime
and no owner metadata so two runs over the same inputs are byte-identical.

**Licensing is not an afterthought here.** vender is GPL-3.0-or-later and bundles
sing-box, which is also GPL-3.0-or-later with one extra upstream term. The
archives therefore carry vender's `LICENSE`, sing-box's own licence text as
`LICENSE.sing-box`, and a `NOTICE` that states the bundling, the upstream
copyright, the additional term, the pinned version, and where the corresponding
source lives. Nothing is silently dropped.

`scripts/install.sh` is the per-user installer: it copies `vender` and
`sing-box` into `$PREFIX/bin` (default `~/.local/bin`) — side by side, which is
one of the three places `core.ResolveBinary` looks — optionally runs `vender
autostart on`, and can uninstall both (removing the autostart entry first). It
needs no root and writes nothing outside the prefix.

## 12. Milestone plan

| Milestone | Deliverable | Acceptance |
|---|---|---|
| **M0** | Skeleton, `internal/config` (config, servers, share links), CLI (`version`, `import`, `servers`), docs, unit tests, cross-compilation | `go build ./...`, `GOOS=windows go build ./...`, `go test ./...`, `go vet ./...` all clean; stubs print their milestone |
| **M1** | `internal/core` engine + `vender daemon`, `status`, `proxy on\|off` | With a real server: daemon starts sing-box, `127.0.0.1:7897` proxies HTTP and SOCKS5; stopping the daemon frees the port; `status` reflects reality |
| **M2** | `internal/rules` built-in policy, actions, split DNS and embedded rule sets, wired into the generated config; `vender rules list` | `proxy.mode: rule` sends CN/private destinations direct, blocks ads, proxies a documented override list and everything else, with matching split DNS; `sing-box check` passes for both server types and a live run shows direct vs proxied vs blocked; `global` and `direct` behave as documented |
| **M3** | `internal/control` + `internal/tui`: the daemon control channel and the Bubble Tea client | TUI attaches to a running daemon, switches the active server and mode, tails engine logs, shows live traffic from the engine's own counters, quits cleanly and restores the terminal; behaves with no daemon; the channel rejects an unauthenticated client, and a bad host leaves no side effects |
| **M4** | `internal/autostart` (systemd user unit / XDG entry, Task Scheduler task / Run key), Windows system proxy, release packaging and the installer | `autostart on` starts the daemon after login on both platforms; `off` removes it; `status` reports it; all idempotent. `vender proxy on\|off` works on Windows. Both release archives build and contain the engine plus the licence documents |
| **M5** | **Robustness**: a critical "must survive a dead proxy" list, an optional per-user `rules.yaml` merged ahead of the built-in policy, a proxy-independent split DNS (FakeIP for proxied names, direct resolution for direct ones), and `selected_server: auto` node failover with the active member shown in the TUI | With a deliberately unreachable proxy, DNS still resolves in well under a second and direct traffic (CN + the critical list) keeps working while foreign requests fail fast; `sing-box check` accepts rule/auto/direct configs; `rules list` shows the merged policy; auto mode serves traffic through a reachable node while another is dead |
| **M6** | **Optional TUN mode**: a `tun` inbound in the engine, `vender tun on\|off\|status`, a TUI toggle that reports the privilege verdict, and a TUN-aware autostart mechanism on both platforms | Config-level: golden files for TUN on/off in rule/global/direct plus `sing-box check` for each. Live: a TUN test in a network namespace where one is available, otherwise a documented statement of what could not be run. `vender tun on` against a running daemon either applies (persist + restart with the TUN inbound) or is refused before the engine is touched, with an actionable message. Autostart reflects the TUN-correct mechanism and `status` says whether it can work |
| M7 | **Subscription import, named profiles and three more protocols** (Plan B: hardening and release) | Share links for Shadowsocks (both spellings and the 2022 ciphers), VMess (TCP/WS/gRPC ± TLS) and Trojan, rendered, validated and round-tripped; `import --url` accepts base64, plain and Clash subscriptions with a per-node summary; `vender profiles list\|use\|update\|rm` with `profiles.yaml` persisted; `auto` scopes to the active profile, a pre-M7 `servers.yaml` still loads and still fails over over every server; `sing-box check` accepts every new outbound type and a profile-scoped group |

## 13. Engineering conventions

- **Minimal dependencies.** vender uses only `cobra`, `yaml.v3` and — from M3,
  for the TUI alone — `bubbletea`, `lipgloss` and `bubbles`. There is no
  networking library (that is sing-box's job) and subscription fetching added none
  either: it is `net/http`, `encoding/base64` and `encoding/json`. The control
  channel is `net` + `encoding/json` + `crypto/rand`/`crypto/subtle`.
  Windows-specific calls that the standard library does not expose
  (`LockFileEx`) go through `syscall.NewLazyDLL`, which loads from `System32`
  only.
- **No OS-specific code.** Everything builds for `linux/amd64` and
  `windows/amd64`; platform differences are handled through standard-library
  abstractions (`os.UserConfigDir`, `os.UserCacheDir`). Where `syscall`-level
  work is genuinely needed, it is split behind build tags inside the relevant
  package — `internal/core/lock_{unix,windows}.go`,
  `internal/core/process_{unix,windows}.go`,
  `cmd/vender/proxy_{linux,windows}.go`,
  `internal/autostart/autostart_{linux,windows,other}.go` — and never sprinkled
  through the CLI.
- **A privilege is a documented input, not an accident.** TUN mode is the only
  thing vender does that needs a capability, so it is the only thing that checks
  for one, and the check is a value (`core.TUNPrivilege`) rather than a bool: it
  carries the detail and the fix, so the engine's error, `vender tun status` and
  `vender autostart status` all say the same true thing. The TUN *rendering* is
  covered by golden files; the TUN *capability* logic by unit tests against a
  real `/proc/self/status` mask and a real capability xattr; the engine's
  acceptance by `sing-box check` for all three modes with TUN on and off, in a
  test that skips when no engine binary is present (so a clean clone still
  passes `go test ./...`).
- **A Windows path is not "untestable".** Build tags are used only where the
  real API is needed. Everything a Windows path *decides* — the task XML, the
  `schtasks`/`reg` argument vectors, UTF-16 encoding, the parsing of their
  output, and the registry write ordering — lives in a file with no build tag
  behind an injected runner, so those tests run on Linux and the same code is
  proven by `GOOS=windows go build ./...`. `internal/autostart/windows.go` and
  `cmd/vender/winproxy.go` are the two examples.
- **The licence obligation is part of the build.** vender is GPL-3.0-or-later
  and bundles the GPL sing-box engine, so `scripts/package.sh` refuses to build
  an archive without `LICENSE`, `NOTICE` and sing-box's own licence text, and
  both archives carry them. The `NOTICE` names the upstream copyright, the extra
  upstream term and the pinned version.
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
- **A control channel that cannot be reached off-host, and cannot be reached
  without the token.** Every message is versioned and authenticated, the token
  is 128 random bits per daemon start, it lives in a `0600` file next to the
  lock, and it is removed when the daemon exits. A test asserts the file mode,
  the removal, and that a forged token is rejected while the daemon carries on
  serving everyone else.
- **The TUI is testable without a terminal.** `View` is pure and colour is a
  property of one renderer, so the screens are covered by golden files
  (`internal/tui/testdata/`, refreshable with `go test ./internal/tui -update`)
  for the Dashboard running and not running, Nodes populated and empty, Settings,
  and Settings with a subscription profile to switch between — plus the behaviour
  tests that go through the same `Update` the program uses.
- **A subscription test opens no socket.** The parsers are pure and covered by
  fixtures under `internal/subscription/testdata/` (plain links, base64 links,
  Clash YAML), and `Fetch` is tested by injecting an `http.Client` whose
  `RoundTripper` returns a canned response, so the test can assert the user agent
  without a listener — not even a loopback one. `cmd/vender` uses the same trick
  through a `subscriptionClient` seam, so `vender import --url` and the whole
  `profiles` lifecycle are exercised without touching the network.
- **The daemon's own channel is tested against a real daemon.** `cmd/vender`'s
  tests run `vender daemon` in-process with the test binary standing in for
  sing-box (including its Clash API), then drive status/servers/select/mode/
  latency and both streams over loopback TCP, and assert that a bad token is
  rejected and that the endpoint file is published and withdrawn.
- **Committed binary assets.** The embedded `.srs` rule sets are the only
  binary data in the repository. They are committed rather than generated at
  fetch time so a clean clone builds, tests and runs offline; their provenance
  and checksums are pinned in both `scripts/fetch-rules.sh` and
  `internal/rules/assets.go`, and a test asserts the two agree.

[cobra]: https://github.com/spf13/cobra
