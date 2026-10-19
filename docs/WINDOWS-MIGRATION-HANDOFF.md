# Windows 迁移交接：Clash for Windows → vender

更新日期：2026-10-19。本文不含节点密钥、订阅地址或控制接口 token，可进入 Git。

## 1. 已确认的目标与执行权限

- 在 **Windows 本机**，由用户以管理员权限启动的 agent 执行；不依赖 Linux 远程连接。
- 最终使用 **vender daemon + sing-box，rule 模式 + TUN**，完全替代 Clash for Windows（CFW）。
- 用户登录桌面后自动运行，无需手动启动 vender 或 TUI；**不要求未登录时运行的 Windows 服务**。
- Bing 主要用于普通搜索：普通搜索入口必须直连，不能因 Microsoft 大类规则而被代理。
- **先备份、后停用、先验证、后卸载**。CFW 的彻底清理必须等 vender 功能和登录自启动均验证成功。
- 安装、切换、验证、注册自启动和验证后的 CFW 卸载属于本次任务。**重启/注销前仍须通知并取得用户确认**；无关 VPN、共享驱动、企业策略及不明确的残留不得自行删除。
- 执行中保留阶段记录。阻塞时停止危险操作，向用户报告证据和可恢复状态，不猜测成功。

## 2. 项目与版本依据

仓库：<https://github.com/jerryloopback-hash/vender>

必须包含提交 **`8ce785f` — `fix(rules): route Bing search directly and align DNS policy`**，可用其后继版本。

在仓库根目录验证（PowerShell 中注意检查原生命令退出码）：

```powershell
git rev-parse HEAD
git merge-base --is-ancestor 8ce785f HEAD
if ($LASTEXITCODE -ne 0) { throw '当前源码不包含 Bing/DNS 修复，停止迁移' }
```

**不要假设现有 GitHub Release 已包含这次修复。** Linux 已运行本地构建版，Windows 需要另备 Windows 可执行文件。程序支持 Windows 11；先检查目标系统版本和架构，不能把 Windows ARM64 当成 amd64 原生环境。

关键实现，遇到文档不一致时以当前源码和实测为准：

| 文件 | 内容 |
|---|---|
| `internal/rules/rules.go` | Bing 例外、Microsoft 等代理覆盖、CN 直连 |
| `internal/rules/dns.go` | 按有效域名路由顺序生成 DNS 分流 |
| `internal/core/render.go` | sing-box JSON、rule/global/TUN 差异 |
| `internal/autostart/windows.go` | 计划任务、用户 SID、最高可用权限、启动参数 |
| `cmd/vender/winproxy.go` | WinINET 代理备份、恢复、外部更改保护 |
| `scripts/fetch-sing-box.sh` | 内核版本、官方来源、归档校验值 |
| `README.md` | CLI、配置和安装说明 |

### 本次规则修复的准确边界

- 精确域名直连：`bing.com`、`www.bing.com`、`cn.bing.com`、`global.bing.com`、`bing.com.cn`、`www.bing.com.cn`、`s.bing.net`。
- 后缀直连：`r.bing.com`、`th.bing.com`、`cn.bing.net`、`mm.bing.net`。
- `edgeservices.bing.com`、`sydney.bing.com`、`copilot.microsoft.com` 仍按 Microsoft 策略代理。
- 无法区分同一 HTTPS 域名的不同 URL 路径。因此 `www.bing.com` 上共享域名的 AI 页面也会直连；不要承诺按页面路径区分。
- DNS 按有效域名路由优先级投影：直连域名 → 国内真实解析；代理域名 A/AAAA → FakeIP；拦截域名 → DNS reject。
- IP-only 规则不投影到 DNS；非地址查询仍可能使用国内解析器。这不是完整的防 DNS 泄漏方案。
- `global` 不使用上述分流例外。本次目标必须是 `rule`，不能为了“全机生效”改成 `global`。

## 3. 用户需要单独提供的私密迁移材料

