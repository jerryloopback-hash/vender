# 给 Windows 本地 agent 的执行 Prompt

将下面代码块中的全文粘贴给 Windows agent。先用目标日常登录账户打开管理员终端，再从该终端启动 agent；不要切换成另一个管理员账户后误装到错误用户目录。

```text
请在这台 Windows 上执行 Clash for Windows（CFW）→ vender 的完整迁移。

已经确认的需求：
1. 最终由 vender daemon + 匹配的 sing-box，以 rule + TUN 透明接管网络，完全替代 CFW。
2. 用户登录桌面后自动运行，不需要手动打开 vender/TUI；不要求登录前运行的 Windows 服务。
3. Bing 主要用于普通搜索：搜索入口和常用图片/静态域名直连，升级服务域名保留代理。
4. 采用“先盘点备份、先验证、后彻底卸载 CFW”的顺序。不能先卸载再测试。

仓库：https://github.com/jerryloopback-hash/vender
开始前必须阅读仓库中的：
- docs/WINDOWS-MIGRATION-HANDOFF.md
- README.md 的 Windows、TUN、autostart、配置部分
- 相关 Windows 自启动和代理实现，以源码/现场实测纠正文档歧义。

如尚无源码，先克隆仓库，或使用本机已存在的工作副本；不要覆盖未知用户修改。
源码必须包含 8ce785f（Bing 精细直连和 DNS/路由一致性修复）或它的后继版本。
不要假设旧 Release 包含修复。构建/部署应记录真实 commit 和版本。
目标 CPU 架构、Windows 版本、内核归档校验、管理员身份及日常用户 SID 都要检查。

执行范围已授权：盘点、备份、准备程序、迁移配置、停用 CFW、验证 vender、注册登录自启动，以及所有验收成功后的 CFW 卸载与已确认专属残留清理。
重启/注销前先通知用户保存工作并取得确认。不要在未经验证的情况下声称迁移完成。
本次执行不用重新询问已确认的总体方案；只在缺少私密材料、权限/账户不符、归属不明、会扩大破坏范围或需要用户重启时询问。

真实节点配置不在 Git。请用户通过私密渠道在本机提供 Linux ~/.config/vender/ 下的 servers.yaml、profiles.yaml、参考用 config.yaml，以及存在时的 rules.yaml。
不要索要用户把密钥/订阅贴进聊天；不要打印这些文件全文，不提交到 Git，不上传迁移日志到公开渠道。
不要复制 Linux .cache/vender、singbox.json、control.json、fakeip.db、锁/PID 或 SSH 私钥。
不要读取/公开 Linux DEBUG-HANDOFF.md；那是含密钥的旧本地文件，不是本次交接材料。

按交接文档阶段 A–E 执行，并维护仓库外、ACL 受限的备份和 migration-report.md：
A. CFW 保持运行，先只读盘点进程/服务/任务/启动项、WinINET/PAC、WinHTTP、环境变量、浏览器代理、网卡、路由和 DNS，完成可回滚备份。
B. 下载并校验正确 Windows 程序与内核，放在稳定目录；迁移配置，去除 Linux 路径/网卡名。先用 IPv4 节点（已知可能为 manual/JP-1），不要假设 Linux 正用的 JP-1-V6 在这台 Windows 可用。
C. 正常停用 CFW 系统代理、TUN、服务及自启动，但不卸载。先验证 vender 本地代理，再在管理员 daemon 上开启 TUN；不得双 TUN，不得用 global 替代 rule。保护 agent 自己的网络依赖，切换前备齐离线材料和恢复步骤。
D. 在目标用户管理员上下文注册 \vender\daemon 登录任务，核对 HighestAvailable、SID、绝对路径和启动参数；停止前台测试实例后单独验证任务能启动。autostart on 不能替代实际启动/重启验证。请用户批准重启，登录后不手动启动 daemon/TUI，验证联网和分流。
E. 只有功能及真实重启登录验收都通过后，才正常卸载 CFW 并清理已确认专属残留。共享 Wintun/TAP 驱动、其他 VPN、企业策略及归属不明项目不要自行删除。

验收需用命中规则/实际出口及真实请求证据，而不是只看网页能否打开：
- www.bing.com/cn.bing.com 命中 bing-search-direct → direct，普通搜索可用。
- 常用图片/静态资源可用。
- edgeservices.bing.com、sydney.bing.com、copilot.microsoft.com 保留 proxy（无需登录/购买）。
- download.microsoft.com 等 Microsoft/CN 重叠域名的 A/AAAA 使用 FakeIP，与 proxy 路由一致。
- 国内站点、内网、境外代理目标、DNS、网络重连和休眠唤醒正常。
- 只有一套实际代理接管，且登录后无需手动操作。
不可区分同一 HTTPS 域名的 URL 路径，不要承诺所有 AI 页面都能单独代理。

安全限制：
- 不编造 vender start/stop/restart 顶层 CLI；前台 daemon 用 Ctrl+C，任务/进程操作先核对路径/PID。
- 不盲用 proxy off --force，不全局清空代理/路由，不运行全局 Winsock/IP reset，不批量删除网卡、驱动或注册表匹配项。
- 不绕过 TLS 校验，不安装 MITM 证书，不关闭安全软件来掩盖问题，不从第三方 DLL 站补驱动。
- 失败时先按交接文档回滚，保留 CFW 的恢复能力并报告阻塞，不连续尝试破坏性网络改动。

先简洁报告只读盘点结果和下一步计划，然后执行。结束时提交脱敏报告：版本/路径、备份、CFW 清理项、自启动权限、验收表、实际重启结果、剩余风险及回滚方法。
```
