---
status: active
owner: Dev Team
last-reviewed: 2026-08-09
---

# OpenResty 模型路由性能与协议风险分析

## 问题与目标

当前部署目标是在单一公网域名下，由香港入口根据 Relay 请求中的 `model` 自动选择香港或新加坡 New API 实例：

- 控制台、静态资源和 `/api/*` 始终由香港 master 处理；
- 国内模型使用香港 New API 及香港网络出口；
- GPT 等境外模型使用新加坡 slave 及新加坡网络出口；
- 客户端不选择区域、不更换域名，也不增加自定义路由参数；
- 单个 Relay 请求只能由一个 New API 实例完成鉴权、渠道选择、预扣、结算和日志写入。

标准 Nginx 的加权轮询可以承载同质 New API 集群，但不会解析 JSON 请求体，也不会根据 `model` 选择区域。OpenResty 可以通过 `access_by_lua_file` 在代理前解析模型并选择 upstream。本分析用于明确该方案的性能成本、不同协议的路由风险、生产约束和验证门槛。

本文不将工程估算视为已完成压测。所有延迟、容量和请求体阈值都必须由预发布环境的真实请求分布验证后才能转为生产事实。

## 当前实际情况

### 已验证事实

1. 当前架构文档已将 OpenResty 作为单域名模型路由入口，并规定控制面固定香港、Relay 按模型选择香港或新加坡，参见 [部署架构](../20-architecture/部署架构.md)。
2. OpenResty 的 `lua-nginx-module` 使用 LuaJIT，并由 Nginx worker 内的轻量协程隔离请求上下文。官方说明本地 LuaJIT 处理在 CPU 和内存方面可达到接近原生 C 模块的性能水平；这说明简单的本地解析和表查询本身通常不是主要瓶颈，但不构成当前机器上的性能保证。[lua-nginx-module 官方文档](https://github.com/openresty/lua-nginx-module)
3. `ngx.req.read_body()` 会显式读取请求体；请求体在内存时可通过 `ngx.req.get_body_data()` 获取，超过缓冲能力时可能存放到临时文件并通过 `ngx.req.get_body_file()` 暴露。[OpenResty 请求体 API 示例](https://raw.githubusercontent.com/openresty/lua-nginx-module/master/README.markdown)
4. Nginx 默认 `client_body_buffer_size` 通常为 8 KiB 或 16 KiB。请求体超过该缓冲时，全部或部分内容可能写入临时文件；`client_max_body_size` 默认值为 1 MiB。[Nginx core 官方文档](https://nginx.org/en/docs/http/ngx_http_core_module.html)
5. 当前路由示例使用 `cjson.safe.decode()` 解析整个 JSON。即使只读取顶层 `model`，解析器仍需处理完整的 `messages`、prompt、tools、schema 和 Base64 内容。
6. 当前示例在请求体落盘后使用标准 Lua `io.open()` 和 `read("*a")` 读取临时文件。这是同步文件 I/O，会占用当前 Nginx worker 的执行时间；在并发大请求下可能放大磁盘、CPU 和内存压力。
7. 读取请求体只发生在请求进入 upstream 前。只要 Relay 响应配置 `proxy_buffering off`，OpenResty 不需要逐块解析模型输出，SSE 响应仍可在收到上游数据后立即传给客户端。[Nginx proxy 官方文档](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)
8. HK、SG 共用 PostgreSQL、Redis、`SESSION_SECRET` 和 `CRYPTO_SECRET` 后，登录和计费状态不依赖 Nginx 会话粘滞。区域选择的目的在于网络出口和协议归属，而不是保存进程内登录 Session。

### 尚未验证的工程估算

- 对几 KiB 至几十 KiB、保持在内存中的普通 JSON，请求体读取、LuaJIT 执行、`cjson` 解码和本地规则查找预计只增加亚毫秒至数毫秒级处理时间；该范围尚未在目标 ECS 上验证。
- 对数百 KiB 至数 MiB 的 JSON，CPU、短期内存分配和解析时间预计随请求体大小增长；包含大型 tools/schema 或 Base64 图片时不能按普通文本请求估算。
- 一旦请求体写入临时文件，延迟将受到磁盘 IOPS、页缓存、并发量以及 PostgreSQL/Redis 同机资源竞争影响，不能给出稳定固定值。
- OpenResty 本地解析开销预计小于 HK-SG 跨区数据访问和模型上游响应耗时，但只有端到端 TTFT、P99 和资源监控才能证明这一判断。

### 请求类型风险矩阵

| 请求类型 | model 来源 | 主要成本 | 潜在风险 | 当前建议 |
| --- | --- | --- | --- | --- |
| 控制台、静态资源、`/api/*` | 不需要 | 普通反向代理 | 若误入 Lua 会增加无意义 Body 处理 | 固定香港，不执行模型解析 |
| Chat、Responses、Embeddings 等 JSON Relay | JSON 顶层 `model` | 完整读取和解析 JSON | 大 prompt、tools/schema、深层 JSON、Base64 内容增加 CPU 和内存 | 仅在受控大小内解析，精确校验顶层字符串 model |
| SSE 流式 Relay | 首个 JSON 请求中的 `model` | 路由阶段解析一次 | 响应缓冲或读取超时导致流式中断 | `proxy_buffering off`、长 `proxy_read_timeout`、禁止代理缓存 |
| Realtime/WebSocket | Query 中 `model` | 路径与 Query 查找 | 缺失 model、Upgrade 配置错误、空闲超时 | 优先读取 Query，不读取 Body，完整传递 Upgrade 头 |
| Gemini 原生协议 | `/v1beta/models/{model}:action` | URI 解析 | URL 编码、动作后缀、模型名边界解析错误 | 从规范化 URI 提取，覆盖 generate/stream/countTokens 用例 |
| Multipart 音频、图片和文件上传 | 表单字段或无 model | 若解析 multipart 则需处理文件流 | 大文件完整缓冲、临时文件、worker 阻塞 | 不走通用 JSON 解析；按路径/平台固定区域 |
| 压缩 JSON | 压缩请求体内部 | 解压后才能解析 | `cjson` 直接失败、CPU 放大、压缩炸弹 | 对需模型路由的压缩请求明确拒绝或固定回退，不在 Lua 中自动解压 |
| 异步任务创建 | JSON、路径或平台 | 视创建协议而定 | 创建进入 SG，后续查询没有 model 而默认 HK | 同一任务平台的创建、查询、取消固定同一区域，或建立 task ID 到区域映射 |
| 异步任务查询/取消 | 通常只有 task ID | 路径或映射查找 | 无法仅凭用户 Session 推断创建区域 | 不依赖 Session 粘滞；优先按平台路径固定 |
| `/v1/models`、文件和 Batch 查询 | 通常无 model | 普通代理 | 默认节点不明确导致资源后续请求跨区域 | 为每类无 model 端点显式定义固定节点 |
| 非法 JSON、缺失 Content-Type、顶层数组 | 无可靠 model | 解析失败 | 静默默认 HK 隐藏客户端错误或产生错误出口 | 定义统一失败策略并记录原因，不记录完整 Body |
| 未知模型或新增别名 | 未命中规则 | 本地查表 | 模型清单滞后导致错误区域 | 默认策略、未命中指标和发布同步必须同时存在 |

## 优化方案

### 一、限制 Lua 路由范围

只对确实需要从 JSON 获取 `model` 的 Relay location 执行 `ngx.req.read_body()`。以下请求绕过通用 JSON 路由：

- `/api/*`、前端和静态资源固定香港；
- Realtime 从 Query 读取 model；
- Gemini 从 URL 读取 model；
- multipart、文件、音频、异步任务平台按路径固定区域；
- 无 model 的资源查询端点使用显式固定规则。

禁止在顶层 `location /` 对所有请求执行 Body 解析。

### 二、建立有界的 JSON 路由合同

生产配置必须确定以下边界：

1. `client_max_body_size`：允许进入该 JSON Relay location 的最大请求体；
2. `client_body_buffer_size`：根据实际 P95/P99 请求体选择，不能为了避免落盘而无上限调大；
3. `client_body_timeout`：限制两次请求体读取之间的空闲时间；
4. model 必须是 JSON 顶层字符串，长度不超过 256 字节；
5. 明确拒绝或固定处理非 `application/json`、压缩 JSON 和落盘请求体；
6. 解析错误日志仅包含 request ID、URI、Content-Type、请求长度、错误类型和最终节点，不包含 Authorization、prompt 或完整 Body。

在没有并发内存预算前，不直接采用数 MiB 或数十 MiB 的 `client_body_buffer_size`。单请求缓冲上限必须乘以峰值并发，纳入 HK 主机上 OpenResty、New API、PostgreSQL、Redis 和内核页缓存的总内存预算。

### 三、移除 worker 内同步大文件读取

生产路由不采用以下无上限路径：

```lua
local file = io.open(body_file, "rb")
body = file:read("*a")
```

请求体落盘时应从以下策略中选择一种，并写入明确配置：

- 对需要 JSON 模型路由的请求返回 413；
- 按端点定义固定区域并跳过 Body 解析；
- 在经过独立设计和压测后使用不阻塞 Nginx worker 的受控处理方式。

在未完成专项实现和压测前，不把同步读取临时文件作为生产兼容路径。

### 四、保持响应流式和单次执行语义

Relay location 至少配置：

```nginx
proxy_http_version 1.1;
proxy_buffering off;
proxy_cache off;
proxy_connect_timeout 5s;
proxy_send_timeout 300s;
proxy_read_timeout 3600s;
proxy_next_upstream off;
```

Realtime/WebSocket 额外传递 `Upgrade` 和 `Connection`。`proxy_next_upstream off` 用于防止 AI 生成、预扣费或任务创建在传输结果不明确时被代理自动重放到另一节点。

### 五、把异步任务和资源后续请求单独设计

用户或登录 Session 粘滞不能证明任务创建区域。优先按平台建立稳定路由：

```text
某任务平台的创建、查询、取消 → 全部固定香港或全部固定新加坡
```

只有无法通过路径或平台固定区域时，才评估 `task_id → region` 映射。该映射若由 OpenResty 查询远程 Redis，会给公网入口新增网络依赖、跨区延迟和故障模式，必须单独设计超时、回退、TTL 和审计，不应隐含在通用模型路由中。

### 六、路由规则发布与可观测性

每个 Relay 请求记录：

- request ID；
- 规范化后的 model；
- model 来源：query、path、json、fixed、fallback；
- 目标节点；
- 解析失败类型；
- 请求长度；
- `$request_time`、`$upstream_connect_time`、`$upstream_header_time` 和 `$upstream_response_time`。

建立以下指标和告警：

- JSON 解析失败率；
- model 缺失和未知模型数量；
- 默认回退香港的比例；
- 请求体落盘/超上限次数；
- HK/SG 路由命中比例；
- OpenResty worker CPU、RSS 和临时目录写入量；
- TTFT、SSE 中断率和 Relay 错误率。

新增、删除或重命名模型时，应用配置和 OpenResty 路由规则必须在同一发布单元中更新。上线前执行 `openresty -t`，通过后 reload，并保留上一版本配置以便回滚。

### 七、压测与验收门槛

使用相同机器、相同 upstream mock 和相同连接参数比较：

- 基线：Nginx/OpenResty 直接 `proxy_pass`，不解析 Body；
- 候选：`ngx.req.read_body()`、`cjson.safe.decode()`、本地规则匹配和动态 upstream。

请求集合至少覆盖：

- 4 KiB、32 KiB、256 KiB、1 MiB 和真实生产 P99 JSON；
- 大型 tools/schema；
- Base64 图片 JSON；
- SSE；
- Realtime/WebSocket；
- Gemini URL；
- multipart；
- 压缩 JSON；
- 非法、深层嵌套、缺失 model 和未知 model 请求；
- 异步任务创建、查询和取消。

至少记录：

- 候选相对基线增加的 P50/P95/P99 入口延迟；
- RPS、错误率、CPU、worker RSS 和临时文件写入；
- 路由正确率；
- 端到端 TTFT 和 SSE 中断率；
- HK-SG 上游连接耗时；
- 在目标峰值并发下是否出现 worker 阻塞、磁盘排队或内存压力。

建议把“受控大小、全程内存内的普通 JSON 路由新增 P99 延迟不超过数毫秒”作为初始候选门槛，而不是已验证事实。最终阈值、JSON 上限和回退策略必须根据生产请求分布、峰值并发和 ECS 资源预算确认。

### 状态跟踪

- [x] 已通过项目代码、现有架构文档和官方 Nginx/OpenResty 文档确认请求体缓冲、Lua 路由和响应流式的基本行为。
- [x] 已识别普通 JSON、SSE、Realtime、Gemini、multipart、压缩 JSON、异步任务和无 model 请求的协议边界。
- [ ] 尚未在目标 HK ECS 上完成基线与候选压测。
- [ ] 尚未根据真实请求 Body P95/P99 确定 `client_max_body_size` 与 `client_body_buffer_size`。
- [ ] 尚未确认所有异步任务、文件和 Batch 端点的区域归属。
- [ ] 尚未将生产版 Lua 路由和 OpenResty 配置落地并执行协议回归。
- [ ] 压测与协议回归完成后，将已验证的长期架构约束收敛到 `docs/20-architecture/部署架构.md`，将可执行部署与监控步骤收敛到 `docs/40-operations/` 对应事实文档。
