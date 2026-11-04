# CDK 两库最终冻结复制工具

2026-11-04：脚本已准备，本地 18 项合成测试通过；本工具尚未在生产执行。初次 online backup 演练已完成，但演练数据不可直接用于最终切换。原 recharge/admin 是两个独立 SQLite 库，lookup 读取 admin 库。

## 使用前提

停机、供应商在途核对、Nginx 维护与切流由主操作方完成。本工具不停止/启动服务，不发送业务请求，不改 DNS/EdgeOne/Nginx，不 reset-failed。`inactive` 本身不能证明充值已经排空。

旧 SG 二进制没有新增优雅停机机制。主操作方必须先让两域（包括 webhook）进入维护，核对已接受请求、连接、后台观察及供应商未决业务的最终状态，然后停止三个 CDK 服务。固定 sleep 或 `recharge_tasks=0` 都不足以代替这项确认。两机维护标记约定为 `/var/lib/cdk-migration/maintenance`，必须由实际启用的 Nginx 配置使用（最终以主操作方审核部署制品为准）。

脚本检查两个固定 SSH 别名 `sg-prod` 和 `aliyun-prod`；SG 使用 `-J aliyun-prod`，不转发或复制本机私钥。两机必须满足：

- `cdk-recharge.service`、`cdk-admin.service`、`cdk-lookup.service` 全部 loaded、inactive/dead、MainPID/ControlPID=0、Result=success；任一失败则拒绝。
- 维护标记存在，无进程持有两 DB、env、manifest 或 DB journal/WAL 文件。工具前后检查 systemd 冻结时间/启动计数和标记 inode 不变。
- Ali 两个 app unit 使用 `ExecStart=/usr/bin/env ... CARD_API_PROXY_URL=... GPTCHECK_PROXY_URL=... /opt/cdk-{role}/current/cdk-{role}`，lookup 同样显式设置 CARD 代理。工具解析 systemd 实际 ExecStart，仅接受这一个可执行文件形式，拒绝缺失/重复代理、额外命令或远端代理地址；三个 CARD 代理须相同。`env` 显式赋值覆盖 EnvironmentFile，所有代理均为有明确端口的 `http://127.0.0.1` 地址。
- Ali 现有数据库和私有文件应为初次演练配置：DB/manifest 服务账号所有、0600，env root:对应服务组、0640。现有 DB 的 WAL/SHM/journal sidecar 若仍存在则拒绝安装，须先查明原因。

## 执行方式

本地 Python 3.7+；SG Python 3.7+ 用于 online backup；Ali Python 3.6+ 可执行只读 fingerprint 和安装。脚本与 `sqlite-snapshot.py` 须保留在同目录。

在主操作方完成冻结后，先只读检查。当前仍在运行时该命令会拒绝，这是预期行为：

```sh
python3 -B deploy/aliyun-cn/final-snapshot-install.py \
  --maintenance-file /var/lib/cdk-migration/maintenance
```

随后主操作方执行最终复制，提供一个不存在的、位于 `/tmp` 或 `/var/tmp` 下的绝对私有目录：

```sh
python3 -B deploy/aliyun-cn/final-snapshot-install.py \
  --maintenance-file /var/lib/cdk-migration/maintenance \
  --execute --work-dir /tmp/cdk-final-20261104-unique
```

不能盲目重复使用同一个目录。任一非零退出都要保持维护、保持服务停止，并检查私有目录中的元数据。输出只含路径、计数、哈希、权限及阶段状态；不打印原始 SQLite 行、密钥、env 或 manifest 内容。SSH 原始 stderr 不输出。

## 实际动作

