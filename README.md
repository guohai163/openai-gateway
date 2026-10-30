# AI 网关 / AI Gateway

[![CI & Container Image](https://github.com/guohai163/openai-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/guohai163/openai-gateway/actions/workflows/ci.yml)

源码：[guohai163/openai-gateway](https://github.com/guohai163/openai-gateway) · 镜像：`ghcr.io/guohai163/openai-gateway`

![AI Gateway project icon](web/public/gyai-icon.png)

最新版本：[v1.4.2](https://github.com/guohai163/openai-gateway/releases/tag/v1.4.2) · [更新与升级说明](CHANGELOG.md)。固定版本部署可将 `.env` 中的 `GATEWAY_IMAGE` 设为 `ghcr.io/guohai163/openai-gateway:1.4.2`。

Go + PostgreSQL 实现的 OpenAI 兼容私有网关，带中英文管理后台。固定上游 `https://api.openai.com`；客户端只接触网关私有 Key。

## 功能

- 直接添加上游普通 API Key，自动同步并保存完整模型列表；共享全部可用上游密钥池。
- 按可用模型轮询上游 Key，明确拒绝时最多尝试 3 个不同 Key。
- 私有 Key 独立 IPv4/IPv6/CIDR 白名单、模型权限、到期与撤销。`*` 表示所有当前可用模型。
- Chat Completions、Embeddings、Responses 和 SSE；Responses 资源归属及多轮上下文持久化绑定。
- 按上游 Key 记录额度阻断、认证错误与限流，支持定时小额探测自动恢复。
- AES-256-GCM 加密上游凭据和私有 Key；私有 Key 另存 SHA-256 哈希用于认证，支持管理员回显；管理员密码 bcrypt；服务端会话、CSRF 和登录限速。
- 调用记录展示输入/输出 Token 与估算费用、缓存命中与缓存用量；价格快照随记录保存。
- 中英文自动识别、手动切换并记住选择；亮色、暗色主题默认跟随系统，也可手动选择并记住。只记录调用元数据，不保存对话正文。

## 使用 CI 镜像运行（推荐）

服务器只需要 Docker Engine 和 Docker Compose v2，无须安装 Go 或 Node.js。镜像支持 Linux AMD64 和 ARM64。

### 1. 下载配置并生成部署密钥

```sh
git clone https://github.com/guohai163/openai-gateway.git
cd openai-gateway
cp .env.example .env
openssl rand -hex 24      # 将结果填入 .env 的 POSTGRES_PASSWORD
openssl rand -base64 32   # 将结果填入 .env 的 MASTER_KEY
openssl rand -hex 24      # 将结果填入 .env 的 ADMIN_PASSWORD
```

编辑 `.env`，替换所有 `replace-with-...` 示例值。`POSTGRES_PASSWORD` 使用上述十六进制字符串，避免数据库连接 URL 转义问题。`.env` 不会被提交或打包到镜像中。

| 变量 | 说明 |
| --- | --- |
| `GATEWAY_IMAGE` | 默认 `ghcr.io/guohai163/openai-gateway:latest`，也可指定版本或 SHA 标签 |
| `POSTGRES_PASSWORD` | PostgreSQL 密码；第一次初始化后保持不变 |
| `MASTER_KEY` | `openssl rand -base64 32` 生成的加密主密钥；必须随数据库备份妥善保存 |
| `ADMIN_USERNAME` | 管理员用户名，默认 `admin` |
| `ADMIN_PASSWORD` | 首次初始化的管理员密码，至少 12 个字符 |
| `COOKIE_SECURE` | HTTPS 正式部署设为 `true`；本地 HTTP 试用改为 `false` |
| `BIND_ADDRESS` | 默认 `127.0.0.1`，只向本机暴露网关 8080 端口 |
| `TRUSTED_PROXIES` | Go 服务实际看到的可信代理来源 CIDR，逗号分隔；无代理时留空 |
| `REQUEST_TIMEOUT_SECONDS` | 上游调用总超时，默认 600 秒，包含流式调用 |

### 2. 拉取并启动

运行文件是仓库根目录的 [docker-compose.yaml](docker-compose.yaml)，Docker Compose 会自动识别这个标准文件名。

```sh
docker compose pull
docker compose up -d
docker compose ps
curl --fail http://127.0.0.1:8080/healthz
```

健康检查返回 `{"status":"ok"}` 表示数据库和网关可用。本地试用时将 `COOKIE_SECURE=false`，通过 `http://127.0.0.1:8080` 登录管理后台，账号取自 `.env`。远程服务器可以用 SSH 转发在本机试用：

```sh
ssh -L 8080:127.0.0.1:8080 user@your-server
```

生产环境使用 [deploy/nginx.conf](deploy/nginx.conf) 配置域名、证书和 HTTPS 反向代理，并保持 `COOKIE_SECURE=true`。不要将数据库端口暴露到公网。

如果拉取镜像时提示 `denied` 或 `unauthorized`，先在 GitHub 的包设置中确认该镜像的可见性。GHCR 首次发布的包可能是私有包：私有包需要在服务器用具有 `read:packages` 权限的 GitHub classic PAT 登录；公开包可以匿名拉取。

```sh
# 在终端环境中设置 GHCR_TOKEN，避免把实际令牌写入仓库或命令文本。
printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u guohai163 --password-stdin
docker compose pull
docker compose up -d
```

参考：[GHCR 身份认证和包可见性](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。

`TRUSTED_PROXIES` 必须配置为 Go 服务实际看到的反向代理来源 CIDR；Docker 转发后可能是 Docker 网桥地址。仅添加精确代理地址，并确保 8080 不能被其他来源绕过代理访问。留空时忽略转发头，以 TCP 来源 IP 校验白名单。Nginx 示例覆盖客户端自带的 `X-Forwarded-For`；可信代理链从右向左解析，遇到首个非可信地址即停止。

### Nginx 前面还有 PROXY protocol 负载均衡

对于 `listen 443 ssl proxy_protocol`，还需要让 Nginx 使用 PROXY protocol 的客户端地址替换 `$remote_addr`。完整示例见 [deploy/nginx-proxy-protocol.conf](deploy/nginx-proxy-protocol.conf)，已包含你的 `gyai.dev` 域名与 SSE 设置。

```nginx
# 仅信任实际向 Nginx 发送 PROXY protocol 的负载均衡器地址，示例须替换。
set_real_ip_from 192.0.2.10/32;
real_ip_header proxy_protocol;

# 放在 location / 中。覆盖外部客户端自带的转发头。
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $remote_addr;
proxy_set_header X-Forwarded-Proto $scheme;
proxy_buffering off;
proxy_read_timeout 660s;
```

网关 `.env` 的 `TRUSTED_PROXIES` 则配置 **Nginx 到网关这一跳的来源地址**，例如固定的 Nginx 容器地址 `172.30.0.2/32`（须替换为实际值），修改后重新创建网关容器。Nginx 必须启用 `http_realip_module`，其 443 端口仅允许可信负载均衡器访问。只有前置负载均衡器确实发送 PROXY protocol 时，才使用这个 `listen` 配置。参考：[Nginx Real IP 官方文档](https://nginx.org/en/docs/http/ngx_http_realip_module.html)。

Nginx 和网关在 Docker 中时须加入同一网络；示例 `openai-gateway:8080` 应能从 Nginx 容器解析，使用本仓库 Compose 的服务名时可改为 `gateway:8080`。网关从右向左解析可信链；没有 `X-Forwarded-For` 时，才接受可信来源的 `X-Real-IP`。不在 `TRUSTED_PROXIES` 中的请求会忽略这两个头。调用记录、IP 白名单和后台自动填入的来源 IP 共用同一解析逻辑。

`ADMIN_USERNAME` / `ADMIN_PASSWORD` 只用于空数据库的首次初始化，不会在重启时覆盖已有账号。重启、更新镜像时必须保留原来的 `MASTER_KEY` 和数据库卷。

### 3. 常用运行命令

```sh
# 查看状态和日志
docker compose ps
docker compose logs -f --tail=100 gateway

# 更新到 .env 指定的镜像版本
docker compose pull gateway
docker compose up -d gateway

# 重启服务
docker compose restart gateway

# 停止并移除容器，保留数据库卷
docker compose down
```

数据库持久化在 `postgres_data` 命名卷中。`docker compose down -v` 会删除数据库数据，不用于日常停止服务。升级前先执行文末备份步骤。需要回滚时，将 `.env` 中 `GATEWAY_IMAGE` 改为之前验证过的 SHA 或版本标签，再拉取、启动；涉及数据库变更时使用对应备份恢复。

### 初始配置顺序

1. 添加上游密钥，填写名称和普通 OpenAI API Key。系统仅使用该 Key 查询模型列表，不发送组织或项目头；成功后加入共享调度池。点击模型数量可以查看完整模型列表和原始响应。
2. 如需额度恢复探测，在该上游 Key 中填写可用且支持 Responses 的探测模型。留空不影响正常推理，但不会发起恢复探测。
3. 颁发私有 Key，名称仅支持小写字母、数字和连字符（`-`），最多 120 个字符；IP 白名单默认按当前请求者 IPv4 `/24` 或 IPv6 `/64` 自动填写，可手动修改。从全部已同步上游 Key 的模型去重列表中搜索、多选，或选择全部模型。有效期仅支持 30、90、120 天，默认 30 天，从创建或续期时计算。新密钥统一以 `gk-ai-` 开头，管理员可在操作栏点击“显示密钥”再次查看或复制；客户端 `base_url` 使用 `https://你的域名/v1`。

私有密钥操作栏和新密钥弹窗均提供“如何使用”，自动填入完整密钥和当前网关地址，生成发送 `hi` 的 curl 示例。密钥输入框支持显示、隐藏及复制；明文只存在当前弹层内存中，不写入浏览器持久存储。默认示例模型为 `gpt-6-luna`，不可用时自动选择已授权且已同步的模型。模型可修改，未授权或未同步时会提示；从 IP 白名单允许的设备执行命令。既有旧前缀密钥仍可正常认证。

升级前只保存了哈希的旧私有 Key 无法从数据库还原。旧 Key 下次通过认证及 IP 白名单检查时，会自动补存 AES-GCM 加密副本；也可在“如何使用”中粘贴原密钥并点击“验证并保存原密钥”。如果原密钥已经丢失，点击“重新生成密钥”会原子撤销旧密钥并生成新的可回显密钥；客户端需要改用新密钥。删除私有 Key 会同时清除加密副本。

`0.0.0.0/32` 只允许来源地址恰好为 `0.0.0.0` 的请求，并不表示全部地址。允许所有 IPv4 地址使用 `0.0.0.0/0`，所有 IPv6 地址使用 `::/0`，也可填写实际来源 IP 或网段；两种地址族分别匹配。认证成功但白名单拒绝时，错误响应的 `error.client_ip` 返回网关识别到的来源 IP，便于核对。

```sh
curl https://ai.example.com/v1/models \
  -H "Authorization: Bearer $GYAI_KEY"

curl https://ai.example.com/v1/responses \
  -H "Authorization: Bearer $GYAI_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"你的可用模型ID","input":"你好","stream":true}'
```

不要将上游普通 Key 发给客户端。调用者自带的组织/项目认证头不会传给上游。

## API

| 接口 | 行为 |
| --- | --- |
| `GET /v1/models` | 私有权限与当前可调度模型交集 |
| `POST /v1/chat/completions` | JSON / SSE |
| `POST /v1/embeddings` | JSON |
| `POST /v1/responses` | JSON / SSE，支持 `previous_response_id` |
| `GET, DELETE /v1/responses/{id}` | 绑定原上游并校验私有 Key 归属 |
| `POST /v1/responses/{id}/cancel` | 取消后台响应 |
| `GET /v1/responses/{id}/input_items` | 查询输入项，支持分页查询参数 |
| `GET /healthz` | 数据库健康检查 |

请求体上限 32 MiB；单 SSE 事件上限 8 MiB。客户端断开会取消上游请求。代理不会解析并重写模型输出；仅旁读 ID、usage 和结构化错误。为保持最小权限，不开放 Files、Batch、Conversations 管理或 WebSocket。

Responses ID 只能由创建它的私有 Key 使用；后续轮次与资源操作绑定原上游，禁用、额度不足或模型不可用时返回 `503 no_available_upstream`，不会切换账户。只接受本网关生成并登记的 ID。保留上游 `store` 默认行为；使用 `store=false` 时上游可能不支持后续查询或续接，网关不会额外保存上下文。

管理 API 位于 `/admin/api`，先 `POST /login` 获取会话 Cookie 和 `csrf`，非 GET 操作携带 `X-CSRF-Token`。后台使用这些相同接口：

- `GET /session`、`POST /logout`、`GET /dashboard`、`GET /settings`。
- `GET /client_ip` 返回当前请求的 `client_ip` 和建议的 `default_cidr`（IPv4 `/24`、IPv6 `/64`）。新建私有 Key 时自动填入白名单，可手动修改；编辑已有 Key 保留原白名单。客户端 IP 沿用 `TRUSTED_PROXIES` 的可信代理解析，不信任任意转发头。
- `GET, POST /upstream_keys|gateway_keys`；`PATCH /{resource}/{id}` 修改名称、启停及对应权限。上游密钥明文不可修改，需要创建新资源进行轮换；新建上游 Key 只需名称和密钥。
- `POST /upstream_keys/{id}/refresh`、`POST /upstream_keys/{id}/reset_auth`、`POST /upstream_keys/{id}/probe`。
- `GET /upstream_keys/{id}/model_catalog` 返回最近一次成功同步的完整上游模型响应和同步时间；也可在上游密钥列表点击模型数量查看。
- 私有 Key 使用 `models` 数组设置模型权限；`["*"]` 表示全部当前及未来可用模型。新建必须传入整数 `expires_in_days`（30、90、120），不再接受任意 `expires_at`。编辑时省略 `expires_in_days` 保持现有到期时间，传入档位则从当前时间续期；返回的 `expires_at` 仍为服务端计算的实际到期时间。
- `GET /logs|audit`；列表支持 `limit`（1–500）、`offset`。
- `POST /gateway_keys/{id}/reveal` 在管理员会话及 CSRF 校验后返回明文，响应带 `Cache-Control: no-store`；请求体 `{}`，或 `{"key":"原密钥"}` 验证补录旧密钥。列表只返回 `can_reveal`，不返回哈希或密文。回显和补录操作保存管理审计，不记录密钥值。
- `POST /gateway_keys/{id}/rotate` 仅用于没有加密副本的旧私有 Key；管理员会话和 CSRF 校验通过后生成新 Key，撤销旧哈希并返回一次明文。响应带 `Cache-Control: no-store`，操作保存管理审计，不记录密钥值。
- `DELETE /gateway_keys/{id}` 删除私有密钥，要求管理员会话和 CSRF。删除会撤销原凭据、停用并隐藏该密钥，保留历史调用、用量与 Responses 归属；已删除密钥不可通过编辑重新启用。

网关错误格式：`{"error":{"message":"model_not_allowed","type":"gateway_error","param":null,"code":"model_not_allowed"}}`。客户端应读取稳定的 `error.code`；后台将已知错误映射为所选语言。

## 模型定价

管理菜单中的“模型定价”页面提供 OpenAI 官方价格的手动同步。点击“同步官方价格”后，服务端从 [官方价格页的 Markdown](https://developers.openai.com/api/docs/pricing.md) 读取 **Standard 文本、向量和图像 Token**价格，无须提供 Admin Key，也不会发送推理请求。

- PostgreSQL 的 `model_pricing` 表以 `(platform, model)` 为联合主键，同名模型在不同平台的价格独立。当前同步器只支持 `openai`。
- 金额使用 `NUMERIC(24,10)`，单位为 USD / 百万 Token。图片模型在 `modality_prices` 中分别保存 `text` 和 `image` 的十进制单价，文本与图像费率互不替代，图片输出使用图像单价；非按张收费。分别保存输入、输出、缓存输入，以及长上下文输入、输出和缓存输入单价；向量模型没有输出计费时显示“不适用”。
- `long_context_policy` 区分无公开分档、已知分档和规则未知；`long_context_threshold` 保存实际输入 Token 阈值，`long_context_surcharge` 表示长上下文输入或输出单价是否更高。**200K 不是通用加价阈值，KB 也不是计价单位**：若官方标注超过 272K 后加价，就保存并显示 272000 输入 Token；普通价和长上下文价在同一价格格显示，窄窗口无需横向滚动查找。
- 上游 `/v1/models` 同步成功时自动建立尚未匹配价格的记录。价格按精确模型 ID 匹配，不用名称前缀推断快照模型的价格；缺失价格、当前来源未再匹配或上下文规则无法确认的记录标红，支持筛选查看。
- 页面分别显示官方已公布价格和是否已在本机上游模型列表中发现。模型没有价格不会阻止正常转发，也不会被当作免费模型。
- 同步以事务更新，平台互不覆盖。网络失败、页面结构不再识别或出现相互冲突的 Standard 价格时保留旧数据并显示错误；官方网页解析并非官方价格 API。
- Batch、Flex、Fast、音频和按分钟/张收费的数据不会混入此文本单价表。不支持自动解析的模型显示待确认；不会对私有 Key 执行收费或扣减。官方公布的 30 分钟及 12 小时缓存写入单价会一并同步保存。

管理接口：`GET /admin/api/pricing?platform=openai&q=模型ID&missing=true&limit=100&offset=0`；`POST /admin/api/pricing/sync?platform=openai`。均需管理员会话，手动同步还需 CSRF Token；平台同步记录保存在 `model_pricing_sync` 表。

本次迁移 `007_model_pricing.sql` 自动创建定价表并为已有模型建立待定价记录；网关项目管理接口和菜单已移除，旧项目资料归档为 `archived_gateway_projects`，上游请求完全使用密钥自身范围。历史迁移文件仍保留，保证从早期版本升级可重放迁移。升级保持原数据卷和 `MASTER_KEY`，无须重新导入密钥。

图片模型分项价格由 `009_modality_prices.sql` 增加到现有定价表，更新后手动同步即可补全。`gpt-image-2.5-flare` / `gpt-image-2.5-sunburst` 的文本、图像 Token 价格分行显示，缺失分项仍标红。GPT Image 2 / 2.5 的图像缓存单价仅适用于通过 Responses API 生成的图片，详见[官方图片模型价格](https://developers.openai.com/api/docs/pricing#image-generation-models)。当前网关转发接口范围保持不变，图像生成的多模态用量不套用文本费用估算。

## 调用记录：费用与缓存

- 输入 Token 使用绿色向下箭头、输出 Token 使用紫色向上箭头；展示各自金额及合计。点击费用旁的信息图标，展开本次调用的单位价格、缓存费用和长上下文档位。
- 价格按请求的 `(platform, model)` 精确匹配并保存快照；实际返回模型 ID 也会保留。金额使用 `NUMERIC(30,16)`，计算过程不使用浮点数。后续同步单价不会修改历史金额。
- 输入费用 = 普通输入 Token × 输入单价 + 缓存读取 Token × 缓存输入单价 + 各有效期缓存写入 Token × 对应写入单价，再除以一百万；输出费用按输出 Token 和对应档位输出单价计算。缓存 Token 包含在总输入中，不会重复计费。长上下文阈值使用总输入 Token（含缓存），超过阈值后输入和输出均使用长上下文单价。
- Responses 读取 `usage.input_tokens_details.cached_tokens`，Chat Completions 读取 `usage.prompt_tokens_details.cached_tokens`：大于零标记“缓存命中”并显示 Token 数；明确为零标记“未命中缓存”；未返回字段显示“缓存未知”。向量接口显示“缓存不适用”。这是 OpenAI 上游的提示词缓存，网关没有缓存对话正文。参考：[官方 Prompt Caching](https://developers.openai.com/api/docs/guides/prompt-caching)。
- 普通及 SSE 响应都会旁读用量。Chat Completions 流式请求应设置 `stream_options: {"include_usage": true}`；客户端断开或上游没有返回完整用量时，不推算缺失 Token、缓存命中或金额。接口转发内容保持原样。
- 缓存写入返回有效期明细时，分别计算 `ttl_30m_tokens` / `ttl_12h_tokens`；未提供有效期或缺少对应单价时，输入费用显示未知。Batch/Flex/Fast 等不支持的服务档位、多模态价格、工具调用费和协议中未返回的其他费用不混入文本估算；以 OpenAI 官方账单为准。
- 重复查询、恢复流及同一 Response 的并发读取不会重复累计，页面标记“用量已计入原记录”。旧调用记录没有可靠的缓存字段及价格快照，保留原始 Token 计数，费用显示未知，不按今天的价格回填。

升级迁移 `008_request_costs_and_key_reveal.sql` 自动增加私有 Key 密文、缓存写入单价和调用价格快照字段。更新镜像后请到“模型定价”手动同步一次，补全新增写入单价；保留原 `MASTER_KEY` 和数据库卷。

## 调度、额度与统计

- 独立请求按模型轮询所有符合条件的 Key。相同 OpenAI 项目/组织中的 Key 共享上游配额及限流，不会增加上游实际容量。
- 首次模型同步失败的 Key 不参与调度，后续同步失败保留旧列表和原始响应。每次成功同步事务保存完整模型响应（含模型元数据），可调度列表排除已下线模型。正常每 10 分钟同步，可手动刷新。
- 仅上游明确拒绝的认证、额度、限流及模型不存在错误可触发候选切换。网络超时、5xx 或流已开始均不自动重放，避免重复执行或收费。
- `credit_balance_exhausted`、组织/项目支出限制和 `insufficient_quota` 均只阻断返回错误的 Key，不影响其他 Key。限流按 Key + 模型冷却并遵守 `Retry-After`；没有上游组织归属信息时，不推断多个 Key 的共享额度。
- 恢复探测按受阻 Key 执行；失败后间隔为 5、10、20、40、60 分钟并封顶。探测模型由该 Key 配置，必须支持 Responses，并限制 `max_output_tokens=64`、`store=false`。探测会产生少量费用，记录为“恢复探测”；手动停用和凭据无效不自动恢复。
- 概览和调用记录仅统计本网关返回的 Token usage；流式接口缺少终态 usage 时不估算。Response 轮询及重复流按 Response ID 去重，Token 与费用记录在同一事务中提交。不查询余额或官方账单；页面费用为本地估算。

官方参考：[模型列表](https://developers.openai.com/api/reference/resources/models/methods/list)、[错误码](https://developers.openai.com/api/docs/guides/error-codes)。

## GitHub Actions 自动构建镜像

工作流位于 [.github/workflows/ci.yml](.github/workflows/ci.yml)，在 GitHub 的 [Actions 页面](https://github.com/guohai163/openai-gateway/actions/workflows/ci.yml) 查看日志、发布标签和镜像摘要。

1. PR、`main` 分支提交、`v*` 标签及手动触发都会执行前端测试与构建、PostgreSQL 集成与竞态测试、Go 静态分析、两种 Compose 配置校验和 Docker 镜像启动检查。
2. 检查通过后，`main` 分支和 `v*` 标签会构建 `linux/amd64`、`linux/arm64` 镜像，并推送到 GHCR；PR 只验证，不发布镜像。
3. 工作流使用 GitHub 自动提供的 `GITHUB_TOKEN`，镜像发布任务具有 `packages: write`，不需要配置 Docker Hub 账号或另存发布令牌。
4. 版本标签的镜像发布成功后，独立 Release 任务使用 `contents: write` 创建对应 GitHub Release。说明取自 `CHANGELOG.md` 的当前版本章节，并附镜像标签与摘要；发布前核对标签与前端版本一致。重新运行任务会更新同一 Release。

| 触发来源 | 镜像标签示例 |
| --- | --- |
| `main` 分支 | `latest`、`sha-<完整提交SHA>` |
| `v1.2.3` 标签 | `v1.2.3`、`1.2.3`、`1.2`、`sha-<完整提交SHA>` |
| Actions 手动执行（选择 `main`） | `latest`、`sha-<完整提交SHA>` |

`latest` 跟随通过检查的 `main` 分支。正式部署可以将 `GATEWAY_IMAGE` 固定为某个版本、`sha-...` 标签或 `ghcr.io/guohai163/openai-gateway@sha256:...` 摘要。首次构建需要等待 Actions 成功后再拉取。

发布新版本的示例（用实际版本号替换）：

先更新 `web/package.json`、`web/package-lock.json` 的应用版本和 `CHANGELOG.md`，提交并推送到 `main`，再创建对应标签。

```sh
git tag v1.2.3
git push origin v1.2.3
```

镜像关联到本仓库。若首次发布被 GitHub 的 Actions 或 Packages 策略限制，请在仓库设置中允许该工作流运行及写入包；具体失败原因见 Actions 日志。镜像首次发布后的可见性由 GHCR 包设置管理，工作流不会自动更改公开范围。

## 从源码构建 Docker 镜像

本地开发或不使用 GHCR 时，通过 [compose.build.yaml](compose.build.yaml) 覆盖网关镜像为 `openai-gateway:local` 并增加构建配置，PostgreSQL、环境变量和数据卷与默认部署保持一致：

```sh
# 先按前文准备 .env
docker compose -f docker-compose.yaml -f compose.build.yaml up -d --build
docker compose -f docker-compose.yaml -f compose.build.yaml logs -f gateway

# 后续本地更新仍使用相同的两个配置文件
docker compose -f docker-compose.yaml -f compose.build.yaml up -d --build gateway
```

单独构建镜像：

```sh
docker build -t openai-gateway:local .
```

## 开发与测试

需要 Go 1.26+、Node 24+、PostgreSQL 17+。

```sh
cd web
npm ci
npm test
npm run build
cd ..
export DATABASE_URL='postgresql://user:password@localhost:5432/gyai?sslmode=disable'
export MASTER_KEY="$(openssl rand -base64 32)"
export ADMIN_PASSWORD='replace-with-a-unique-long-password'
export COOKIE_SECURE=false
go run ./cmd/gateway
```

开发热更新可另开终端执行 `cd web && npm run dev`，Vite 会代理管理 API 到本地 8080。后端必须先构建 `web/dist`，以嵌入前端资源。

```sh
go test -race ./...
go vet ./...
# 使用隔离数据库；测试会自动创建并清理独立 schema，需要 CREATE 权限。
TEST_DATABASE_URL='postgresql://user:password@localhost:5432/testdb?sslmode=disable' \
  go test -race -count=1 ./internal/gateway/...
```

集成测试不调用 OpenAI，使用本地模拟 HTTP 上游。未设置 `TEST_DATABASE_URL` 时跳过数据库集成测试。生产入口不允许通过环境变量改写上游地址。

## 已部署旧版本的升级

启动时自动执行数据库迁移，将旧组织/上游项目继承的启停、额度阻断、探测模型和冷却时间转存到各上游 Key。已有密钥密文、模型列表、私有 Key、调用记录、Responses 归属及独立网关项目均保留，无须重新导入。

组织、上游项目和官方费用历史表归档为 `archived_*`，不再提供管理入口或参与调度；旧 Admin Key 凭据字段移除。旧的组织管理、Admin Key 校验、官方费用与密钥发现接口已删除。

升级前先备份数据库和原 `MASTER_KEY`，然后执行 `docker compose pull gateway && docker compose up -d gateway`。不需要删除数据卷。新建上游 Key 只需名称和普通 API Key。

## 备份与恢复

数据库卷保存配置、密钥密文、会话、响应归属、日志与统计。必须同时保管 `MASTER_KEY`；丢失后无法解密上游凭据或回显私有密钥（私有密钥仍可通过哈希认证）。

```sh
docker compose exec -T postgres pg_dump -U gyai -d gyai -Fc > gyai.dump
# 在新建空数据库中恢复，恢复时先停止网关。
docker compose stop gateway
cat gyai.dump | docker compose exec -T postgres pg_restore -U gyai -d gyai --clean --if-exists
docker compose up -d gateway
```

迁移在启动时执行，使用版本表和数据库锁，升级前备份。日志和响应归属首版不自动删除，请按容量规划保留时间；删除归属记录会使对应响应无法续接。会话和过期冷却记录自动清理。

首版运行一个 Go 实例。轮询游标与登录限速在进程内；状态、额度、探测计划和会话在 PostgreSQL。需要多副本时应先增加共享调度、探测锁和分布式登录限速。
