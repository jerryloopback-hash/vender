# Direct ICMP Ping

On Nodes, `t` probes the highlighted server; `T` probes every row (all profiles).
`Esc` cancels, and `q`/Ctrl-C still quits. Navigation, status and traffic continue.
At most four node requests run concurrently. Each node has a 10-second daemon
budget (including DNS), and a batch has a two-minute budget. Cancellation closes
client connections and cancels daemon sockets. Batch IDs discard late results.
No selection, active profile, proxy mode, route, firewall or TUN setting changes.

All node types, including Hysteria2, use the same three ICMP Echo requests, each
with at most a one-second reply window. The displayed RTT is the mean of replies
from one resolved address, rounded up to at least 1 ms; `/33%` is packet loss
(one lost packet out of three). The selected row shows reply counts, the actual
interface/address and any error. No reply means ICMP did not answer; it does **not**
prove the proxy protocol is unusable. Permissions or an unknown direct exit are
reported as unmeasurable, separately from ICMP silence. This test does not measure
TCP handshakes, authenticated proxy sessions or website fetches.

The `latency` control reply preserves `name`, `ms` and `err` and adds
`method: "icmp-direct"`, `sent`, `received`, `packetLoss`, `interface`, `address`.
Old clients may ignore the added fields. A new TUI refuses unlabelled results
from an older daemon, so URLTest/TCP results cannot be presented as ICMP.
Packet loss is shown when sample counts exist.
Manual Ping is independent of sing-box and never invokes `ClashClient.Delay`.
Automatic failover continues using sing-box URLTest health; that data is labelled
as failover health and cannot populate or overwrite the Nodes Ping column.

## Physical exit constraints

An explicit `engine.bind_interface` takes precedence. Otherwise the route
inventory chooses a matching physical route by longest prefix, then metric.
Equal-cost routes on different interfaces are refused: configure the interface.
The same rules apply to resolver endpoints, bootstrap and ICMP destinations.
No code path retries with an unbound socket or changes host routing.

- **Linux:** read the kernel's route inventory through `RTM_GETROUTE`, retain
  unicast routes in the main table on up, non-loopback interfaces with a sysfs
  hardware `device` link, and exclude TUN/TAP. Multipath routes are unsupported.
  Bind every socket with `SO_BINDTODEVICE` before sending, and bind its source
  address to that interface. Policy routing that prevents this constrained
  lookup produces an error or no reply. Raw ICMP normally needs root or
  `CAP_NET_RAW`; privilege failure is an error, never a different test method.
- **Windows:** read `GetAdaptersAddresses`, `GetIfEntry2Ex` and
  `GetIpForwardTable2`; require `HardwareInterface`, operational up state,
  Ethernet/Wi-Fi type, no tunnel type, and a preferred source address. Choose
  routes with route metric **plus interface metric**. Every raw ICMP and UDP/TCP
  DNS socket sets `IP_UNICAST_IF` (IPv4 interface index in **network byte order**)
  or `IPV6_UNICAST_IF` (IPv6 index in **host byte order**) before bind/connect.
  The outgoing interface socket option is the constraint; source binding alone
  is insufficient. Raw sockets require an elevated daemon. Failure of creation
  or setsockopt terminates the probe.

Virtual bridges, veth, VPN adapters and devices for which hardware identity
cannot be established are deliberately refused, even when an interface was
explicitly named. Hyper-V virtual switches, some cellular/PPP adapters and
container-only networking can therefore be unmeasurable. A privileged third-party
packet interception/filter driver can still drop packets; this feature does not
reconfigure such a driver or promise a reply. Windows native socket behavior
still needs real Windows 11 verification; Linux-hosted Windows checks only
compile the implementation and tests.

Primary Windows API references:

