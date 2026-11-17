# CDK 校验与账号预检耗时检测

仅观测 `POST /api/v1/public/cdk/preview` 和 `POST /api/v1/public/cdk/preflight`。现有请求体、响应体、HTTP 状态、45 秒上游超时及 429 重试策略保持不变。响应头新增 `X-CDK-Trace-ID`，用于关联同一次调用。

后端日志以 `[cdk-timing]` 开头，每条包含固定操作名、随机 trace ID、数值耗时、HTTP 状态和受控错误分类。不记录请求或响应内容、URL、卡密、Session、账号、设备标识或密钥。`business_code` 只提取上游顶层整数业务码。

## 三层记录

- `handler`：`decode_ms` 为解析本站请求；`config_ms` 为读取站点配置等准备；`upstream_total_ms` 为调用卡台的总时间；`postprocess_ms` 为绑定写库与响应写出；`total_ms` 为 handler 总耗时。
- `call`：同一次卡台逻辑调用的总时间、实际尝试次数和最终错误分类。
- `attempt`：每一次出站尝试的排队和网络阶段。现有 429 重试沿用同一 trace ID，`attempt` 从 1 递增。

## 出站阶段

| 字段 | 含义 |
| --- | --- |
| `queue_ms` | 本站共享限流器的等待时间，包括既有 429 退避 |
| `connection_ms` | 从开始出站请求到取得可用连接，包含连接池等待及新建连接 |
| `dns_ms` | Go 客户端实际执行的本地 DNS 查询 |
| `tcp_ms` | 本地客户端建立 TCP 连接的时间；代理模式下连接目标是本地隧道端口 |
| `proxy_connect_ms` | 已建立本地 TCP 后，等待 CONNECT 通道就绪的时间 |
| `tls_ms` | 与卡台端到端 TLS 握手时间 |
| `request_write_ms` | 取得连接后发送请求的时间 |
| `upstream_wait_ms` | 请求发送完毕至收到响应首字节的时间 |
| `response_headers_ms` | 首字节至收到完整响应头的时间 |
| `response_body_ms` | 读取响应体的时间 |
| `total_ms` | 本次尝试的总耗时 |
| `connection_reused` | 是否复用了已有连接 |
| `end_stage` | 正常完成或发生取消、超时时所在的阶段 |

DNS、TCP、CONNECT、TLS 是 `connection_ms` 的子项，不能和它重复相加。DNS 可能和并行拨号交错。零值可能意味着未发生该阶段或耗时不足精度，不代表远端不存在 DNS 查询。代理端 DNS 和远端 TCP 建连归入 CONNECT 阶段。

`upstream_wait_ms` 同时包含网络往返、卡台排队和卡台内部处理；本站无法进一步区分卡台内部的 Session 刷新、订阅检查和报价步骤，需要卡台配合提供追踪数据。客户端超时前尚未完成的阶段，会记录截至失败时已消耗的时间。

## 查看

```bash
journalctl -u cdk-recharge --since '10 minutes ago' --no-pager | grep '\[cdk-timing\]'
```

按同一 `trace_id` 合并 handler、call、attempt。业务成功需同时检查 HTTP 状态及业务码，HTTP 200 不一定表示预检成功。分析正常业务时排除验收探测对应的 trace ID，并分别统计新建连接、复用连接和失败请求，避免把冷连接或失败样本混成平均值。
