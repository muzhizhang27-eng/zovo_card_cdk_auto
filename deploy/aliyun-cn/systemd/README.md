# Ali CDK 资源和启动约束

2026-11-04 三个 service 及 `cdk.slice` 已安装、daemon-reload 和实际 systemd239 配置校验通过。安装时三个业务 unit 均 loaded/inactive/dead/MainPID=0/Result=success，未 enable/start。

| 服务 | MemoryMax | GOMEMLIMIT | CPUQuota | 身份与状态目录 |
| --- | --- | --- | --- | --- |
| cdk-recharge | 192M | 144MiB | 50% | cdk-recharge；`/var/lib/cdk-recharge` |
| cdk-admin | 192M | 144MiB | 50% | cdk-admin；`/var/lib/cdk-admin` |
| cdk-lookup | 96M | 64MiB | 50% | user cdk-admin / group cdk-recharge；只读 admin DB，经 `/run/cdk-lookup/lookup.sock` 服务 |

统一 `cdk.slice` MemoryMax=480M。三进程 GOMAXPROCS=1、TimeoutStopSec=330s；CPUWeight=25，并设置 CPUShares=256。现场 Ali 为 systemd239 + cgroup v1，因此 CPU shares 生效；CPUWeight 为 v2 兼容配置。主站服务未迁入该 slice。

三个 ExecStart 都以 `/usr/bin/env` 明确赋值 `CARD_API_PROXY_URL=http://127.0.0.1:18784`；主两个另赋值 `GPTCHECK_PROXY_URL=http://127.0.0.1:18785`，并明确赋值 Go 资源变量。这让 copied EnvironmentFile 无法意外覆盖上述强制配置，env 最后 exec 真正 binary，不引入持久 shell 或改变主进程信号路径。私有配置仍由原 EnvironmentFile 路径加载；不在本目录保存凭据。

三 unit 保留 SG 现有文件系统和进程沙箱。通过 Wants/After 安排所需 SSH tunnel 的启动顺序；应用的专用 transport 必须代理失败即报错，不允许直连回退。After 本身不保证远端可用，主任务启动前需检查两个回环代理实际通路。

先安装候选 Nginx 维护 marker、验证本地代理与数据库，再由主迁移任务指向新 current 链接并启动/验收；未完成最终冻结、复制和核对前不能解除维护或允许双写。SG 三业务 unit 的停止和最终兼容转发也由主任务协调。
