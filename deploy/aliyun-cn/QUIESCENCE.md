# SG 旧进程切换前的只读观测

`quiescence-observe.py` 不冻结、不停止服务、不写数据库、不请求任何上游接口。它只输出固定路由类别、HTTP method/status/完成时间、表计数/状态计数、文件时间与大小、三个 systemd PID 的 socket/队列和 I/O 计数。URL query、完整请求、IP、客户、卡密、session、API key 均不输出。

在 SG 上运行一份快照：

```sh
sudo python3 /home/ubuntu/cdk-cn-build-20261104/quiescence-observe.py
```

维护开启后可只观察冻结时间之后已完成的请求（时间必须带时区）：

```sh
sudo python3 /home/ubuntu/cdk-cn-build-20261104/quiescence-observe.py --since '2026-11-04T16:00:00Z'
```

`--since` 只过滤 access log，不改变数据库或进程。保留第一份默认 24 小时快照，随后重复采集并对比。工具不会自动给出安全停止结论；`automatic_safe_to_stop_verdict` 始终为 false。

## 已部署代码中的任务边界

基线是 SG `20261028-upstream-1434`，已与 deployed manifest 的 75 项 SHA256 核对。下表是请求链和持久化行为，不是统一停机倒计时。

| 路由／工作 | 是否跨 HTTP 请求继续 | 持久化／时限 |
|---|---|---|
| public preview | 本地不跨请求；上游返回持有人的 token | 成功响应后写 `cdk_session_bindings`。每次上游 HTTP 45 秒；遇明确 429 最多 3 次，但 limiter 排队与 Retry-After 没有总时限。 |
| public preflight | 本地不跨请求 | 写 session/token binding；同上 45 秒与 limiter。 |
| public redeem | **上游充值／开卡可在响应后继续**；本地仅转发 | 没有本地 order/pending/outbox 表；进度由远端订单和 redemption token 维持。每次 HTTP 45 秒，明确 429 可重试；丢响应不自动重试。不能用本地 tasks=0 证明远端无单。 |
| public recover-subscription | 上游操作可能在失联后仍有结果；本地仅转发 | 单次 HTTP 180 秒，无本地 recovery job 状态；不自动重试。 |
| admin POST cardplatform/cdks | 本地请求链同步；购买在失联时结果可能未知 | 先 GetPlans 45 秒，再 Issue 180 秒；非确定失败可再读最多 10 页库存，每页 45 秒，即约 **675 秒网络预算加数据库工作**。只有收到结果后落库存/成功审计，没有持久化“正在购买”记录。 |
| public exchange | 本地请求链同步；多个远端动作 | 列订单45秒、详情45秒、可选地区查询45秒、发码180秒、禁用旧码45秒，约 **360 秒网络预算加数据库工作**。新码成功后落库，最后写 exchange 日志；并非事务性远端 outbox。 |
| admin inventory sync | 同步请求，逐页本地写库存 | 最多50页，每页45秒，约 **2250 秒网络预算加数据库工作**。 |
| result/result-by-code | **本地 detached goroutine** | 请求返回后可 GET 一次订单详情（45秒），再写卡健康统计。本版仅本地拉黑，**不自动冻结远端卡**。数据库操作没有 ctx 总截止，不能把45秒当绝对后台寿命。 |
| provider webhook | **本地 detached goroutine** | 主流程先写 webhook、缓存状态；卡健康观察另起30秒 context。但当前观察主要是没有 QueryContext/ExecContext 的 DB 操作，所以30秒不是完整硬上限。两库当前 webhook secret 均未配置，观察记录为空。 |
| plansync | **常驻后台 goroutine** | 每180秒运行一次，HTTP共享30秒context，写 plan/product 缓存；DB循环没有完整总时限。缓存写入应与交易写入分开判断。 |
| billing check | 本地请求链同步，仅查询 | ChatGPT 每次30秒；通常2–4次，只有 sessionToken 的特殊流程可能5次（最多约150秒网络预算）。没有本地充值任务。当前 AccountHub 未配置。 |
| Unix lookup | 只读，随调用完成 | broker总context25秒，Web端30秒；批量查询由请求内 WaitGroup 等待。 |
| legacy recharge_tasks | 持久化人工申请，不是当前 Zovo 队列 | Create/Confirm/Approve/Reject 函数仍在源码，但当前 `setupRoutes` 没有注册这些旧路由；两个库均0不能替代对当前 BFF 的检查。 |
| Telegram／在线更新 | 可跨请求 | 本次两项均未启用；Telegram最长10秒HTTP。更新含 reexec，迁移期间必须保持 `UPDATE_ENABLED=0`。 |

旧 HTTP Server 的 `WriteTimeout=300s` 不是 handler 总执行 deadline；它不保证300秒后业务 goroutine自动结束。公开 limiter 在等待时还未进入 http.Client，因此45秒 client timeout不覆盖排队。不存在一个统一的“sleep 180/300/675 秒便可安全停机”结论。

## 采样与停止条件

1. 主操作员先确认两个域名、HTTP/HTTPS、管理员入口、Webhooks 全部经过已生效维护入口；marker存在仅是证据之一，脚本不验证 Nginx 配置是否真正生效。
2. 比较冻结前后 access log 的固定路由类别。日志只在请求完成时写入，不能仅凭“新日志为空”判断没有在途请求。近期出现购买、兑换、恢复、换码、同步或5xx未知结果时，不按时长直接放行。
3. 两个 Web PID 的 `direction=inbound_http` 连接必须结束，listener `listen_backlog_current` 应为0。若仍存在连接、非零收发队列、持续新增网络字节或后续完成日志，继续观测。LISTEN 的 backlog上限不是待发送字节。
4. lookup应只剩 listener，没有 `/run/cdk-lookup/lookup.sock` 的已连接业务 socket。`unix_state_counts` 中其它 ESTAB 常为 systemd 日志连接，不等同于业务请求。
5. 外部443的 ESTAB 可能是空闲连接池，也可能是正在等待响应。对比 socket inode、bytes_sent/received、last_payload_activity_age_ms 与缓存同步时间；不要只看队列0。默认连接池空闲超时90秒，而缓存同步周期180秒，可在真实外连清零的间隙判断；若不清零或有新payload，先解释原因。
6. 最后一次 result/webhook 请求完成之后，仍需覆盖后台观察尾部；45秒是已知上游读取时限，30秒是 webhook声明context，二者都不限制所有数据库操作。因此同时要求业务表计数/状态/最近时间持续稳定、DB/WAL/journal文件和PID写计数无未解释变动。plan/product缓存每180秒刷新属于已知后台写，不是充值pending。
7. 至少保留多次连续快照，跨度应覆盖上面实际出现过的后台工作；若最后业务请求早于观测很多小时且不存在HTTP/Unix业务连接，这比任意等待更强。仍不能据此证明远端所有订单终止：远端已受理作业可继续，必须保留 bindings、API配置、原域名和查询路径，让迁移后轮询恢复。
8. 人工确认观测条件后才停止旧三个服务。由 `final-snapshot-install.py` 再确认 PID/fd 已释放两个DB，做一致快照、安装与完整性检查。旧数据没有可证明排空的异常时，停在观察阶段，不强行完成切换。

以上工具与检查不调用真实客户订单API，不改变上游业务状态。