Git 仓库 **没有**真实节点配置。以下文件需要用户从 Linux 通过可信的私密渠道传到 Windows，放在仓库以外的受限目录：

| Linux 源文件 | 用途 | 处理方式 |
|---|---|---|
| `~/.config/vender/servers.yaml` | 节点、协议参数和凭据 | 必需，保留全部协议字段 |
| `~/.config/vender/profiles.yaml` | 分组、订阅、提供商专属 DNS | 建议一并迁移；缺失时先向用户确认 |
| `~/.config/vender/config.yaml` | 当前模式、节点、TUN 参数 | 作为参考，不直接照搬 Linux 路径 |
| `~/.config/vender/rules.yaml` | 用户规则 | 仅存在时迁移；交接时 Linux 没有此文件 |

已知 Linux 源目录为 `/home/ll-lite/.config/vender/`；源代码目录为 `/home/ll-lite/vender/`。

禁止复制/发布：

- `.cache/vender/` 整个运行目录，包括 `singbox.json`、`control.json`、`fakeip.db`、锁和 PID。这些包含凭据或进程状态，应在 Windows 重新生成。
- `DEBUG-HANDOFF.md`：Linux 本地调试文件含真实密钥，不能当成公开交接文档上传。
- Linux SSH 私钥、浏览器认证资料。Windows 拉仓库应使用本机自己的 Git 认证，不复制 Linux 私钥。

不要把 YAML 原文、分享链接或订阅 URL 打进聊天/日志/提交。`servers list --links` 会泄露凭据；`profiles list/show` 也可能显示订阅地址，记录前应脱敏。Windows 用 ACL 限制私密目录和日志，不把 `chmod 600` 当成 Windows 权限方案。

### 已知节点与网络差异

Linux 当前使用 `JP-1-V6`，但 **Windows 的 IPv6 可用性尚未验证**，不要照搬就认定节点失效。

源配置中有 `manual` 分组的 `JP-1`（IPv4 VLESS）、`JP-1-V6`、`JP-1-Hy2`、`JP-1-Hy2-V6`；以迁移到手的文件为准。初次验证建议选 IPv4 的 `JP-1`，验证成功后再考虑 IPv6 或 auto。

另有提供商分组及节点域名专属 DNS。若使用这些节点，必须保留 `profiles.yaml` 中相关 DNS 策略，不能拿公共 DNS 随意替换，否则节点入口可能解析错误。

## 4. 阶段 A：只读盘点与备份，CFW 继续运行

在任何改动前，记录：

1. Windows 版本、CPU 架构、当前用户与 SID、是否已提升。提升后必须仍是目标日常登录用户；若切换为另一个管理员账户，HKCU、AppData 和任务身份都会变化，需先确认。
2. CFW 安装类型（安装包/便携/包管理器）、路径和版本；运行中的 GUI、Clash 核心、辅助服务及其可执行路径。
3. CFW 自启动：应用选项、Run/RunOnce、启动文件夹、计划任务、服务模式。服务名应通过路径和关联信息确认，不能只凭 `clash`/`tun` 名称删除。
4. 网络基线：默认路由、网卡与虚拟网卡、DNS、IPv4/IPv6、CFW 的 TUN/TAP/服务模式、监听端口及其 PID。
5. 当前用户 WinINET 手动代理、PAC、自动检测；WinHTTP 代理；用户/系统环境变量 `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`、`NO_PROXY`（含大小写变体）；浏览器扩展和应用独立代理。
6. 企业策略或其他 VPN。如代理由企业策略管理，或网卡归属不明，停止相关清理并询问。

可以使用 `Get-CimInstance Win32_Process`、`Get-CimInstance Win32_Service`、`Get-ScheduledTask`、`Get-NetAdapter`、`Get-NetRoute`、`Get-DnsClientServerAddress`、`Get-NetTCPConnection`、`netsh winhttp show proxy` 等。命令行、PAC URL、环境变量可能含敏感数据，输出需脱敏。

