# CDK EdgeOne 配置

2026-11-05 已发布并完成两域业务 DNS 切换。站点 `zone-3x0dhdux0fcs` 使用全球可用区，两个域名均 HTTPS 回源 `121.40.64.63:443`，回源 Host 使用各自加速域名。

| 阿里云 DNS 主机记录 | 类型 | 记录值 | 用途 |
| --- | --- | --- | --- |
| `cdk` | CNAME | `cdk.lanting.live.eo.dnse1.com` | 充值站访问入口 |
| `cdk-admin` | CNAME | `cdk-admin.lanting.live.eo.dnse1.com` | 管理站访问入口 |
| `_dnsauth.cdk` | CNAME | `cdk.lanting.live.eoacme0.com` | 边缘证书验证与续期 |
| `_dnsauth.cdk-admin` | CNAME | `cdk-admin.lanting.live.eoacme0.com` | 边缘证书验证与续期 |

四条记录均为默认线路、启用、TTL 600 秒。委派记录需持续保留。两域边缘免费证书已部署，控制台显示到期时间 2027-02-02 07:59:59（UTC+8），到期前 15 天自动更新。这是浏览器到 EdgeOne 的证书；源站证书仍由 SG Certbot 签发并同步至 Ali，详见 [TLS 配置](../tls/README.md)。

最初委派验证失败时，权威记录已经正确，递归 DNS 曾对 CNAME 返回 NXDOMAIN，而 TXT 查询已能跟随新委派；缓存恢复一致后重试验证通过。无需修改 NS 或重复创建同名记录。两地检查见 [委派验证报告](../EDGE-DELEGATION-VALIDATION-20261105.json)。

## 规则顺序和边界

当前控制台规则从上向下执行，下面的规则优先级更高；最终顺序：

1. Next 静态资源缓存（主站已有规则）
2. 错误响应不缓存（已有规则）
3. CDK 回源配置
4. CDK 管理站配置
5. CDK 版本化静态资源缓存

第 3、4 条按各自 Host 匹配，节点不缓存，并以“设置”覆写 `X-CDK-Origin-Auth` 和 `X-CDK-Client-IP`。前者使用每域独立私有回源值，后者为 `${http.request.ip}`。这些头仅用于 Ali 验证访客 IP 的可信来源；应用登录和业务鉴权保持独立。秘密不存入 Git。

第 5 条的非敏感导入配置为 [cdk-static-cache-rule.json](cdk-static-cache-rule.json)。仅两域 `/assets/` 下带 8 位版本后缀的 JS/CSS 遵循源站缓存头；无 Cache-Control 时不缓存，浏览器缓存也遵循源站。源站仅允许 GET/HEAD 的 200/206/304 资产返回一年 immutable，其他方法及错误仍 no-store。

控制台不允许节点缓存操作与请求方法条件组合，因此方法限制留在源站。智能压缩操作也不接受此路径正则；实际 gzip 在源站静态 location 开启，已通过 EdgeOne 公网验证压缩交付及解压后的内容一致性，无需扩大压缩规则到动态接口。

两站同一节点重复请求静态资产均出现 MISS 后 HIT，Age 正常增加；HTML、公开 API 和缺失资产重复请求始终 no-store/MISS。回源认证、真实 IP 覆写及未登录鉴权也已分别验证。

配置语法参考：[腾讯云配置组语法](https://cloud.tencent.com/document/product/1552/125342)、[条件表达式](https://cloud.tencent.com/document/product/1552/125343)。控制台解析器要求 FollowOrigin 同时填写 DefaultCacheStrategy=off 和 DefaultCacheTime=0，尽管 DefaultCache=off 时这两项不生效。

验收证据：

- [管理站公网缓存](../EDGEONE-ADMIN-PUBLIC-ACCEPTANCE-20261104T165420Z.json)
- [充值站公网缓存](../EDGEONE-CDK-PUBLIC-ACCEPTANCE-20261104T165718Z.json)
- [充值站回源身份](../CDK-EDGE-INGRESS-VALIDATION-20261105.json)
- [两地公网与服务最终验收](../FINAL-PUBLIC-VALIDATION-20261105.json)
- [浏览器界面验收](../BROWSER-ACCEPTANCE-20261105.json)
