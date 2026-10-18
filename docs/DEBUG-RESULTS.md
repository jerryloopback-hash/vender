# vender 调试结果（2026-10-17）

## 结论

**架构方向合理，但原版本的恢复能力、状态可信度和双栈完整性不足。**
本轮已修复并部署主要 Ubuntu 可靠性问题，自建节点与机场 SS、VMess 的 TCP 代理可用。
Windows 11 能完成构建，但尚不能称为迁移验收通过，尤其系统代理恢复和进程退出管理仍有缺口。

本报告不包含凭据、订阅 URL 或真实出口 IP。不应把一次连通性检查当作长期稳定性或所有机场节点可用的保证。

## 运行基线与最终状态

- 源码基线：`a234b94`；原安装二进制的 Go 构建记录为 `50f00ddb…`，与 HEAD 的差异只有 `.gitignore`，不是功能版本不一致。
- 交接描述的 `manual + auto` 已过时：实际是 manual 自动池 + 固定机场 SS 节点。源码明确允许跨 profile 固定选择；profile 只约束 auto 池。
- 最终安装版本：`debug-20261017 / a234b94-dirty`，不是已发布的新版本；已安装文件与测试候选文件哈希一致。
- systemd 系统服务正常，以原用户及原网络 capabilities 运行。没有更改系统单元或 VPS 服务配置。
- 保留原机场固定节点选择、rule 模式、mixed 监听端口及原规则。
- 本机 TUN 已增加 IPv6 地址，真实 IPv6 目的路由指向 `vender0 / table 2022`。
- `fakeip.db` 已启用持久化，Linux 文件权限为 `0600`；配置及运行目录仍为 `0700`。
- 生产变更设置了独立的 180 秒离线回滚；验证后已解除，最终无遗留回滚定时器。
- 初次部署脚本未等待 Type=simple 服务完全就绪就查询端点，触发保护性恢复；增加就绪等待后再次部署通过。此为测试脚本问题，不算 vender 启动失败。

## 已确认并修复的问题

| 编号 | 严重度 | 原问题与证据 | 修复 |
|---|---|---|---|
| F1 | 高 | TUN 仅配置 IPv4，本机原生 IPv6 请求实际出口为大陆，绕过代理 | 默认未指定地址时提供双栈地址；显式 IPv4-only 配置保留但提示；本机现有配置按用户选择迁移为双栈 |
| F2 | 高 | 内核被终止后，daemon 继续存在，systemd 不会因此重启代理 | daemon 对意外退出做退避恢复；最多 5 次/5 分钟；手动 stop、Close 和新操作使旧恢复失效；使用最后实际应用的配置 |
| F3 | 高 | restart 遇坏配置先停止工作内核；新配置启动失败没有恢复 | 停机前纯渲染与真实 `sing-box check`；启动失败尝试恢复旧内核；仅成功启动后发布新运行快照 |
| F4 | 中 | 内核已死、同端口被无关 TCP 监听占用时，CLI status 仍返回 0；磁盘配置被当作运行状态 | 查询经 token 认证的 daemon 状态；停止/异常的受管内核返回非零；坏磁盘编辑不妨碍查询旧内核真实状态 |
| F5 | 高 | FakeIP 分配不持久化；实测重启后 beta 域名从 `.3` 变 `.2`，可能与应用旧缓存冲突 | daemon rule 配置开启 FakeIP 持久化；真实内核回归验证重启后映射不变；缓存文件创建/修复为 `0600` |
| F6 | 中 | `bind_interface` 未应用于国内上游 DNS；与已有 TUN 共存时，节点域名查询被另一个内核拦截并返回 FakeIP | 将显式网卡绑定同步到直连网络 DNS，不绑定本地 FakeIP 或已 detour 的代理 DNS |
| F7 | 中 | 自启单测默认触及 `/etc/systemd/system`，在有生产服务的机器失败，甚至尝试删除真实服务文件 | 全部相关 fixture 使用临时系统单元目录及显式模拟 euid；本次真实文件未被删除 |
| F8 | 中 | 配置校验声称拒绝 wildcard，但实际接受 `0.0.0.0` / `::`，可暴露未认证 mixed 代理 | 拒绝未指定地址及其 IPv4-mapped 形式，增加回归测试 |

文档还修正了 mixed 栈 TCP/UDP 描述颠倒、TUN 地址默认值、rule-mode DNS 描述，以及把 strict_route 误称永久 kill switch 的问题。