在仓库外建立有日期的迁移备份目录，备份 CFW 配置/订阅、相关注册表值、计划任务 XML、服务启动信息和网络基线。备份不等于允许盲目覆盖恢复：回滚时只恢复本次实际修改的项目。

**网络依赖保护：** Windows agent 本身可能依赖 CFW。提前下载程序、内核、源码与构建依赖，并把恢复步骤保存到本地；切换失败时不要反复破坏网络，应请用户执行本地回滚。

## 5. 阶段 B：准备 vender，不卸载 CFW

建议程序目录为 `$env:LOCALAPPDATA\vender\bin`；不要放在临时目录、下载缓存或随时会移动的源码构建目录。高权限任务会执行这些文件，目录不得允许其他普通用户写入。

### 源码构建

需要 Go 1.26+。在 Windows PowerShell 的源码根目录执行；先检查遗留的 `GOOS`/`GOARCH` 等环境设置，构建到目标架构。路径中的空格必须正确引用。

```powershell
go version
go env GOOS GOARCH
go test ./...
if ($LASTEXITCODE -ne 0) { throw '测试失败，先排查' }
$commit = (git rev-parse --short HEAD).Trim()
$date = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
go build -ldflags "-X main.version=windows-migration -X main.commit=$commit -X main.date=$date" -o .\vender.exe .\cmd\vender
if ($LASTEXITCODE -ne 0) { throw 'Windows 构建失败' }
```

也可接收 Linux 端交叉构建的可信 Windows 包，但必须记录源码版本与 SHA-256，不能拿 Linux 二进制运行。上述测试不保证 Windows TUN、任务计划和真实网络已通过验收，后续必须实测。

### sing-box 内核

交接时锁定 **1.14.3**，Windows amd64 官方归档：

```text
https://github.com/SagerNet/sing-box/releases/download/v1.14.3/sing-box-1.14.3-windows-amd64.zip
SHA256 d8b86ef58936dba9230ec6a57ffa4243933f3c14ef4809828b6c553440ed1518
```

以当前 `scripts/fetch-sing-box.sh` 为准，用 `Get-FileHash -Algorithm SHA256` 校验下载的 **ZIP 归档**。不要把归档 hash 用来校验解压后的 EXE。

将 `vender.exe`、匹配的 `sing-box.exe` 和发行包所需配套文件放在一起，保留许可证。不从第三方 DLL 下载站补驱动；TUN 若报驱动/依赖问题，先记录完整错误、核对官方发行包及 Windows 安全日志，不预先承诺需要手工安装某个 DLL。

### 配置目录

```text
%APPDATA%\vender\       config.yaml、servers.yaml、profiles.yaml、可选 rules.yaml
%LOCALAPPDATA%\vender\  自动生成的运行状态
%LOCALAPPDATA%\vender\bin\  建议的稳定程序目录
```

导入前备份可能已有的 vender 配置，不能覆盖未知用户数据。迁移 `servers.yaml` 和 `profiles.yaml`，为 Windows 整理 `config.yaml`：

- `proxy.listen: 127.0.0.1:7897`（端口被占用时先查归属，再决定是否改端口；不要强杀未知进程）。
- `proxy.mode: rule`。
- `engine.binary: ""`，使用同目录的 `sing-box.exe`；不要保留 Linux 路径。
- `engine.bind_interface: ""`，不照搬 Linux 网卡名。
- 初次 `engine.tun.enabled: false`，TUN 验证阶段再开启；`stack: mixed`、`auto_route: true`、`strict_route: false` 可作为起点。
- 初次选择 `manual` 分组内可用的 IPv4 节点，例如 `JP-1`，而不是未经验证的 `JP-1-V6`。
- 不复制运行缓存、旧 PID、control token 或 FakeIP 数据库。

在管理员 PowerShell 中，用绝对路径调用，以下 `$v` 应替换为实际稳定路径：

```powershell
$v = Join-Path $env:LOCALAPPDATA 'vender\bin\vender.exe'
& $v version
& $v rules list
& $v profiles use manual
& $v servers use JP-1
```

