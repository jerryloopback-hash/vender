# Windows 11 修复与实机验收

## 本轮源码修复

### 系统代理：真正保存与恢复有效设置

- 使用 WinINET per-connection API 读取/设置 **LAN 有效配置**，不再只修改 ProxyEnable。
- 备份包含代理开关/自动发现 flags、手动代理、bypass、PAC URL；开启期间禁用 PAC/自动发现，避免继续绕过本地代理。
- 原设置先写入当前用户的 `HKCU\Software\vender\SystemProxyBackupV1` 并 flush，然后才修改系统设置。
- 备份不随 profile/run-dir 改变；同一用户的操作有文件锁，重复 `on` 不覆盖最初原设置。
- 修改或刷新失败时尝试回滚；无法恢复时保留备份。`off` 不要求 daemon 正常或 config.yaml 可读。
- 外部软件改过代理时，普通 `off` 拒绝覆盖；只有显式 `vender proxy off --force` 才恢复原备份。
- 没有备份时 `off` 为无操作，不会擅自禁用其他软件的代理。
- 不修改 WinHTTP、拨号连接配置；恢复的是有效设置，不保证注册表字节/值缺失状态完全相同。

**旧版迁移限制：** 如果旧版已覆盖了原代理且从未备份，无法追溯恢复丢失的旧值。请先在 Windows 设置中手动确认代理/PAC，再首次使用新版 `proxy on`。

### 子进程生命周期

- daemon 启动内核前先把自身纳入 `KILL_ON_JOB_CLOSE` Job Object；随后创建的子进程原子继承约束，避免 Start 后再 Assign 的孤儿窗口。
- Job handle 不继承给子进程，保留到 daemon 进程结束。无法建立约束则拒绝启动，不静默退化。
- Windows 11 支持嵌套 Job；实际 Task Scheduler/其他宿主环境仍需验证。
- 正常停止先尝试向独立进程组发 CTRL_BREAK，给内核清理机会；没有共享控制台或超时则强制终止。
- Job Object 保证进程生命周期，不保证强制终止后的驱动、路由、DNS 一定彻底恢复，这仍是实机验收项。

### 自启、中文路径和权限

- 计划任务 trigger/principal 显式绑定当前用户 SID；TUN 继续使用 HighestAvailable。
- TUN 自启注册要求当前进程确实已提权；不会把标准用户的 HighestAvailable 误认为可获得管理员权限。
- daemon 失败按 1 分钟间隔重试，最多 5 次；与内核自身的限速恢复分开。
- CLI/TUI 注册自启时保留 config-dir/run-dir；Windows 命令行引用覆盖空格、中文、引号和尾部反斜杠。
- 设置任务 working directory；Task 创建成功清理旧 Run 值；已有 task 更新失败时不安装冲突的 Run 回退项。
- 删除 Run 值使用 Registry API 的错误码；删除 task 使用 Scheduler COM HRESULT，不依赖中文/英文错误句子。
- UTF-16 输出正确解码，不再简单删除 NUL、破坏中文用户名与路径。
- schtasks/reg/PowerShell 从 Windows 系统目录解析，DLL 使用 System32 安全加载，不依赖工作目录中的同名文件。
- privatefs 为 Windows 配置/运行目录及既有敏感文件设置受保护的 DACL，仅当前用户 SID 与 SYSTEM 完全访问；原子写入的临时文件继承此限制。
- Windows 网卡/TUN 名允许空格和中文；Linux 仍保留其名称限制。
- 测试中区分 POSIX mode 与 Windows DACL，增加真实 Store 文件 ACL 检查；golden 比较仅归一化路径分隔符，不修改生产输出。

## 测试与边界

Linux 可执行的策略回归覆盖：原代理/PAC 恢复、重复 on/off、备份写入失败、部分修改/刷新失败、外部冲突与强制恢复、启动参数引用、自启回退冲突和 UTF-16 中文。

Windows 专属测试已加入：