### 配置恢复的边界

恢复旧内核不等于跨多个 YAML 文件的完整事务。失败的新设置可能仍留在磁盘，运行状态会保留旧值并给出错误，必须修正待应用设置。
本轮没有引入完整的多进程配置锁或跨文件事务，不能宣称这两类问题已经解决。

## 实测矩阵

所有外部测试均设超时和流量上限；未全量遍历机场测速，也未做大流量压测。
隔离实例使用独立配置、运行目录、监听端口，关闭 TUN，并绑定物理出口以避免套娃。

| 测试 | 结果 | 限定 |
|---|---|---|
| 自建 REALITY IPv4 入口 | HTTP CONNECT、SOCKS5 远端解析均通过 | 出口为日本；入口地址族与 VPS 访问网站的出口地址族是不同概念 |
| 自建 REALITY IPv6 入口 | 同上，通过 | 当前主机具备 IPv6 |
| 自建 Hy2 IPv4 / IPv6 入口 | 两种入口的 HTTP、SOCKS5 远端解析均通过 | 未重测交接中的吞吐数字 |
| 机场日本 SS / 香港 SS | HTTP、SOCKS5 远端解析均通过 | 带供应商 DNS 策略；不代表其余节点全可用 |
| 机场两个日本下载专用 VMess | 失败 | 修正隔离环境 DNS 捕获后仍是入口 TCP 超时；直接运行 sing-box 对照同样失败，未归罪于 VMess 协议实现 |
| 机场香港 VMess S09（TCP）/ S10（WS） | HTTP、SOCKS5 远端解析均通过 | 出口香港，证明本轮失败不是 vender 全部 VMess 不可用 |
| auto：坏节点 + 可用自建 IPv6 节点 | 通过 | 内核选择可用成员，HTTP/SOCKS 成功；不是完整的长期故障转移压测 |
| 自建 REALITY / Hy2 的 SOCKS5 UDP | STUN Binding 成功 | 验证了实际 UDP 转发，不仅是 Hy2 自身使用 UDP 传输 |
| 机场抽测日本 SS 的 SOCKS5 UDP | STUN 超时 | 不能宣称该机场节点支持可用 UDP；仍需区分机场限制、路径与目的端策略 |
| 生产 IPv4 TUN | 通过 | 出口日本 |
| 生产 IPv6 TUN | 通过 | `curl -6` 及指定真实 IPv6 目的地址请求均走日本出口；路由也确认进入 TUN |
| 内核崩溃恢复 | 隔离真实内核通过 | 3 秒观察时已恢复；未在生产故意杀内核 |
| 手动 stop 不反弹 | 单测、隔离真实内核通过 | 无关监听不能骗过 status，退出码为 1 |
| 坏 YAML / 内核拒绝的 REALITY 参数 | 隔离真实内核通过 | restart 返回失败，原 direct 内核仍健康 |
| FakeIP 重启保持映射 | 真实内核本地 DNS 回归通过 | `.invalid` 域名，无外部 DNS 或节点依赖 |
| VPS 服务只读核验 | xray、hysteria-server 均 active | SSH 使用现有密钥；没有更改远端服务 |

补充香港正常用途的 VMess 节点后，两种传输均成功；日本下载专用入口失败应单独处理，不能据此判定整个协议实现有误。

## 自动化结果

- `VENDER_TEST_SINGBOX=… go test -race -count=1 ./...`：最终代码所有包通过，包含实际内核配置校验和新增 FakeIP 重启回归。
- `go vet ./...`：通过。
- `git diff --check`：通过。
- Linux / Windows amd64 构建：通过。
- `GOOS=windows GOARCH=amd64 go test -exec=/bin/true ./...`：**仅编译检查**，没有执行 Windows 测试；不能当作 Windows 实测通过。
- 原来缓存命中的绿色结果掩盖了本机自启测试不隔离的问题；最终验证均取消缓存。

新增回归集中于：`internal/control/recovery_test.go`、`internal/config/tun_debug_test.go`、`internal/core/dns_interface_test.go`、`internal/core/fakeip_restart_test.go` 及 CLI daemon 集成测试。

## Windows 11：迁移前的主要缺口

> 以下为本轮最初审查快照。后续源码修复及仍待实机确认的边界见 [WINDOWS-HARDENING.md](WINDOWS-HARDENING.md)。

### 已由源码确认的实现边界

