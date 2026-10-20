# Windows 11 人工安装、迁移与故障恢复

这份指南供另一台 Windows 11 电脑的使用者独立操作，不需要 AI agent。我们先准备完整离线材料，再验证本地代理，最后启用 TUN 和登录任务。原来的 Clash for Windows 简称 CFW，在真正重启验收前只停用，不卸载。

我们依据仓库源码与 [Windows 迁移交接](WINDOWS-MIGRATION-HANDOFF.md) 整理操作。静默启动功能已在本提交实现，但不能当作所有旧 Release 已有的功能，也不能代替另一台电脑上的真实登录验收。执行前核对所用提交中的文件、参数和测试结果。下文的现场观察仅说明原现场发生过什么，不保证新电脑表现相同。

## 安装前先认清程序和身份

`vender.exe` 提供 CLI、可选终端界面 TUI 和常驻 daemon。daemon 管理自己的 `sing-box.exe` 子进程，本地默认监听 `127.0.0.1:7897`，同时接受 HTTP 和 SOCKS5。TUN 是可选的系统透明接管，默认关闭。TUI 只是控制客户端，退出 TUI 不会退出 daemon。实现依据是 `cmd/vender/root.go`、`cmd/vender/daemon.go` 和 `internal/core/process_windows.go`。

本提交提供 GUI 子系统的 `vender-daemon.exe`，源码入口为 `cmd/vender-daemon`。它是静默启动伴随程序，不是托盘配置界面，也不替代正常的 `vender.exe`。主程序仍应保留控制台子系统，方便人工查看错误。静默 helper 使用生命周期 Job Object 管理子进程，不能把任意隐藏窗口工具视为等价替代。

项目声明支持 Windows 11，Go 构建要求为 1.26 或更高。现有内核锁定脚本只给出了 Windows amd64 的版本与归档校验值，所以本指南的完整二进制流程针对 x64。ARM64 的系统可以仿真部分 x64 应用，但不能据此保证 TUN、驱动与配套 DLL 兼容。ARM64 机器应停在准备阶段，另行核实整套架构，不能只换一个 EXE。参见 [Windows ARM 应用说明][ms-arm] 和 `scripts/fetch-sing-box.sh`。

在日常登录用户的普通 PowerShell 先记录身份，随后右键以管理员身份打开 PowerShell，再执行同样命令比较 SID：

```powershell
Get-CimInstance Win32_OperatingSystem | Select-Object Caption, Version, BuildNumber, OSArchitecture
Get-CimInstance Win32_Processor | Select-Object Name, Architecture
whoami /user
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$identity.User.Value
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
$principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
```

两个窗口必须属于同一个日常用户 SID，管理员窗口最后一项应为 `True`。若 UAC 要求输入另一个管理员账户，先停止，不要继续创建那个人的 HKCU、AppData 和登录任务。`HighestAvailable` 不是把普通账户变成管理员的办法。参见 [UAC 工作方式][ms-uac]、[任务运行级别][ms-runlevel]。

## 先备份到离线可读的位置

切换网络前，把本文、恢复步骤、源码、Go 安装材料、构建依赖、完整程序包、官方 sing-box ZIP 和私密配置准备好。Go 模块下载也可能依赖旧代理，应在 CFW 仍正常时完成。之后运行测试与构建应不再临时上网找依赖；内置规则不需启动时下载，但真实节点、提供商 DNS 和订阅更新当然仍需网络。

在仓库外建立按日期命名的私密备份目录，保存原 CFW 配置、安装来源、启动方法和快捷方式。记录安装型还是便携型、CFW 与核心的完整路径、相关服务的启动类型、已确认的自启动和任务 XML。不要按进程名或服务名包含 `clash` 就认定归属。

同时记录网卡、默认路由、DNS、监听端口、WinINET 手动代理与 PAC、自动检测、WinHTTP 代理、用户及系统代理环境变量。浏览器插件和应用自己的代理也要记下来。以下都是盘点命令，输出仍可能含隐私，不应原样上传：

```powershell
Get-NetAdapter
Get-NetRoute -DestinationPrefix '0.0.0.0/0'
Get-NetRoute -DestinationPrefix '::/0'
Get-DnsClientServerAddress
Get-NetTCPConnection -State Listen | Select-Object LocalAddress, LocalPort, OwningProcess
& "$env:SystemRoot\System32\netsh.exe" winhttp show proxy
```

注册表备份应记录每个相关值的名称、类型、内容和是否原本存在，包括 Run、用户 PATH、App Paths 与代理项。值不存在是有效状态，恢复时应恢复成不存在，而不是造一个空字符串。不要为备份导出整个注册表，更不要事后不加辨别地导入整个 Internet Settings 或整段 PATH。

