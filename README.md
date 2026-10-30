# AI 网关 / AI Gateway

[![CI & Container Image](https://github.com/guohai163/openai-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/guohai163/openai-gateway/actions/workflows/ci.yml)

源码：[guohai163/openai-gateway](https://github.com/guohai163/openai-gateway) · 镜像：`ghcr.io/guohai163/openai-gateway`

![AI Gateway project icon](web/public/gyai-icon.png)

最新版本：[v1.8.0](https://github.com/guohai163/openai-gateway/releases/tag/v1.8.0) · [更新与升级说明](CHANGELOG.md)。固定版本部署可将 `.env` 中的 `GATEWAY_IMAGE` 设为 `ghcr.io/guohai163/openai-gateway:1.8.0`。

Go + PostgreSQL 实现的 OpenAI 兼容私有网关，带中英文管理后台。固定上游 `https://api.openai.com`；客户端只接触网关私有 Key。

## 功能

- 直接添加上游普通 API Key，自动同步并保存完整模型列表；共享全部可用上游密钥池。
- 按可用模型轮询上游 Key，明确拒绝时最多尝试 3 个不同 Key。
- 多用户系统：管理员及普通用户；全员密码＋TOTP 登录，管理员创建账号并生成一次性展示的 14 位初始密码。
- 私有 Key 独立 IPv4/IPv6/CIDR 白名单、模型权限、到期与撤销；普通用户最多 3 个，只能使用有效定价模型、查看自己的密钥和调用情况。
- Chat Completions、Embeddings、Responses 和 SSE；Responses 资源归属及多轮上下文持久化绑定。
- 按上游 Key 记录额度阻断、认证错误与限流，支持定时小额探测自动恢复。
- AES-256-GCM 加密上游凭据、私有 Key 和 TOTP 密钥；私有 Key 另存 SHA-256 哈希用于认证，管理员或所属用户可回显；密码 bcrypt；服务端会话、CSRF、登录限速与 OTP 防重放。
- 调用记录展示输入/输出 Token 与估算费用、缓存命中与缓存用量；价格快照随记录保存。
- 中英文自动识别、手动切换并记住选择；亮色、暗色主题默认跟随系统，也可手动选择并记住。默认只记录调用元数据；配置 S3 后可按私钥选择存储输入/输出 JSON。

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
| `POST /v1/images/generations` | JSON / SSE，图片生成 |
| `POST /v1/images/edits` | JSON 图片引用或 multipart 文件上传 / SSE，图片编辑 |
| `POST /v1/responses` | JSON / SSE，支持 `previous_response_id` |
| `GET, DELETE /v1/responses/{id}` | 绑定原上游并校验私有 Key 归属 |
| `POST /v1/responses/{id}/cancel` | 取消后台响应 |
| `GET /v1/responses/{id}/input_items` | 查询输入项，支持分页查询参数 |
| `GET /healthz` | 数据库健康检查 |

文本接口请求及非流式响应上限 32 MiB，单 SSE 事件上限 8 MiB。Images 接口请求、非流式响应及单 SSE 事件上限分别为 **128 MiB**；此限制针对整个请求，不是每张图片，多个文件合计也需在限制内。图片上传使用权限为 `0600` 的临时文件缓冲，以便在上游明确拒绝后换 Key 重试，请求结束即删除；不会保存到 PostgreSQL。前置 Nginx 需设置 `client_max_body_size 128m`，参考 `deploy/` 示例。客户端断开会取消上游请求。代理不会解析并重写模型输出；仅旁读 ID、usage 和结构化错误。为保持最小权限，不开放 Files、Batch、Conversations 管理或 WebSocket。

### 图片生成与编辑

使用上游模型列表中已同步、且私有 Key 已授权的精确模型 ID，例如 `gpt-image-2.5-flare` 或 `gpt-image-2.5-sunburst`（不是 `gpt-images-2.5`）。两个接口都要求显式传入 `model`，避免上游默认模型变化绕过授权；模型是否可用仍取决于上游 Key 权限。参数、返回图片的 base64 和上游错误保持原样。

```bash
# 生成图片：返回 JSON；使用 stream:true 可接收原始 SSE 事件。
curl https://ai.example.com/v1/images/generations \
  -H "Authorization: Bearer $GYAI_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-image-2.5-flare","prompt":"一只坐在云上的水獭","size":"1024x1024","quality":"high"}'

# 上传参考图片编辑；可重复 image[]，也可添加 -F 'mask=@mask.png'。
curl https://ai.example.com/v1/images/edits \
  -H "Authorization: Bearer $GYAI_KEY" \
  -F 'model=gpt-image-2.5-sunburst' \
  -F 'image[]=@input.png' \
  -F 'prompt=把背景改为星空' \
  -F 'quality=high'

# JSON 编辑支持 URL 或 data URL；网关不下载这些 URL。
curl https://ai.example.com/v1/images/edits \
  -H "Authorization: Bearer $GYAI_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-image-2.5-sunburst","images":[{"image_url":"https://example.com/input.png"}],"prompt":"把背景改为星空"}'
```

multipart 保留原始 boundary、重复图片字段、文件字节、mask 及其他参数；重复 `model` 字段会拒绝。请求按现有模型权限调度，明确的认证/额度/限流拒绝最多切换 3 个不同 Key；开始输出或结果不确定时不重放生成。`stream:true` 时转发 `image_generation.*` / `image_edit.*` 事件，记录完成事件中的用量。流式中断但上游未返回用量时显示未知，不按图片张数猜测 Token。

详见官方 [图片生成接口](https://developers.openai.com/api/reference/resources/images/methods/generate)、[图片编辑接口](https://developers.openai.com/api/reference/resources/images/methods/edit)。Images 的 `file_id` 依赖其上传时的上游 Key 范围；网关尚未代理 Files，也不跨 Key 管理这些 ID，推荐使用 multipart、URL 或 data URL。

Responses ID 只能由创建它的私有 Key 使用；后续轮次与资源操作绑定原上游，禁用、额度不足或模型不可用时返回 `503 no_available_upstream`，不会切换账户。只接受本网关生成并登记的 ID。保留上游 `store` 默认行为；使用 `store=false` 时上游可能不支持后续查询或续接，网关不会额外保存上下文。

控制台 API 位于 `/admin/api`。先 `POST /login` 验证邮箱（旧管理员可使用原用户名）和密码，取得短期验证 Cookie 与 `csrf`；再 `POST /mfa` 提交 `{"otp":"六位动态码"}` 并携带 `X-CSRF-Token`，通过后才取得正式会话。首次登录需先绑定身份验证器；仅密码不能进入系统。非 GET 操作均携带 CSRF Token。后台使用这些相同接口：

- `GET /session`、`POST /logout`、`GET /dashboard`、`GET /settings`。
- `GET /client_ip` 返回当前请求的 `client_ip` 和建议的 `default_cidr`（IPv4 `/24`、IPv6 `/64`）。新建私有 Key 时自动填入白名单，可手动修改；编辑已有 Key 保留原白名单。客户端 IP 沿用 `TRUSTED_PROXIES` 的可信代理解析，不信任任意转发头。
- `GET, POST /upstream_keys|gateway_keys`；`PATCH /{resource}/{id}` 修改名称、启停及对应权限。上游密钥明文不可修改，需要创建新资源进行轮换；新建上游 Key 只需名称和密钥。
- `POST /upstream_keys/{id}/refresh`、`POST /upstream_keys/{id}/reset_auth`、`POST /upstream_keys/{id}/probe`。
- `GET /upstream_keys/{id}/model_catalog` 返回最近一次成功同步的完整上游模型响应和同步时间；也可在上游密钥列表点击模型数量查看。
- 私有 Key 使用 `models` 数组设置模型权限；管理员的 `["*"]` 表示全部当前及未来可用模型，普通用户的 `["*"]` 仅表示全部当前及未来具有有效完整定价的模型。新建必须传入整数 `expires_in_days`（30、90、120），不再接受任意 `expires_at`。编辑时省略 `expires_in_days` 保持现有到期时间，传入档位则从当前时间续期；返回的 `expires_at` 仍为服务端计算的实际到期时间。
- `GET /logs|audit`；列表支持 `limit`（1–500）、`offset`。
- `POST /gateway_keys/{id}/reveal` 在管理员会话及 CSRF 校验后返回明文，响应带 `Cache-Control: no-store`；请求体 `{}`，或 `{"key":"原密钥"}` 验证补录旧密钥。列表只返回 `can_reveal`，不返回哈希或密文。回显和补录操作保存管理审计，不记录密钥值。
- `POST /gateway_keys/{id}/rotate` 仅用于没有加密副本的旧私有 Key；管理员会话和 CSRF 校验通过后生成新 Key，撤销旧哈希并返回一次明文。响应带 `Cache-Control: no-store`，操作保存管理审计，不记录密钥值。
- `DELETE /gateway_keys/{id}` 删除私有密钥，要求管理员会话和 CSRF。删除会撤销原凭据、停用并隐藏该密钥，保留历史调用、用量与 Responses 归属；已删除密钥不可通过编辑重新启用。

网关错误格式：`{"error":{"message":"model_not_allowed","type":"gateway_error","param":null,"code":"model_not_allowed"}}`。客户端应读取稳定的 `error.code`；后台将已知错误映射为所选语言。

## 多用户、权限与 TOTP

仅管理员可以在“用户管理”创建账号：输入邮箱，默认角色为普通用户，也可选择管理员。系统使用密码学随机数生成 14 位初始密码，仅在创建或重置后展示一次，不发送邮件、不保存明文。用户可自行修改密码，无需首次强制修改；修改时验证当前密码和一个尚未使用的 OTP，随后全部会话退出。

| 能力 | 管理员 | 普通用户 |
| --- | --- | --- |
| 用户、上游、价格同步、系统设置、管理审计 | 可管理 | 不可访问 |
| 私钥及调用记录 | 全部用户 | 仅自己，包括自己的历史调用 |
| 创建私钥 | 无三个限制 | 最多 3 个未删除私钥，禁用/过期仍计数，删除释放名额；并发安全 |
| IP 白名单 | 可配置 IPv4/IPv6/CIDR | 支持具体 IP 和限定 CIDR；禁止 `/0` 全网、`0.0.0.0`、`::` 及未指定地址网络 |
| 可用模型 | 按私钥权限选择已同步模型 | 同时要求当前有效完整价格；创建、列表及实际调用均校验 |
| 输入/输出正文存储 | 按私钥配置 | 不能查看或修改配置；接口不返回开关、存储路径或归档状态 |

普通用户默认只看到“私有密钥”“调用记录”“我的账号”，可回显、复制、修改、续期或删除自己的私钥，并展开自己的自然月模型消费。上游 ID、名称及配置信息不返回给普通用户。默认白名单按其当前连接来源填入具体 IP（IPv4 `/32`、IPv6 `/128`），管理员仍默认 `/24`、`/64`。可信代理解析规则沿用 `TRUSTED_PROXIES`。

“有价格”指 `model_pricing` 的 `price_status=ready`：精确模型、当前有效来源、已知上下文规则及必要的输入/输出分项价格。管理员同步价格后，普通用户模型列表自动更新；无价或失效价格不可通过已有私钥继续调用。普通用户调用仅支持已有 Standard/default 单价档位。管理员可为任一用户的私钥配置 S3 输入/输出记录，普通用户提交相关字段统一被拒绝，不能借助错误探测是否启用了存储。

首次登录会展示**本地生成**的绑定二维码和手动密钥，支持 Google Authenticator、Microsoft Authenticator 等 TOTP App。密钥仅用于本地二维码，不发送到第三方服务。使用 SHA-1、6 位码、30 秒周期，允许相邻时间窗口，校验成功后同一时间步不能再次使用；服务器和手机需保持时间同步。短期验证凭据 5 分钟过期、最多 5 次验证码尝试；IP 限速和账号失败锁定同时生效。TOTP 密钥使用 `MASTER_KEY` 加密入库，备份时一并保管主密钥。

管理员可停用用户、切换角色、重置密码和 OTP。相关操作会使目标用户的登录会话及尚未完成的验证凭据失效；**停用用户同时阻止其全部私钥的新请求**，重新启用后仍需重新登录。重置密码保留原 OTP，重置 OTP 后下次用原密码登录需重新绑定。不能停用或降级最后一名启用的管理员；管理员降级前，其未删除私钥必须不超过三个，白名单也必须符合普通用户规则。操作写入审计，密码、验证码和绑定密钥不进入日志。

新增接口（均需对应权限的正式会话与 CSRF）：

- `GET, POST /admin/api/users`：管理员列出或创建用户；`POST` 接收 `email`、可选 `role`（`user` / `admin`）。
- `PATCH /admin/api/users/{id}`：管理员设置 `role` 或 `enabled`。
- `POST /admin/api/users/{id}/reset_password`、`reset_otp`：管理员重置凭据；新密码仅此次响应可见。
- `GET /admin/api/account`、`POST /admin/api/account/password`：当前账号及修改密码；后者接收 `current_password`、`password`（12–72 字节）、`otp`。
- 普通用户对私钥列表、回显、编辑、删除、消费统计和调用记录接口始终按所属用户过滤；跨用户 ID 返回 `404`。

升级会自动执行 `011_users_and_mfa.sql`，保留原管理员用户名及密码哈希，已有私钥和历史记录归原管理员，**全部旧会话退出，原管理员下一次登录也必须绑定 OTP**。不需要重新颁发已有 API Key。迁移替换旧管理员表，回滚旧程序需要恢复升级前数据库备份。

丢失身份验证器时由另一名管理员重置 OTP。若唯一管理员无法登录，数据库维护者可在确认账号归属后，通过数据库事务清除该账号的 `totp_secret`、将 `totp_last_step=-1`、递增 `auth_version`，并删除对应 `sessions` 与 `login_challenges`；保留密码哈希，之后必须使用原密码重新绑定。不要通过关闭 OTP 校验恢复访问。

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

图片模型分项价格由 `009_modality_prices.sql` 增加到现有定价表，更新后手动同步即可补全。`gpt-image-2.5-flare` / `gpt-image-2.5-sunburst` 的文本、图像 Token 价格分行显示，缺失分项仍标红。直接 Images API 按 `usage.input_tokens_details.text_tokens`、`image_tokens` 和输出图像 Token 分别计算，快照保留分项价格和用量，计入现有自然月消费及模型统计。缺少或不一致的用量显示未知/部分费用。GPT Image 2 / 2.5 的缓存单价仅适用于 Responses API 的图片工具，直接 Images API 显示“缓存不适用”，参考[官方缓存输入价格说明](https://developers.openai.com/api/docs/guides/image-generation#cached-input-pricing)。Responses/Chat 中无法归属到具体图片模型的多模态用量仍不套用文本费用估算。

## 调用记录：费用与缓存

- 请求 IP 后提供“查询”按钮，点击才由服务端请求 `https://ipconfig.io/json?ip=日志IP`，在当前 IP 下展示 `country`、`city`、`asn`、`asn_org`。管理员可查询全部记录，普通用户仅可查询自己的记录；公网 IP 会发送给该第三方服务，结果作为归属参考，不保存到数据库、不自动批量查询。内网、本地及共享地址直接标记，不发送到外部。
- IP 查询接口为 `POST /admin/api/logs/{id}/ip_lookup`，请求体 `{}`，需要正式会话和 CSRF。服务端从有权访问的日志读取 IP，不接受调用者覆盖 IP 或目标 URL；不向第三方转发网关认证头或 Cookie。请求限时 8 秒、响应限 64 KiB，不跟随重定向；失败可手动重试，缺失字段显示 `—`。
- 输入 Token 使用绿色向下箭头、输出 Token 使用紫色向上箭头；展示各自金额及合计。点击费用旁的信息图标，展开本次调用的单位价格、缓存费用和长上下文档位。
- 价格按请求的 `(platform, model)` 精确匹配并保存快照；实际返回模型 ID 也会保留。金额使用 `NUMERIC(30,16)`，计算过程不使用浮点数。后续同步单价不会修改历史金额。
- 输入费用 = 普通输入 Token × 输入单价 + 缓存读取 Token × 缓存输入单价 + 各有效期缓存写入 Token × 对应写入单价，再除以一百万；输出费用按输出 Token 和对应档位输出单价计算。缓存 Token 包含在总输入中，不会重复计费。长上下文阈值使用总输入 Token（含缓存），超过阈值后输入和输出均使用长上下文单价。
- Responses 读取 `usage.input_tokens_details.cached_tokens`，Chat Completions 读取 `usage.prompt_tokens_details.cached_tokens`：大于零标记“缓存命中”并显示 Token 数；明确为零标记“未命中缓存”；未返回字段显示“缓存未知”。向量接口显示“缓存不适用”。这是 OpenAI 上游的提示词缓存；可选 S3 归档与提示词缓存独立。参考：[官方 Prompt Caching](https://developers.openai.com/api/docs/guides/prompt-caching)。
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

## 自然月消费与按模型查询

概览、上游密钥和私有密钥提供自然月 Token 费用统计。密钥列表直接显示当月已知费用；点击“更多明细”向下展开各模型的请求次数、输入/输出 Token、输入/输出费用及合计。概览提供总消费展开区。可查询任意日期范围（最多 366 天，包含结束日期），也可切换当月或上月。

`REPORT_TIMEZONE=Asia/Shanghai` 决定自然月、查询日期和 S3 年月路径的时区。月初从该时区第一天 00:00 开始，下月第一天 00:00 结束。统计来自本网关已保存的单价快照，**不是 OpenAI 官方账单**；费用未知的请求单独显示数量，已知部分显示小计，不把缺失价格算成免费。恢复探测不计入客户消费，同一 Response 已去重的 Token 和费用不再次累计；同一请求的重试以请求 UUID 合并请求次数。

管理接口（均需要管理员会话）：

- `GET /admin/api/spending`：当前自然月总消费和模型明细。
- `GET /admin/api/upstream_keys/{id}/spending`：某个上游 Key 的消费明细。
- `GET /admin/api/gateway_keys/{id}/spending`：某个私有 Key 的消费明细。
- 以上接口支持 `?from=2026-10-01&to=2026-10-30`，结束日期包含当天；列表返回 `month_cost`、`month_requests` 和 `month_unknown`。

私有 Key 名称旁提供复制按钮，直接调用已有的受管理员会话和 CSRF 保护的回显接口，并将完整密钥写入剪贴板。旧版只有哈希的 Key 会打开补录页面，不会自动更换密钥。

## 可选 S3 请求 JSON 存储

在 `.env` 设置以下变量，使用已创建的 S3 兼容存储桶；保留四个必填项为空即关闭功能。配置不完整时启动会报配置错误，避免误以为存储已经生效。

```dotenv
S3_ENDPOINT=https://s3.example.com
S3_REGION=us-east-1
S3_BUCKET=ai-gateway
S3_ACCESS_KEY_ID=replace-with-access-key
S3_SECRET_ACCESS_KEY=replace-with-secret-key
S3_SESSION_TOKEN=
S3_FORCE_PATH_STYLE=true
REPORT_TIMEZONE=Asia/Shanghai
```

支持 Amazon S3、MinIO 及提供兼容接口的对象存储。`S3_ENDPOINT` 使用 HTTP(S) 的服务根地址，不包含桶名、子路径、查询参数或嵌入的凭据；生产部署使用 HTTPS。需要时将 `S3_FORCE_PATH_STYLE=false` 切换为桶域名访问。修改环境后执行 `docker compose up -d gateway` 重新创建容器。

- **未配置 S3**：设置页没有 S3 区块，私钥没有 JSON 存储开关，保持只保存元数据。
- **配置 S3**：设置页展示服务地址、区域和桶名，隐藏访问密钥。点击“测试写入与读取”会向 `_gateway-tests/<UUID>.json` 写入测试 JSON、读回比对并尝试删除测试对象；测试接口 `POST /admin/api/storage/test` 要求管理员会话及 CSRF。权限至少需要桶内对象的 PutObject、GetObject，测试清理另需 DeleteObject。
- 私有 Key 的“JSON 存储配置”（新建/编辑也可设置）分别控制 `store_input`、`store_output`，默认都关闭。配置 S3 **不会自动启用任何私钥的正文存储**。在这里开启后，请求正文会发送到你配置的存储桶。
- 路径为 `私钥name/YYYY-MM/请求UUID-in.json`、`私钥name/YYYY-MM/请求UUID-out.json`。年月按请求开始时间及 `REPORT_TIMEZONE` 计算，重命名私钥后，历史文件保留旧路径；旧版名称中不适合作为单一路径段的字符会替换为连字符。
- `X-Gateway-Request-ID` 响应头、调用记录 `request_uuid` 和 S3 对象文件名一致。每条新的调用记录 `id` 也使用 UUID；多个上游重试记录共用一个 `request_uuid`，归档保留客户端输入及最终响应。
- 普通有效 JSON 保留原始正文。SSE 输出保存为 JSON 包装：`{"format":"sse","data":"原始 SSE 文本","complete":true,"status":200,"request_uuid":"..."}`；中断时 `complete=false`，不会改变向客户端传输的 SSE 字节。无 JSON 正文的请求/响应使用同类包装。认证头、Cookie 和其它 HTTP 头不会写入对象。
- 图片编辑的 multipart 输入保存为 `{"format":"multipart","encoding":"base64","content_type":"multipart/form-data; boundary=...","data":"原始请求体的 base64","request_uuid":"..."}`，文件、mask、重复字段及边界均可无损还原。图片接口的 S3 捕获仍受单方向 32 MiB 限制，较大的图片请求/响应可正常转发，但归档状态会显示超限。
- 单方向最多捕获 32 MiB；超限不上传截断内容，记录状态“超过 32 MiB，未存储”。S3 失败不重放模型调用，也不修改客户端响应；归档在响应刷出后进行有超时的尽力写入，进程中断或失败不会自动重试。调用记录展示输入/输出对象路径及各自状态（待写入、已存储、失败等）。

迁移 `010_storage_and_spend.sql` 增加开关、请求 UUID、对象路径/状态与消费查询索引；PostgreSQL 不保存对话正文。旧记录不回填 UUID 或 S3 文件。删除私钥保留历史调用与 S3 对象，存储桶保留期限由桶生命周期策略管理；数据库备份不包含 S3 对象。
