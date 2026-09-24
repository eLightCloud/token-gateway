---
status: planned
owner: Dev Team
last-reviewed: 2026-09-23
---

# NGINX 媒体转发入口配置

> 本文保留旧媒体转发方案的配置与验证记录。当前待实施方向已合并为[豆包视频双协议适配与 URL 交付方案](../80-dev/2026-09-23-豆包视频双协议适配与URL交付方案.md)：北向固定为 ModelArk，南向返回 URL 时按原生协议映射；本次不新增媒体字节转发，也不要求所有南向强制返回 URL。本文不作为本次南向适配的新部署要求。下文“已实现/已验证”仅属于旧方案记录，不能代表当前工作区；本次合并未变更生产入口，已有部署需清点存量依赖后再迁移。

## 背景

部署链路为“本服务（HK）→ NGINX → 上游”。渠道 API 地址指向 NGINX，任务创建和查询经其转发；但上游返回的媒体绝对 URL（如 `content.video_url`）由本服务直接访问，出站 IP 是 HK 本服务而非 NGINX，会被上游拒绝。渠道设置 `media_via_base_url` 启用后，本服务把每次无凭证媒体请求（含每一跳重定向）封装为对“渠道 API 地址 + 固定媒体入口”的请求，由 NGINX 出站访问媒体源。

## 网关侧合同

| 项目 | 值 |
| --- | --- |
| 媒体入口路径 | `/_newapi/media`（拼接在渠道 API 地址后，保留基址路径前缀） |
| 内部目标头 | `X-NewAPI-Media-URL`，值为当跳的原始媒体 URL |
| 方法 | 仅 GET/HEAD，无请求体 |
| 转发请求头 | Range、If-Range、If-None-Match、If-Modified-Since |
| 重定向 | NGINX 原样返回 3xx 与 Location，由本服务解析后经同一入口发起下一跳 |
| 内部头长度上限 | 网关按 8 KiB 拒绝超限目标（对应 NGINX 缓冲配置需协调，见下） |

可信的渠道入口可使用私网地址；本服务对原始媒体 URL 和每个重定向目标仍执行媒体 SSRF 校验。不要为使私网 NGINX 可达而关闭全局 SSRF 防护。

常量定义在 `controller/task_media_via_channel.go`；若两端任何一侧调整，必须同步修改。

## NGINX 参考配置

先在 `http` 块中声明以下 `map`。支持 DNS 名、IPv4、方括号 IPv6 及显式端口；authority 用于 HTTP Host，去掉端口/方括号的主机名用于 TLS。查询串不会参与主机名提取，userinfo、fragment、空白及非 HTTP(S) 目标被拒绝。目标域名、端口及解析后的 IP 仍须由部署方限制，语法校验不代替出站访问控制。

```nginx
map $http_x_newapi_media_url $media_target_authority {
    default "";
    "~^https?://(?<media_authority>(?:[A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(?::[0-9]+)?)(?:[/?][^\s#]*)?$" $media_authority;
}
map $media_target_authority $media_target_host {
    default "";
    "~^\[(?<media_ipv6>[0-9A-Fa-f:.]+)\](?::[0-9]+)?$" $media_ipv6;
    "~^(?<media_dns>[A-Za-z0-9.-]+)(?::[0-9]+)?$" $media_dns;
}
```

假设渠道 API 地址为 `https://gate.example.com`（可为共享 server 的独立 location，不影响现有 API 反代）：

```nginx
# 媒体转发入口：独立 location，与已有 API 反代规则分开。
# 仅本服务可调用（替换为实际网段/认证方案）。
location = /_newapi/media {
    # --- 访问控制（必须替换为本服务的实际来源地址） ---
    allow 127.0.0.1;        # 示例默认仅本机；替换为 HK 本服务的实际地址/网段
    deny all;
    # 当前宿主不注入专用入口认证头；如需此方式须另行接入，不要复用渠道 API Key。

    # --- 方法与请求体 ---
    limit_except GET HEAD {
        deny all;
    }

    # --- 内部目标头：校验后才可用于变量 proxy_pass ---
    # 长度上限与本服务 8 KiB 限制协调；服务器级声明：
    # large_client_header_buffers 4 16k;
    set $media_target "";
    if ($http_x_newapi_media_url = "") {
        return 400;
    }
    if ($media_target_authority = "") {
        return 403;
    }
    set $media_target $http_x_newapi_media_url;

    # --- 出站 ---
    # resolver 指向可信 DNS（变量 proxy_pass 不走系统 hosts）。
    resolver 10.0.0.2 valid=300s ipv6=off;
    resolver_timeout 5s;

    proxy_pass $media_target;

    # 上游身份：Host 指向原媒体目标，HTTPS 正确 SNI 并校验证书。
    proxy_set_header Host $media_target_authority;
    proxy_ssl_server_name on;
    proxy_ssl_name $media_target_host;
    proxy_ssl_verify on;
    proxy_ssl_trusted_certificate /etc/nginx/trusted-ca.crt;
    proxy_ssl_protocols TLSv1.2 TLSv1.3;

    # 默认会透传原请求头；必须先关闭，再显式设置允许的媒体头。
    # 内部目标头、入口认证头、Authorization、Cookie 均不得到达媒体源。
    proxy_pass_request_headers off;
    proxy_pass_request_body off;
    proxy_set_header Content-Length "";
    proxy_http_version 1.1;
    proxy_set_header Range $http_range;
    proxy_set_header If-Range $http_if_range;
    proxy_set_header If-None-Match $http_if_none_match;
    proxy_set_header If-Modified-Since $http_if_modified_since;
    proxy_set_header Accept-Encoding "";
    proxy_set_header Connection "";

    # 响应：保留媒体类型、长度、范围与缓存校验头，正确传递 200/206/304/416。
    proxy_pass_header Content-Length;
    proxy_pass_header Content-Range;
    proxy_pass_header Accept-Ranges;
    proxy_pass_header ETag;
    proxy_pass_header Last-Modified;
    proxy_pass_header Content-Type;

    # 缓冲与缓存：媒体响应不缓冲、不缓存，避免全文件缓冲与私有媒体共享缓存。
    proxy_buffering off;
    proxy_cache off;

    # 重定向：原样传回 Location，由本服务发起下一跳。
    proxy_redirect off;
    proxy_next_upstream off;

    # 超时：按首部等待与读写空闲配置，与本服务协调（示例值需按环境调整）。
    proxy_connect_timeout 10s;
    proxy_read_timeout 120s;
    proxy_send_timeout 60s;
}
```