私密文件只经可信渠道传输。必需的是 `servers.yaml`，建议连同 `profiles.yaml` 和实际存在的 `rules.yaml` 一并保留。Linux 的 `config.yaml` 只能作为整理参考。不要复制旧机器的运行目录、`singbox.json`、`control.json`、锁、PID 或 `fakeip.db`，Windows 应自行生成。禁止传播 `DEBUG-HANDOFF.md`、分享链接、订阅 token 与私钥。

## 确认真实提交，不凭 M7 或 Release 猜版本

我们需要包含 `8ce785f` 的后继源码，其中有 Bing 与 DNS 规则修复。Release 标签只是发布名称，不自动证明包含该修复，也不证明包含这轮 helper 与安装脚本。TUI 里出现 M7 更不是旧程序的充分证据。

已知根因是旧版 `internal/tui/model.go` 硬编码了 `const milestoneTag = "M7"`，而 README 状态已经写 M8。`8ce785f` 本身也包含这个旧标签，见 [该提交的原始文件](https://github.com/jerryloopback-hash/vender/blob/8ce785f/internal/tui/model.go)。原现场安装程序来自 `d684fad` 源码构建，它是 `8ce785f` 的后继，不是因为误装了旧 Release。我们在本提交把标签改为 M8，并增加与 README 状态一致的回归测试。这里分别涉及界面里程碑、README 功能阶段、Release 名、版本元数据和真实 Git 提交，不能混为一谈，也不要把 `d684fad` 当成所有安装都该显示的版本。更新源码或提交 Git 不会自动替换正在运行的旧二进制。

在准备好的源码根目录执行：

```powershell
git rev-parse HEAD
if ($LASTEXITCODE -ne 0) { throw '无法读取源码提交' }
git merge-base --is-ancestor 8ce785f HEAD
if ($LASTEXITCODE -ne 0) { throw '未证实包含必要修复，停止' }
git status --short
```

浅克隆缺历史时，祖先检查失败也可能是缺少对象，不能擅自认定通过。联网准备阶段补齐可信历史，或由交付方提供可核验的完整仓库。工作区若有修改，应先确认来源并记录，不能把一个干净提交号当作修改后源码的全部证明。

## 在 Windows 从源码构建

先从 [Go 官方下载页][go-download] 准备 Go 1.26+，按 [官方安装说明][go-install] 安装。新开 PowerShell 后检查 `go version` 和 `go env`。若 Go 不可见，先用已确认的绝对路径排查，不要从不明网站下载所谓修复程序。

下面在 x64 Windows 的可信源码根目录构建，输出放在源码内的暂存目录。我们不直接覆盖正在运行的稳定程序。检查遗留的交叉编译环境，确认 `GOOS=windows`、`GOARCH=amd64`，不要把 Linux 产物误当 Windows 文件。

```powershell
go version
go env GOOS GOARCH GOTOOLCHAIN
if ((go env GOOS).Trim() -ne 'windows' -or (go env GOARCH).Trim() -ne 'amd64') { throw '遗留交叉编译环境不符；先纠正 GOOS/GOARCH，再测试' }
go mod download
if ($LASTEXITCODE -ne 0) { throw '依赖未准备完整' }
go test ./...
if ($LASTEXITCODE -ne 0) { throw '测试失败，停止安装并排查' }
go vet ./...
if ($LASTEXITCODE -ne 0) { throw '静态检查失败' }
$commit = (git rev-parse HEAD).Trim()
$date = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$meta = "-X main.version=windows-manual -X main.commit=$commit -X main.date=$date"
New-Item -ItemType Directory -Force .\dist\manual | Out-Null
go build -trimpath -ldflags $meta -o .\dist\manual\vender.exe ./cmd/vender
if ($LASTEXITCODE -ne 0) { throw '主程序构建失败' }
```

若所选提交已经有 helper 入口，再构建它。若入口不存在，不能照抄命令后声称静默功能已交付，应选包含实现的可信提交，或者暂用可见控制台方案。

```powershell
go build -trimpath -ldflags "-H=windowsgui $meta" -o .\dist\manual\vender-daemon.exe ./cmd/vender-daemon
if ($LASTEXITCODE -ne 0) { throw '静默 helper 构建失败' }
& .\dist\manual\vender.exe version
go version -m .\dist\manual\vender.exe
go version -m .\dist\manual\vender-daemon.exe
Get-FileHash .\dist\manual\vender.exe, .\dist\manual\vender-daemon.exe -Algorithm SHA256
& .\dist\manual\vender-daemon.exe --launcher-info | ConvertFrom-Json | Select-Object protocol, version, commit, date
```

`main.version`、`main.commit`、`main.date` 的主程序实现见 `cmd/vender/version.go`，构建约定见 `scripts/build.sh`。helper 与主程序应来自同一份源码、同一架构，记录各自哈希。版本字符串可人工注入，所以还要结合源码与 Go 构建元数据核查，哈希也必须与可信交付记录比较。

旧源码在 Go 1.27 下曾因 readiness timeout 测试夹具使用 `select{}` 触发死锁。本提交已经修复该测试夹具，不修改真实内核等待逻辑。若仍遇到，应保留失败信息并取得修正后的源码重新完整测试，不要跳过失败测试或把失败当作真实 TUN 已验证。单元测试通过也不能代替目标电脑上的权限、网络和重启验收。

## 核对官方 sing-box 和稳定目录

当前锁定值来自 `scripts/fetch-sing-box.sh`。在可信联网阶段下载官方 ZIP，在 PowerShell 校验归档本身：

```powershell
$url = 'https://github.com/SagerNet/sing-box/releases/download/v1.14.3/sing-box-1.14.3-windows-amd64.zip'
$zip = Join-Path $PWD 'sing-box-1.14.3-windows-amd64.zip'
Invoke-WebRequest -Uri $url -OutFile $zip
$expected = 'd8b86ef58936dba9230ec6a57ffa4243933f3c14ef4809828b6c553440ed1518'
if ((Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash -ne $expected) { throw '官方内核归档校验不符，停止' }
Expand-Archive -LiteralPath $zip -DestinationPath .\dist\manual\sing-box-unpacked
```

不要拿 ZIP 的 SHA-256 比较解压后的 EXE。校验后保留完整解压内容，特别是 `sing-box.exe`、`libcronet.dll` 与许可证；不要只拖出一个 EXE。保留 vender 的 `LICENSE`、`NOTICE` 与单独命名的 `LICENSE.sing-box`，不要相互覆盖。重新分发时同时保留上游要求的许可和源码来源说明，参见 `scripts/package.sh`、`NOTICE`。

建议将审核后的整套文件人工复制到 `%LOCALAPPDATA%\vender\bin`。不要使用临时目录、下载缓存、会移动的源码输出目录或其他用户能写入的公共目录。高权限任务最终会执行这里的 EXE 和 DLL，目录 ACL 与文件哈希一样重要。

新电脑尚无 vender 时，可在源码根目录按下面的方式建立私密目录并复制本轮构建的文件。代码遇到既有目录会停止，不会覆盖旧配置。若安装来自可信完整发行包，将两个主程序的复制来源改为该包，仍须核对 helper 与真实提交；不要混用旧包和新源码。

```powershell
$root = Join-Path $env:LOCALAPPDATA 'vender'
$cfg = Join-Path $env:APPDATA 'vender'
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
foreach ($dir in @($root, $cfg)) {
    if (Test-Path -LiteralPath $dir) { throw "目标已存在，先盘点备份，不覆盖：$dir" }
}
foreach ($dir in @($root, $cfg)) {
    New-Item -ItemType Directory -Path $dir | Out-Null
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner($sid)
    foreach ($s in @($sid.Value, 'S-1-5-18', 'S-1-5-32-544')) {
        $identity = [Security.Principal.SecurityIdentifier]::new($s)
        $rule = [Security.AccessControl.FileSystemAccessRule]::new($identity, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $dir -AclObject $acl
}
$bin = Join-Path $root 'bin'
New-Item -ItemType Directory -Path $bin | Out-Null
Copy-Item .\dist\manual\vender.exe, .\dist\manual\vender-daemon.exe -Destination $bin
$engineDirs = @(Get-ChildItem .\dist\manual\sing-box-unpacked -Directory | Where-Object { Test-Path (Join-Path $_.FullName 'sing-box.exe') })
if ($engineDirs.Count -ne 1) { throw '内核解压目录不唯一或结构不符，先人工检查' }
$engine = $engineDirs[0].FullName
Copy-Item (Join-Path $engine 'sing-box.exe'), (Join-Path $engine 'libcronet.dll') -Destination $bin
Copy-Item (Join-Path $engine 'LICENSE') -Destination (Join-Path $bin 'LICENSE.sing-box')
Copy-Item .\LICENSE, .\NOTICE -Destination $bin
```

把私密配置文件放入已保护的 `$cfg`，不要放在 Git 目录。若复制工具保留了源文件上的额外 ACL，还要核对文件本身，不只看父目录。复制错误时保留已建目录并检查原因，不要换成递归删除后重来。`setup.ps1` 会检查稳定 bin 和其父目录是否允许其他身份写入，不能以关闭此检查来规避安全问题。

```powershell
$bin = Join-Path $env:LOCALAPPDATA 'vender\bin'
$v = Join-Path $bin 'vender.exe'
$sb = Join-Path $bin 'sing-box.exe'
$cfg = Join-Path $env:APPDATA 'vender'
$run = Join-Path $env:LOCALAPPDATA 'vender'
& $v version
& $sb version
Get-FileHash -LiteralPath $v, $sb, (Join-Path $bin 'libcronet.dll') -Algorithm SHA256
Get-Acl -LiteralPath $bin | Format-List Owner, AccessToString
```

确认其他普通用户没有写权限。配置、运行目录和迁移备份也必须限制访问。`internal/privatefs/protect_windows.go` 使用受保护 DACL，仅授予当前 SID 与 SYSTEM 完全访问并设置继承，不是靠 `chmod 600`。应用会保护它管理的私密路径，但不能据此假定外部手工备份、旧文件和整个父目录都已安全。ACL 操作依据参见 [icacls 文档][ms-acl]，不要对整个用户目录递归重置权限。

## 整理配置，先停用 CFW 再做隔离测试

若目标已有 vender 配置，先备份，不覆盖未知数据。将私密节点文件放入 `$cfg`，按 Windows 重新整理 `config.yaml`。下面只是起点，`selected_server` 必须改成这台电脑确实拥有且允许使用的节点名：

```yaml
log_level: info
proxy:
  listen: 127.0.0.1:7897
  mode: rule
engine:
  binary: ""
  bind_interface: ""
  tun:
    enabled: false
    stack: mixed
    auto_route: true
    strict_route: false
    interface_name: vender0
selected_server: YOUR_VERIFIED_NODE
```

`engine.binary` 为空时先查主程序旁的内核，再查 PATH；不要保留 Linux 绝对路径与网卡名。依据为 `internal/core/binary.go`、`internal/config/config.go`、`internal/config/tun.go`。若端口占用，先核对 PID 和可执行路径，不强杀未知程序。

`JP-1` 只是原交接材料中的 IPv4 节点名，不是程序自带节点。仅当实际配置里确实有它、属于预期分组且参数来源可信时，才执行 `& $v profiles use manual` 与 `& $v servers use JP-1`。不能因为名字相似就改服务器地址，也不能假定原来的 `JP-1-V6` 在新网络可用。其他用户选择自己的已确认 IPv4 节点即可。

提供商的 `profiles.yaml` 可能包含节点入口域名的专属 DNS，必须保留。这与应用侧的 DNS 分流不是同一件事。随意换成公共 DNS 可能让节点入口得到诱饵地址。依据为 `internal/config/profiledns.go` 和 `internal/core/profile_dns.go`。

准备完成后，在 CFW 界面正常关闭系统代理、TUN、已确认的服务模式与自启动，再退出 GUI。检查核心和关联服务是否仍接管，确认后才停止。不要同时运行两个自动接管默认路由的 TUN，也不要让 vender 的上游借道 CFW 后把结果误认成独立成功。

检查旧 PAC、环境变量与独立代理扩展是否还指向 CFW 端口，只修改已备份且确属 CFW 的项。WinINET、WinHTTP、浏览器和环境变量不是同一套代理。最终主要用 TUN 时通常不需要 `vender proxy on`。当前源码的 `proxy off` 会尝试恢复持久备份，遇到外部更改会拒绝覆盖，不能机械理解成“关闭所有代理”。见 `cmd/vender/winproxy.go`、`cmd/vender/proxy_windows.go`。

## 先验证本地代理，再开启 TUN

在同一日常用户的管理员 PowerShell 前台运行 `& $v daemon`，保留错误输出。另开同身份窗口，重新定义 `$bin`、`$v`、`$sb`、`$run` 等变量，再执行：

```powershell
& $v status
& $v rules list
& $sb check -c (Join-Path $run 'singbox.json')
& "$env:SystemRoot\System32\curl.exe" --noproxy 'localhost,127.0.0.1' --proxy http://127.0.0.1:7897 --connect-timeout 10 --max-time 30 -I 'https://www.bing.com/'
```

另选一个用户常用的境外 HTTPS 目标，把上条 URL 换成它验证代理路径。这里显式限定 no-proxy 列表，避免继承环境里的通配绕过，也避免旧 Windows PowerShell 丢弃空字符串参数。可再用 `--proxy socks5h://127.0.0.1:7897` 验证 SOCKS5。系统缺少 `curl.exe` 时先检查 Windows 组件来源，不要运行当前目录中同名文件。证书检查必须保持开启，禁止 `-k`、`--insecure` 或安装拦截证书来凑成功。

`status` 成功只说明受管内核活着并监听，不证明远端节点通畅。`sing-box check` 只检查配置，不证明真实出口。生成的 `singbox.json` 含凭据，检查可在本地执行，不要复制上传。上述含义分别见 `cmd/vender/status.go` 与 `internal/core/engine.go`。

本地代理确实可用后，在管理员 CLI 执行 `& $v tun on`、`& $v tun status`，检查网卡、默认路由、DNS 和不配置显式代理的应用。只提升 CLI 不能给已运行的普通权限 daemon 补权限，必须正常停止旧 daemon 后，从提升窗口重新启动。始终保持 `rule`，TUN 接管范围与 `global` 路由模式不是一回事。

TUN 默认地址可覆盖双栈，手写成只有 IPv4 地址则不会自动接管 IPv6。不要为排障全局禁用 IPv6。`strict_route` 也不是程序退出后继续保护网络的防火墙开关。先使用简单、已确认的网络与单节点，再验证多网卡、休眠唤醒和 Wi-Fi 重连。

## 人工验收规则和 DNS

我们需要同时看页面和路径，而不是只看浏览器打开了一个缓存页。新建连接，结合 TUI 日志、生成规则和内核连接记录判断 direct 或 proxy。浏览器 DoH、安全 DNS、扩展代理、旧连接与缓存都会影响观察；临时调整测试浏览器前记下原值，测试后恢复。

| 验收对象 | 应看到的结果与边界 |
|---|---|
| Bing 普通搜索 | `www.bing.com`、`cn.bing.com` 等入口 direct，实际搜索可用 |
| 图片与静态资源 | 允许列表内域名 direct，检查真实页面资源，不要求每个根 URL 可打开 |
| 升级域名 | `edgeservices.bing.com`、`sydney.bing.com`、`copilot.microsoft.com` 仍 proxy |
| Microsoft 重叠域名 | 例如 `download.microsoft.com`，A/AAAA 的策略为 FakeIP，与 proxy 路由一致 |
| 国内与内网 | 国内目标 direct，原有路由器、NAS 等服务不受损 |
| 常用境外目标 | 不依赖 CFW，经预期节点实际可用 |
| 网络恢复 | 重连与唤醒后仍可用，IPv4 节点不依赖未经证实的 IPv6 |

Bing 例外不是整个 `bing.com` 后缀，规则准确列表见 `internal/rules/rules.go`。同一 HTTPS 域名的不同 URL 路径无法在这里分开，所以共享 `www.bing.com` 的 AI 页面也会 direct。`global` 不采用这些目的地例外，不是这次安装的目标。

原现场访问 `https://s.bing.net/` 根路径遇到证书无效，不能单凭它认定路由失败，也不能保证另一台电脑复现。根路径不是静态资源健康检查协议，应检查真实页面请求、证书主机名和连接路径。任何证书错误都不允许通过关闭校验来验收。

AAAA 查询返回 NODATA 表示查询成功但没有该类型记录，不等于 DNS 服务不可达，更不自动证明代理失败。区分响应码、是否有 A 记录、域名策略与实际 IPv6 路径。DNS 分流在 `internal/rules/dns.go` 中按有效域名规则排序，非地址查询仍可能走国内解析器，并非完整防泄漏承诺。

内核本地 Clash API 可用于取证，但地址和 secret 应从本机运行配置读取并仅在本地使用，不粘贴到截图或命令历史。不要把 API 报告的目标地址当成出口证明。不会安全使用 API 时，优先保留脱敏日志和实际页面证据，不临时开放控制端口给局域网。

## 登录任务与静默启动

先完成前台 TUN 验证，再注册登录任务。确认配置里 TUN 已启用，从同一日常用户的管理员窗口执行。若使用自定义 `--config-dir`、`--run-dir`，所有 CLI、TUI 和任务都必须使用同一组绝对路径。

```powershell
& $v autostart on
if ($LASTEXITCODE -ne 0) { throw '自启动未成功，先核对错误' }
& $v autostart status --json
& "$env:SystemRoot\System32\schtasks.exe" /Query /TN '\vender\daemon' /XML
```

检查任务名称 `\vender\daemon`、日常用户 SID、LogonTrigger、`InteractiveToken`、`HighestAvailable`、绝对程序路径、工作目录与配置参数。`InteractiveToken` 表示用户已经登录后的交互会话，不是未登录即启动的系统服务。任务的 Hidden 属性也不保证控制台程序绝对无窗口。参见 `internal/autostart/windows.go`、[登录类型][ms-logontype]、[任务隐藏属性][ms-hidden]。

本提交的原生 autostart 优先选择同目录的 GUI 子系统 `vender-daemon.exe`。缺少 helper 时回退控制台，并在 detail 如实说明；存在但不是合法 GUI EXE 时拒绝注册。程序只检查文件类型和 PE 子系统，不会替人工核验供应链与提交来源，仍需自行比较主程序与 helper 的构建元数据和可信哈希。必须检查实际任务动作，不能只凭文件放在旁边就认定用了 helper。旧 Release 可能没有这项功能；回退控制台不是 TUN 权限失败，但不能写成已经实现静默。

可选的 `scripts/windows/setup.ps1` 负责用户 PATH、App Paths、静默任务转换与可选隐藏当前窗口，不下载程序、不安装节点配置，不停止或重启 daemon。脚本面向原生 Windows 11 x64，默认只读预演需要显式 `-DryRun`。在源码根目录执行下面的预演：

```powershell
$dailySid = 'S-1-5-21-REPLACE-WITH-RECORDED-DAILY-SID'
$backup = Join-Path $env:LOCALAPPDATA 'vender-install-backup-REPLACE-DATE'
.\scripts\windows\setup.ps1 -BinDir $bin -AddPath -RegisterAppPath -SilentTask -ExpectedUserSid $dailySid -BackupDir $backup -DryRun
```

将 `$dailySid` 替换成普通日常窗口实际记录的 SID，不要拿错误提升账户的 SID 自证正确。核对预演后去掉 `-DryRun` 执行；仅在明确想隐藏当前控制台时追加 `-HideCurrentWindow`。控制台附着与隐藏交给同目录的短生命周期 `hide-console.ps1` 子进程，不从调用者的 PowerShell 直接脱离控制台。复制安装脚本时必须连同 setup-lib.ps1 与 hide-console.ps1 一起保留。脚本与备份必须在稳定、私密位置保留，不能用全局绕过执行策略作为默认前提。

`-SilentTask` 要求已经存在并核验过的 vender 任务，当前进程必须是匹配日常用户的提升会话。它应保留 SID、运行级别、参数和触发器，拒绝不属于 vender 的目标。先在 TUN enabled 状态执行原生 `autostart on`，再转换静默任务，不能反过来让脚本猜测权限。隐藏方案应在创建时隐藏控制台，让内核继承；启动后才隐藏并不能保证不闪窗。

注册或转换任务不代表已经运行新任务。先在前台 daemon 窗口按 Ctrl+C 正常退出，确认不再由它提供服务，然后手动运行任务：

```powershell
& "$env:SystemRoot\System32\schtasks.exe" /Run /TN '\vender\daemon'
& $v status
& $v tun status
Get-ScheduledTaskInfo -TaskPath '\vender\' -TaskName 'daemon'
```

核对进程完整路径、父子关系、端口和 TUN。使用 helper 时可能多一层 helper 进程，但不能出现两组 daemon 和内核。任务状态或上次返回值不能代替真实联网验证。只有一个 TUI 窗口能打开也不能证明任务正确。

保存工作，取得电脑使用者明确同意后，再真正重启电脑。登录桌面后不要手动启动 daemon 或 TUI，先检查 `status`、任务、网卡与实际分流，再验证重连和唤醒。仅手动 `/Run` 成功不算重启验收；仅注销登录也不能写成已重启。若暂不能重启，明确记为待验收，保留 CFW。

## PATH、Win+R 和日常 TUI

`-AddPath` 和 `-RegisterAppPath` 只改当前用户，不应改系统 PATH。Windows 进程通常继承启动时的环境，旧 PowerShell 不会自动获得注册表中新 PATH。先新开普通终端验证，必要时在当前窗口显式合并刷新：

```powershell
$machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$env:Path = "$machinePath;$userPath"
Get-Command vender.exe -All
vender.exe version
```

上述刷新会替换本窗口临时 PATH，若有临时工具路径，先记录。还要核对 `Get-Command` 没找到同名旧程序。App Paths 是 Shell 查找机制的一部分，不等于给所有控制台加入 PATH。参见 [进程环境变量][ms-env] 和 [应用注册][ms-apppaths]。

原现场在旧的提升环境下仅注册 App Paths 时，ShellExecute 测试仍未按预期找到程序，并没有直接操作原生 Win+R 对话框完成成功验收。因此不承诺写入注册表后所有旧窗口和 Win+R 立即生效，也不把旧提升进程的失败等同于所有普通桌面环境都失败。优先用绝对路径验证，保存工作后重新登录再试；仍失败可建立明确指向稳定路径的快捷方式，不通过盲改系统 PATH 或结束 Explorer 来修复。

Win+R 可输入带引号的实际绝对路径加 `tui`，例如 `"C:\Users\你的用户名\AppData\Local\vender\bin\vender.exe" tui`。实际用户目录可能不是显示名，以 `$v` 为准。打开 CLI 没带子命令不等于打开 TUI，更不等于启动 daemon。

TUI 中 `1` 至 `4` 切换页面，`2` 是节点，`4` 是设置；`q` 或 Ctrl+C 只退出界面。`s`、`x`、`R` 是向 daemon 发送内核启动、停止、重启请求，不是顶层 CLI 子命令。`m` 会切换模式，误触后恢复 `rule`；设置页 `u` 会切换 TUN，排障时不要随手按。

订阅导入只转换支持的节点与有限的提供商 DNS，不会完整复刻 CFW 的策略组、脚本或远程控制界面。导入前核对跳过原因，不能把部分导入当作完整兼容。`profiles use` 设置本地分组，只有 `selected_server: auto` 时才限定自动故障切换池；指定节点不会因为切换分组自动换成别的节点。实现见 `cmd/vender/import.go`、`cmd/vender/profiles.go`、`internal/subscription/clash.go`。

订阅更新会增删节点，也可能让失效选择回到 auto 并重启受管内核，先备份再操作。`servers list --links` 会泄露凭据；即使新源码对部分 URL 脱敏，也不要直接分享 `profiles list/show`、导入输出或原配置。

## 故障时先判断失败层次

我们先确认运行的是哪一个文件、哪一个用户和哪一个目录，再判断监听、远端节点、DNS 或浏览器。不要一开始就重置网络。日常诊断优先使用 `$v`、`$sb` 和 Windows 系统目录中的绝对路径，避免当前目录同名工具混入。

| 现象 | 先检查什么 |
|---|---|
| 命令找不到 | `$v` 绝对路径能否运行，当前进程 PATH 是否过旧，有没有同名旧 EXE |
| 界面写 M7 | 查看真实 `version`、Go 构建元数据和哈希，不凭里程碑推断回退 |
| daemon 连接不上 | 同一 SID、相同配置与运行目录、锁与真实进程，别删除活跃锁或 token |
| 7897 被占用 | `Get-NetTCPConnection -LocalPort 7897` 后按 PID 查完整路径，不按名称强杀 |
| status 成功但无网 | 节点端口、协议、TLS、提供商 DNS、实际上游路径，status 不保证远端 |
| TUN 权限不足 | daemon 本身是否提升，任务是否同 SID 的 HighestAvailable，不只看 CLI |
| 登录后失败 | 任务 XML、绝对路径、工作目录、DLL、配置路径、安全日志和真实登录触发 |
| DLL 或驱动错误 | 官方 ZIP 架构、完整内容、哈希与安全软件记录，不从 DLL 网站补文件 |
| 浏览器和 curl 不同 | DoH、扩展、独立代理、缓存、长连接和证书，不能关闭 TLS 校验 |
| TUI 的 t 或 T 没有延迟结果 | 这是直接 ICMP 探测，可能被网络或节点防火墙拒绝；先看权限、物理接口与提供商 DNS，不能把无 ICMP 回包等同于代理协议失败，参见 DIRECT-PING.md |

采集进程时可使用 `Get-CimInstance Win32_Process`，仅筛选已经确认的完整路径与关联 PID。命令行可能含敏感参数，分享前脱敏。查看 TUI 日志时同样注意节点入口与域名隐私，不把生成配置上传求助。

不存在顶层 `vender start`、`vender stop`、`vender restart`。前台 daemon 用 Ctrl+C；TUI 控制的是内核；任务启动用已核验的 `/Run`。任务移除不会自动停止现有 daemon，修改脚本也不会替你重启它。文件修改后还需核实 status 描述的是已应用状态还是磁盘待应用设置。

禁止为排障关闭 TLS 校验、盲用 `proxy off --force`、执行全局 winsock/IP reset、清空所有路由或批量删除网卡。企业策略、公司 VPN 和不明服务不在清理范围。遇到这些因素先保留证据，联系对应管理员。

## 离线回滚与最后卸载

把这一段和旧 CFW 启动路径提前打印或存成本地文件。回滚不依赖订阅在线刷新、下载新工具或 AI 指导。CFW 的已验证配置与安装材料必须仍在。

若 vender 还能响应，先执行 `& $v tun off`。前台实例用 Ctrl+C；任务实例在任务仍存在时可以使用系统 `schtasks.exe /End /TN '\vender\daemon'`，确认只针对本次任务。若必须按 PID 停止，先核对完整路径与父子关系，不能按 `sing-box` 名称批量杀进程。

确认本次内核已退出、TUN 接管解除，再执行 `& $v autostart off` 移除本次启动项。保留进程与网络证据；Job Object 是生命周期保护机制，不是每台电脑路由一定已恢复的证明。检查网卡、路由和联网，仅修正已确认属于这次操作的残留。

如果用了 setup 脚本，保留它实际输出的 BackupDir。恢复接口如下。先预演，再核对要恢复的记录；脚本恢复不替代停止 daemon，不会将已隐藏的当前窗口重新显示，也不负责恢复 CFW 配置：

```powershell
.\scripts\windows\setup.ps1 -ExpectedUserSid $dailySid -RestoreFrom $backup -DryRun
```

审核后去掉 `-DryRun`，以该提交脚本帮助与实际备份格式为准。恢复任务 XML 若重新启用了 vender 自启动，需要明确停用或移除后再回到 CFW。只恢复本次确实修改的 PATH、App Paths 和任务动作，保留之后追加的其他 PATH 项；遇到外部任务或 App Paths 改动会拒绝覆盖。备份说原值不存在时，删除本次新增值即可，缺失值本身不算错误，不应阻断余下恢复。每轮应用都会生成新备份，幂等调用可能没有实际修改；撤销早先变更时要使用对应那一轮的备份，而不是盲取最新目录。

若使用过 vender 系统代理，按持久备份恢复并检查内容；该备份可能仍指向原来的 CFW 端口，所以顺序与原状态要核对。存在外部更改时不要强制覆盖。再按盘点记录恢复 CFW 的本次受影响项，正常启动 CFW，确认没有 vender TUN 同时运行，验证国内、境外与局域网恢复。

只有前台验证、任务手动启动、实际重启登录、规则与网络恢复测试都完成，而且使用者明确同意后，才按原安装渠道卸载 CFW。清理仅针对已确认属于它的目录、服务与启动项。共享 Wintun/TAP 驱动、其他 VPN 网卡以及归属不明的条目不删除。备份继续保留到使用者确认不再需要回滚。

交付记录至少保留系统架构、日常 SID、源码完整提交、主程序/helper/内核版本与哈希、稳定目录、备份目录、任务实际动作和每项验收结论。未做真实重启就写“待重启验收”，未交付 helper 就写“控制台启动”，不要把计划功能写成已验证结果。

## 依据与官方参考

仓库依据均为相对根目录路径：`README.md`、`docs/WINDOWS-MIGRATION-HANDOFF.md`、`cmd/vender/root.go`、`cmd/vender/version.go`、`cmd/vender/autostart.go`、`cmd/vender/tun.go`、`cmd/vender/winproxy.go`、`internal/autostart/windows.go`、`internal/autostart/native_windows.go`、`internal/privatefs/protect_windows.go`、`internal/config/store.go`、`internal/core/render.go`、`internal/core/profile_dns.go`、`internal/core/process_windows.go`、`internal/rules/rules.go`、`internal/rules/dns.go`、`internal/tui/model.go`、`scripts/build.sh`、`scripts/fetch-sing-box.sh`、`scripts/package.sh`。新 helper 与 setup 的最终实现应另核对 `cmd/vender-daemon`、`scripts/windows/setup.ps1` 及对应测试。

以下官方文档供核对 Windows 与 Go 的平台语义，不构成目标电脑已经验收的证据。操作步骤同时以仓库实现和本轮隔离测试为依据，执行时仍应结合官方现行页面和目标系统行为判断。

[go-download]: https://go.dev/dl/
[go-install]: https://go.dev/doc/install
[ms-arm]: https://learn.microsoft.com/en-us/windows/arm/apps-on-arm
[ms-uac]: https://learn.microsoft.com/en-us/windows/security/application-security/application-control/user-account-control/how-it-works
[ms-runlevel]: https://learn.microsoft.com/en-us/windows/win32/taskschd/principal-runlevel
[ms-logontype]: https://learn.microsoft.com/en-us/windows/win32/taskschd/principal-logontype
[ms-hidden]: https://learn.microsoft.com/en-us/windows/win32/taskschd/tasksettings-hidden
[ms-env]: https://learn.microsoft.com/en-us/windows/win32/procthread/environment-variables
[ms-apppaths]: https://learn.microsoft.com/en-us/windows/win32/shell/app-registration
[ms-acl]: https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/icacls