1. **系统代理没有真正恢复旧值。** `cmd/vender/winproxy.go` 写入 ProxyServer/ProxyOverride，off 只把 ProxyEnable 清零，没有保存原代理/PAC/绕过配置。README/帮助中的“restore previous setting”过度承诺。
2. **没有可靠的父死子清理机制。** `internal/core/process_windows.go` 仅 CREATE_NEW_PROCESS_GROUP 和 TerminateProcess，没有由 vender 配置的 Job Object/KILL_ON_JOB_CLOSE。daemon 异常结束时应实测是否留下内核、监听和 TUN。正常终止也不是 Unix 那样的 SIGTERM 优雅清理。
3. **计划任务不配置 daemon 失败重试。** 本轮内核恢复不覆盖 daemon 自身崩溃。需完善 Task Scheduler 的恢复策略，并核对用户、权限和配置路径。
4. **中文 Windows 的幂等删除易误报。** `winNotFound` 只识别英文 schtasks/reg 错误文本，中文系统缺少目标时可能把正常 off 当成失败。
5. **自启不保留自定义 config-dir/run-dir。** 注册的命令只有 `vender daemon`，便携/自定义路径部署需额外设计，不能直接照搬当前 Linux 文件路径。
6. **权限说明不能照搬 POSIX。** Windows 的 0600/0700 不等于用户专属 ACL；需要在实际 AppData 或便携目录检验继承权限、管理员 daemon 与普通 TUI 的互通。

### 必须实机确认

- 固定版本内核的 TUN 驱动加载、首次提权、防火墙、杀软拦截和驱动/路由/DNS 清理。没有证据支持简单断言“另放一个 wintun.dll 就够了”或“一定不需要额外驱动依赖”。
- 中文/空格路径、带空格的网卡别名、睡眠/唤醒、IPv6 断续、Wi-Fi 换网。
- 与 WSL/Hyper-V/ZeroTier/其他 VPN 共存；strict_route 的平台特定 DNS 限制。
- 推荐顺序：显式 HTTP/SOCKS → 系统代理（先做好旧设置恢复）→ 管理员 TUN → 登录自启 → 异常退出/重启 → 卸载恢复。

**当前建议：可准备 Win11 测试环境，但不建议把现有发布包当作已验收的日常全局代理直接迁入。**

## 其他未完成/待深入项

- 多进程 CLI 导入/删除/更新缺乏完整事务与锁；失败磁盘设置仍需人工修正。
- 控制服务在认证前使用未限长 JSON 解码、没有握手读超时，需要防止本地资源耗尽；本轮没有恶意压测生产 daemon。
- 订阅请求错误使用 Go URL error 包装，可能回显带 token 的 URL；应统一错误脱敏并以虚构凭据测试。
- VLESS TLS 的部分导入字段（如 Clash skip-cert-verify/ALPN）与渲染之间需补齐或明确拒绝，不应静默忽略。
- 全局模式下，没有专属 DNS 策略且入口为域名的节点，需要专门验证 bootstrap 依赖，避免依赖尚未建立的代理解析节点自身。
- rule DNS 的非 A/AAAA 查询仍使用国内上游，这是当前明确的隐私取舍，不是“完全无 DNS 泄露”；应用自带 DoH 又是另一条路径。
- 自建 Hy2 当前关闭证书校验；建议后续使用受验证证书或可靠证书公钥 pin，而非保持 insecure。
- 四个自建入口同属一台 VPS，并不构成服务器级容灾。
- 未完成长时间运行、换网/休眠、全量规则命中对照、TCP 吞吐、QUIC 应用和 Windows 实机验收。

## 私有证据与恢复

原始测试配置、日志、结果、旧二进制与离线恢复脚本保存在本机私有目录：
`/tmp/opencode/vender-debug-Xfhyqq/`（目录 `0700`）。这些文件可能包含真实节点凭据，**不要提交或上传**。

恢复脚本：`rollback.py`，恢复本轮开始时的原二进制与原 config.yaml，并重启服务；它也会恢复原先 IPv4-only 的地址配置，使用前应理解这一点。
该目录可能被系统清理，长期保留前应迁往用户自己的权限受限备份目录。本轮没有创建 git commit、推送仓库或发布 Release。

参考官方文档（通过 Tavily 核对；文档也含新版本内容，实际兼容性以本机固定 1.14.3 测试为准）：
- https://sing-box.sagernet.org/configuration/inbound/tun/
- https://sing-box.sagernet.org/configuration/experimental/cache-file/