最后两条仅在相应分组和节点确实存在时执行；不要打印分享链接或完整订阅。

## 6. 阶段 C：停用 CFW，隔离验证 vender

1. 从 CFW 正常关闭系统代理、TUN/服务模式与自启动，退出 GUI；检查其核心和辅助服务是否仍在运行。停止已确认属于 CFW 的项，但**暂不卸载、不删配置**。
2. 确认旧代理、PAC、环境变量没有继续指向失效端口。需要修改时先备份，只改已确认属于 CFW 的值。WinINET 和 WinHTTP 是两套设置，vender 的 `proxy off` 不等于清理所有代理。
3. 初次从目标用户的管理员 PowerShell 前台执行 `& $v daemon`，在另一终端运行 `& $v status`。没有顶层 `vender start/stop/restart` CLI；不要编造命令。前台实例用 Ctrl+C 正常停止。
4. 先验证本地 HTTP/SOCKS5 代理能连接，查看生成的配置并执行 `sing-box.exe check -c <实际 singbox.json 路径>`。CFW 必须已停止接管，避免 vender 的上游仍经旧代理而出现假成功。
5. 在同一用户的管理员终端执行 `& $v tun on`；检查 `tun status`、TUN 网卡、路由、DNS 和真实流量。如果权限不足，要提升运行 **daemon** 的进程；仅提升 CLI 不能给既有非管理员 daemon 补权限。
6. 保持 `rule`，不要为了全机接管改成 `global`。不要同时启用 CFW TUN 和 vender TUN。

最终以 TUN 提供透明接管为主；通常不必再执行 `vender proxy on`。确需系统代理时说明理由，并记录 vender 的代理备份状态，避免以后恢复出 CFW 的旧端口。不要不经检查执行 `proxy off --force`。

### 验证必须看实际路径，而不只看“网页能打开”

| 项目 | 合格证据 |
|---|---|
| Bing 普通搜索 | 浏览器真实搜索成功；`www.bing.com`/`cn.bing.com` 命中 `bing-search-direct`，出口 direct |
| Bing 图片/静态资源 | 相关页面资源能加载；允许列表内图片/静态域名直连 |
| 升级服务分流 | `edgeservices.bing.com`、`sydney.bing.com`、`copilot.microsoft.com` 仍选择 proxy；不要求用户登录或购买服务 |
| Microsoft/CN 重叠 | 例如 `download.microsoft.com`，A/AAAA 应走 FakeIP，与其 proxy 路由一致；不要求下载大型文件 |
| 国内网站与局域网 | 国内站点直连，路由器/NAS 等已有内网访问不受损 |
| 代理目标 | 一个用户常用境外目标经所选节点可用，不依赖 CFW |
| DNS | 直连入口真实解析、代理域名 FakeIP；A 和 AAAA 均检查，非地址查询边界如实记录 |
| 稳定性 | Wi-Fi 重连、休眠唤醒后恢复；IPv6 不可用时 IPv4 节点仍可工作 |

可使用本地 sing-box Clash API 的 `/connections`、`/dns/query` 和日志取证；地址与 secret 从该机运行配置读取，**只在本地使用，不打印 secret**。API 返回地址不能单独证明出口，需结合命中规则/出口和实际连接。

浏览器安全 DNS、独立代理扩展、缓存和既有长连接会影响观察。不要关闭 TLS 证书校验或装 MITM 证书来验收；必要时只临时调整测试浏览器设置并记录恢复办法。新建连接后再判定规则。

## 7. 阶段 D：登录自启动与重启验收

1. 确认 `config.yaml` 已是 TUN enabled + rule，稳定程序路径不会变化。
2. 在 **目标日常用户的管理员 PowerShell** 中：

   ```powershell
   & $v autostart on
   & $v autostart status --json
   ```