说明与限制：

- `proxy_pass $variable` 使用完整 URL 时不改写 URI，原始转义路径与查询串（含签名、`%2F`、`+`、重复参数顺序）原样转发。
- 配置中的目标头校验是最低限度；NGINX 侧还应通过防火墙/安全组限制出站目标（受控媒体域名、端口），不能仅依赖本服务在 HK 的 DNS 校验推断远端连接安全。
- 日志使用自定义格式，只记录方法、`$uri`、状态、请求 ID 与耗时；**不记录** `$http_x_newapi_media_url`（含签名）或完整查询串。若入口与任务资源 URL 重叠，注意不落 `access` 签名参数。
- 该 location 应部署在允许本服务访问的地址上；若渠道 API 地址带路径前缀（如 `https://gate.example.com/gateway`），入口为 `/gateway/_newapi/media`，网关会自动保留前缀。

## 验收清单（需真实 NGINX 执行）

- [ ] `nginx -t` 通过，且配置在真实实例加载成功。
- [ ] 通过入口请求受控媒体源，观测媒体源侧（访问日志/出站抓包）来源 IP 为 NGINX 出口，而非 HK 本服务。
- [ ] GET 与 HEAD 均正确返回；Range/If-Range/If-None-Match/If-Modified-Since 正确到达媒体源；200/206/304/416 状态原样返回。
- [ ] 媒体源返回 301/302/307/308 时，NGINX 原样回传状态与 Location，未自行跟随；网关收到后经同一入口发起下一跳。
- [ ] 绝对、相对（含 `../`）、同域、跨域 Location 均验证通过；循环与超限跳转被网关拒绝。
- [ ] 未携带 `X-NewAPI-Media-URL` 或目标非法时返回 400/403，不会转发到默认上游。
- [ ] HK 对媒体源直连被阻断时，开启渠道开关后任务视频、尾帧图片下载仍成功。
- [ ] 关闭开关后恢复既有直连行为；开关与渠道正向代理同时启用时验证“网关 → 正向代理 → NGINX 入口 → 媒体源”完整链路。
- [ ] 日志检查：网关、入口代理与 NGINX 日志均不含完整签名 URL 或 `access` 参数。
- [ ] 无凭证媒体请求头不包含渠道 API Key、内部目标头之外的鉴权信息；NGINX 转发头符合允许列表。

## 发布前修复与验证边界

- 媒体配置保存与下载时共用类型/地址校验；开启但配置错误会失败，不回退为 HK 直连。
- 重定向响应体直接关闭，不为复用连接等待无关的响应体；保留现有逐跳转发结构。
- `openai_video` 仍未实施：界面禁用，保存及豆包新任务提交均拒绝该值；存量任务查询/下载不受新任务协议校验影响。
- 真实 NGINX 的本地隔离验证与线上 HK 验收分别记录。只有在线上确认媒体源看到 NGINX 出口 IP，才可勾选上面的业务验收项。

## 本地验证入口

```bash
python3 scripts/tests/video-media-nginx.py
```

脚本直接提取本文的两个 NGINX 配置块，以 `nginx:1.28.2-alpine` 创建临时容器，只发布本机端口，并在结束时删除容器。仅替换本地测试所需的 DNS、CA 文件路径和调用方网段，另加受控媒体源与主机名解析检查端点。

2026-09-23 执行通过：`nginx -t`、GET/HEAD、200/206/304/416、Range 和条件请求头、内部目标头/Authorization/Cookie/入口认证头隔离、签名路径与重复查询参数、无路径查询 URL、重定向返回后重新经入口请求、非法目标和 POST 拒绝，以及 DNS/端口/IPv6 主机名提取。

验证局限：本地媒体源使用 HTTP；IPv6 仅检查解析，未验证 IPv6 连通性；未验证生产 HTTPS 证书/SNI、远端 DNS 与出站访问控制、HK 禁止直连和实际出口 IP。上面的线上验收清单仍需逐项执行。