1. 先检查两机停稳门禁，尚未通过时不创建远端备份。
2. SG 在新 0700 `/var/tmp/cdk-final-source-*` 下生成两个 online backup。源连接只读，逐库要求 before=backup=after 完整类型摘要，且源 DB 原始字节和 5 个私有文件元数据全程不变。
3. 同步快照两库、原两个 env、admin manifest；私有 tar.gz 压缩包经本机新 0700 目录传到 Ali 新 0700 `/root/cdk-final-target-*`，文件均 0600。逐文件 SHA-256、archive SHA-256、schema、全表摘要均核对；只允许固定 6 个 archive 文件，不解包路径或链接。
4. Ali 持有独占 `/run/lock/cdk-final-copy.lock`，拒绝并发安装；停机状态下备份现有五个演练文件和原 owner/group/mode，再在目标所在目录准备新文件。源 env 的普通内容按最新字节同步；如果目标 env 含新增代理键，保留该键。源或目标 env 中任何代理值若与 Ali unit 不一致则拒绝。合并前后摘要记录在 `env_merges`，不输出值。
5. 每个文件通过同目录 `os.replace` 原子替换、fsync，最后核对所有文件字节/原权限与两库完整指纹。五个文件不是一个文件系统事务；若验证失败且服务仍停稳，工具恢复已经替换的演练文件。若门禁已经失效则绝不向运行中的服务回滚 DB，保持失败状态由操作方处理。
6. 再检查 SG 仍停止且源文件没有变化、Ali 三服务仍停止。成功只报告 `installed-services-still-stopped`，不自动启动/切流。

`source-report.json` 和 `target-result.json` 提供脱敏审计证据。Ali `rehearsal-backup` 留存替换前文件供故障恢复。只在所有核对通过后由主操作方启动唯一 Ali 写入端；SG 原三个应用继续停止，旧入口可按主计划兼容转发到 Ali。

Ali 开始新写入之后，回滚必须重新冻结 Ali，将两个最新 DB 同批回迁 SG。不得直接启动旧 SG DB 或恢复本工具留下的演练数据。

## 摘要口径与测试

`sqlite-snapshot.py` 将实际 SQLite NULL/TEXT/BLOB/INTEGER/REAL 分别编码，64 位整数不经浮点，REAL 用 IEEE-754 字节；每行哈希排序后逐表聚合，保留重复行，行顺序不影响结果。schema、index、trigger、sqlite_sequence、编码/user_version/application_id/auto_vacuum 都比较；quick_check/FK 检查必须通过。SQLite 客户端版本允许不同，内容/schema 不允许差异。新备份规范为 DELETE journal 单文件（原两库即 DELETE），源库不 checkpoint。

本地验收（只用临时合成 SQLite，不连接服务器/供应商）：

```sh
python3 -B -m unittest discover -s deploy/aliyun-cn -p 'test_*snapshot*.py' -v
```

覆盖 18 项：类型安全与数据保密、schema/触发器/自增、WAL 已提交数据、拒绝覆盖/内存上限、完整安装、代理保持与冲突拒绝、活动门禁拒绝不改目标、最终 hash 失败整组回滚、拒绝遗留 WAL、拒绝 archive 路径逃逸、拒绝并发安装、实际 ExecStart 代理解析及异常命令拒绝。

## 新停止逻辑只读评审

`backend/internal/lifecycle/http.go` 先 Shutdown HTTP，再等待 `background.Wait`；`plansync.Start` 在起 goroutine 前登记，且主 ctx 取消会结束周期任务。HTTP handler 内未纳入 background 的并发查询由 handler 自己的 WaitGroup/channel 等完，因此也在 HTTP drain 范围。后台 webhook/public 观察、Telegram 和更新均已登记；注册子任务发生在父任务 Done 前，没有发现 WaitGroup 自等待死锁或此次迁移下遗漏的业务 goroutine。

限制：300 秒总 drain 超时会返回错误并退出失败，不能当安全成功；unit 应设置 TimeoutStopSec=330s。旧 SG 未运行这份新增逻辑。自更新会 reexec，不服从正常 drain，但本次保留 `UPDATE_ENABLED=0`，不能在冻结中开启。最终是否可切换仍依赖在途外部业务核对；SQLite 一致性只能证明本地存储完整。

源码代理已在隔离网络/资源限制下报告 background/lifecycle 等相关包 race 测试通过；此评审未重复执行生产测试或改动 Go 文件。