3. 检查 `\vender\daemon` 任务 XML：目标用户 SID、登录触发、`HighestAvailable`、绝对程序路径、`daemon` 参数及正确配置/运行目录。若退化成普通 Run 项或低权限任务，**TUN 自启动不算完成**。
4. 不要仅凭命令返回成功认定任务已启动。当前 Windows 实现注册任务后不一定立即运行；先正常停止前台验证实例，再显式从计划任务启动：

   ```powershell
   schtasks /Run /TN '\vender\daemon'
   ```

5. 确认只有一个 vender daemon 和其 sing-box 子进程、端口正常、TUN 正常；不是前台测试窗口在代替任务工作。TUI 不是必需进程。
6. 请用户保存工作并批准重启/注销。登录桌面后，在**不手动运行 daemon/TUI**的前提下检查 status、任务、真实联网与 Bing 分流。
7. 如果用户暂不能重启，将“重启验收”明确标记为待办，**先保留 CFW 回滚能力，不进入彻底清理阶段**。

控制台可能短暂闪现，不等于自启动失败。此处目标是不需要手动操作，不是承诺绝对无窗口。TUN 也不是程序退出后仍生效的防火墙 kill switch。

## 8. 阶段 E：验收成功后彻底清理 CFW

- 使用原安装渠道正常卸载（安装包/包管理器/便携目录按实情处理）。备份保留在受限目录内，直至用户确认无需回滚。
- 清理已核实属于 CFW 的自启动、计划任务、核心/辅助服务、安装目录和配置残留。
- 驱动和虚拟网卡：依据设备、驱动包、路径和实际使用关系确认归属。共享 Wintun/TAP 驱动或其他 VPN 的网卡不得因为名字相似就删除；归属不明先报告。
- 检查 WinINET/PAC、WinHTTP、环境变量、浏览器扩展及应用设置，不再指向 CFW 旧端口；不删除用户无关代理配置或企业策略。
- 不执行全局 `netsh winsock reset`、`netsh int ip reset`、全量路由清空、批量网卡删除、全注册表搜关键字删除；确需特殊修复先解释并单独取得确认。
- 清理后复验 vender，并报告残留。不要把“零个名字里带 clash 的条目”当作唯一验收标准。

## 9. 回滚（切换前就保存到本机）

遇到 vender 连通性、TUN、权限或自启动不能解决的问题时：

1. 若 vender 可响应，先 `& $v tun off`；关闭本次注册的自启动：`& $v autostart off`。移除启动项不保证停止现有进程，需另行确认。
2. 若需结束计划任务，可在任务仍存在时用 `schtasks /End /TN '\vender\daemon'`；或停止已核实路径/PID 的本次 vender 进程与子进程。前台实例用 Ctrl+C。禁止按 `sing-box` 名称杀掉其他项目进程。
3. 确认本次 TUN 接管已解除、vender 子进程没有残留。仅对本次已确认的网卡/路由做必要修复，不全局重置网络。
4. 按盘点备份，恢复本次改过的 CFW 启动方式、服务和代理设置，重新启动保留的 CFW。此时不能再有 vender TUN 同时工作。
5. 验证正常联网，报告失败阶段、脱敏错误和当前恢复状态。不要把 CFW 卸载后才准备回滚方法。

## 10. 交付报告与求助格式

Windows agent 将脱敏结果写入仓库外的 `migration-report.md`，至少包括：

- 系统/架构/用户身份；源码提交、程序/内核版本、二进制校验值和安装路径。
- 配置/日志/备份位置；只记节点名称、协议、IPv4/IPv6，不记密钥和订阅。
- 每阶段“完成/失败/待用户操作”；进程父子关系、监听、任务名称与权限证据。
- Bing direct、升级域名 proxy、Microsoft 重叠 DNS、国内/境外访问和登录自启的验收表。
- CFW 已移除项、未移除项及理由；是否仍有需要用户处理的其他代理/驱动。
- 回滚步骤以及重启是否真正验证。不能只写“已配置自启动”。

需要 Linux 端协助时只提供：失败命令、退出码、脱敏日志、Windows/程序版本、所处阶段、CFW/vender/TUN 当前状态；不要提供 YAML 原文或 token。