- [IPv4 socket options: IP_UNICAST_IF](https://learn.microsoft.com/en-us/windows/win32/winsock/ipproto-ip-socket-options)
- [IPv6 socket options: IPV6_UNICAST_IF](https://learn.microsoft.com/en-us/windows/win32/winsock/ipproto-ipv6-socket-options)
- [HardwareInterface and operational flags: MIB_IF_ROW2](https://learn.microsoft.com/en-us/windows/win32/api/netioapi/ns-netioapi-mib_if_row2)
- [Route metric and interface metric: MIB_IPFORWARD_ROW2](https://learn.microsoft.com/en-us/windows/win32/api/netioapi/ns-netioapi-mib_ipforward_row2)
- [Raw socket privileges and received headers](https://learn.microsoft.com/en-us/windows/win32/winsock/tcp-ip-raw-sockets-2)

## Domain nodes and DNS

An IP literal skips DNS. Domain nodes use their **own** profile's longest-suffix
`namespace_policy` match. A matching policy is exclusive: only that resolver and
its permitted profile bootstrap may be used, even if resolution fails.

Without a matching namespace policy, profile `nameservers` entries are tried in
order. A failed, empty or unusable answer advances to the next candidate. The
first usable answer supplies this measurement; answers from different resolvers
are never merged. Exhausting the list returns an error and never switches to a
public substitute. UDP/bare and TCP addresses use `config.ParseResolverAddress`,
including IPv6 and custom ports. An explicitly selected TLS/HTTPS resolver is
unsupported and returns an error rather than downgrading to plaintext DNS.

When there is no applicable profile resolver (including a manual domain node
with no profile DNS or an empty policy), vender uses its explicit built-in
**223.5.5.5 over UDP**, shared with rule-mode's direct domestic DNS through
`rules.BuiltinDirectDNSAddress`. This default still uses mandatory physical-exit
binding. It is selected only in the absence of an applicable resolver, never as
recovery from a configured provider policy or nameserver list failing.

Resolver hostnames bootstrap only against UDP/TCP **IP-literal nameservers from
the same profile**, in their configured order. Without a working permitted
bootstrap, resolution fails; the built-in default is not used for bootstrap.
Empty, failed, local or FakeIP bootstrap answers cannot become resolver endpoints.
Each resolver/bootstrap lookup has a three-second budget within the node's total
10-second budget; every DNS wire exchange remains limited to 1.5 seconds.
Cancellation or expiry of the node budget stops further candidates; a single
lookup timeout may advance to the next permitted candidate. No-result paths
always return an explicit error.

All DNS sockets use the same physical-exit selection and mandatory binding as
ICMP. The small DNS wire client asks exact A and AAAA names, follows bounded
CNAME chains and retries truncated UDP replies over TCP to the same endpoint and
interface. It does not use system DNS, hosts files, search suffixes, proxy
environment variables or the engine's FakeIP resolver. Local/non-unicast answers
and common FakeIP ranges `198.18.0.0/15` and `fc00::/18` are refused. Addresses in
these ranges are also refused as literal node/resolver addresses, so legitimate
overlap is unsupported. ICMP silence still means no ICMP reply, not proxy protocol
failure; raw socket privileges remain required as described above.

## Ubuntu acceptance (2026-10-18)

A separate daemon with `CAP_NET_RAW`, independent config/run directories and no
TUN was tested while the production TUN remained running. All four VPS entries
reported `method=icmp-direct` and the physical Wi-Fi interface, not `vender0`:

| Entry | Replies | Mean RTT |
|---|---|---|
| JP-1 | 3/3 | 121 ms |
| JP-1-V6 | 3/3 | 116 ms |
| JP-1-Hy2 | 2/3 | 175 ms |
| JP-1-Hy2-V6 | 3/3 | 169 ms |

Ping still succeeded after that test daemon's managed engine was stopped.
The production daemon PID, engine PID, selected node, mode and profile stayed
unchanged. Nodes sharing an IP are the same ICMP destination: differences in
these tiny sequential samples do not compare Reality and Hy2 protocol quality.
The single loss also does not establish a persistent loss rate.