- `cmd/vender/proxy_native_windows_test.go`：只读 WinINET 调用、原生结构体布局；不修改真实系统代理。
- `internal/core/process_windows_test.go`：受控 helper 父进程退出，验证子进程随 Job 终止；不使用真实内核/TUN。
- `internal/privatefs/protect_windows_test.go`：临时目录/文件的 Windows DACL。
- `internal/autostart/native_windows_test.go`：原生参数解析、Scheduler COM 只解析 XML，不注册真实任务。

`.github/workflows/test.yml` 提供 Linux/Windows CI。Windows hosted runner 也不是 Win11 桌面实机；本轮没有提交/推送工作流或远程执行 CI。
本机只能交叉编译及执行 Linux 测试，不能宣称上述 Windows 原生测试已经运行通过。

本轮本机最终结果：Linux `go test -race -count=1 ./...` 通过；Linux 与 Windows amd64 的 `go vet ./...` 通过；Windows amd64 测试编译、CGO 禁用的程序构建通过。
`GOOS=windows go test -exec=/bin/true` 只算编译检查，不计为 Windows 运行测试。

调试包目录：`dist/windows-debug/`，包含 vender、固定版 sing-box、上游附带的 DLL、许可证和本文档；不包含任何节点配置或凭据。它不是正式 Release。

## Win11 实机验收顺序

1. **先用专门目录，不覆盖常用代理。** 解压 Windows amd64 包；迁移 config/servers/profiles/rules 时检查路径和秘密文件权限。删除或替换 Linux 的 engine.binary、bind_interface 等主机相关设置。
2. **显式代理。** TUN 关闭，普通用户启动 daemon，另开终端测试 `curl.exe --proxy http://127.0.0.1:7897 https://www.cloudflare.com/cdn-cgi/trace`；再测 SOCKS5 远端 DNS、代表节点及 UDP。
3. **系统代理恢复。** 记下原手动/PAC/自动发现设置；运行 on → on → off，检查恢复；daemon 不在时也测 off。另用 Windows 设置修改代理，再确认普通 off 拒绝覆盖、force 仅在明确需要时恢复。
4. **管理员 TUN。** 同一账号提权，启用双栈 TUN，验证 DNS、TCP/UDP、真实 IPv6 目的地址、LAN；验证普通用户 TUI 可以连接管理员 daemon。不要把“只有 IPv4 成功”当成双栈通过。
5. **异常退出。** 在测试窗口结束 daemon/内核，确认进程、监听、TUN、DNS、路由清理或限速恢复。停止代理前先关闭系统代理，避免把残留系统代理误判为 TUN 断网。
6. **自启。** 使用带空格/中文的显式配置和运行目录注册；检查任务用户、参数、权限与失败重试；注销登录、重启、断网启动后恢复网络。
7. **共存与恢复。** 睡眠/唤醒、切 Wi-Fi、IPv6 有/无、WSL/Hyper-V/其他 VPN、关闭 autostart、卸载后的网络状态。

### 仍需注意

- 系统代理仍是显式 on/off 管理，不是 daemon 死亡时自动改回旧设置的独立 watchdog。daemon 不在时可运行 `proxy off` 恢复备份；计划任务恢复也有等待时间。
- `strict_route` 仍不是永久 kill switch；驱动依赖、防火墙/杀软、系统休眠及接口切换没有在 Ubuntu 上被“模拟验收”。
- 便携目录若同步到其他 Windows 用户，不应复制本机系统代理备份；备份现存 HKCU 中而不是节点配置内。
- 任务名仍为机器上固定的 `\vender\daemon`，面向个人单用户使用；多 Windows 账号同时安装需单独设计 task 命名/迁移。

参考原生 API 文档（经 Tavily 核对）：
- https://learn.microsoft.com/en-us/windows/win32/api/wininet/ns-wininet-internet_per_conn_option_listw
- https://learn.microsoft.com/en-us/windows/win32/api/wininet/ns-wininet-internet_per_conn_optionw
- https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects
