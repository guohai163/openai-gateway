# AI 网关 / AI Gateway

[![CI & Container Image](https://github.com/guohai163/openai-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/guohai163/openai-gateway/actions/workflows/ci.yml)

源码：[guohai163/openai-gateway](https://github.com/guohai163/openai-gateway) · 镜像：`ghcr.io/guohai163/openai-gateway`

Go + PostgreSQL 实现的 OpenAI 兼容私有网关，带中英文管理后台。固定上游 `https://api.openai.com`；客户端只接触网关私有 Key。

## 功能

- 直接添加上游普通 API Key，自动同步并保存完整模型列表；共享全部可用上游密钥池。
- 按可用模型轮询上游 Key，明确拒绝时最多尝试 3 个不同 Key。
- 私有 Key 独立 IPv4/IPv6/CIDR 白名单、模型权限、到期与撤销。`*` 表示所有当前可用模型。
- Chat Completions、Embeddings、Responses 和 SSE；Responses 资源归属及多轮上下文持久化绑定。
- 按上游 Key 记录额度阻断、认证错误与限流，支持定时小额探测自动恢复。
- AES-256-GCM 加密上游凭据；私有 Key 保存 SHA-256 哈希；管理员密码 bcrypt；服务端会话、CSRF 和登录限速。
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
3. 颁发私有 Key，名称仅支持小写字母、数字和连字符（`-`），最多 120 个字符；IP 白名单默认按当前请求者 IPv4 `/24` 或 IPv6 `/64` 自动填写，可手动修改。从全部已同步上游 Key 的模型去重列表中搜索、多选，或选择全部模型。有效期仅支持 30、90、120 天，默认 30 天，从创建或续期时计算。新密钥统一以 `gk-ai-` 开头，明文只展示一次；客户端 `base_url` 使用 `https://你的域名/v1`。

私有密钥操作栏和新密钥弹窗均提供“如何使用”，可生成发送 `hi` 的 curl 示例，自动使用当前网关地址。刚创建的密钥会直接带入示例；之后可填写保存的完整密钥，或通过 `GATEWAY_API_KEY` 环境变量使用。密钥输入仅保留在当前弹层，不会保存或上传。模型可修改，未授权或未同步时会提示；从 IP 白名单允许的设备执行命令。既有旧前缀密钥仍可正常认证。

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
- `DELETE /gateway_keys/{id}` 删除私有密钥，要求管理员会话和 CSRF。删除会撤销原凭据、停用并隐藏该密钥，保留历史调用、用量与 Responses 归属；已删除密钥不可通过编辑重新启用。

网关错误格式：`{"error":{"message":"model_not_allowed","type":"gateway_error","param":null,"code":"model_not_allowed"}}`。客户端应读取稳定的 `error.code`；后台将已知错误映射为所选语言。

## 模型定价

管理菜单中的“模型定价”页面提供 OpenAI 官方价格的手动同步。点击“同步官方价格”后，服务端从 [官方价格页的 Markdown](https://developers.openai.com/api/docs/pricing.md) 读取 **Standard 文本 Token 和向量模型**价格，无须提供 Admin Key，也不会发送推理请求。

- PostgreSQL 的 `model_pricing` 表以 `(platform, model)` 为联合主键，同名模型在不同平台的价格独立。当前同步器只支持 `openai`。
- 金额使用 `NUMERIC(24,10)`，单位为 USD / 百万 Token。分别保存输入、输出、缓存输入，以及长上下文输入、输出和缓存输入单价；向量模型没有输出计费时显示“不适用”。
- `long_context_policy` 区分无公开分档、已知分档和规则未知；`long_context_threshold` 保存实际输入 Token 阈值，`long_context_surcharge` 表示长上下文输入或输出单价是否更高。**200K 不是通用加价阈值**：若官方标注超过 272K 后加价，就保存并显示 272000。
- 上游 `/v1/models` 同步成功时自动建立尚未匹配价格的记录。价格按精确模型 ID 匹配，不用名称前缀推断快照模型的价格；缺失价格、当前来源未再匹配或上下文规则无法确认的记录标红，支持筛选查看。
- 页面分别显示官方已公布价格和是否已在本机上游模型列表中发现。模型没有价格不会阻止正常转发，也不会被当作免费模型。
- 同步以事务更新，平台互不覆盖。网络失败、页面结构不再识别或出现相互冲突的 Standard 价格时保留旧数据并显示错误；官方网页解析并非官方价格 API。
- Batch、Flex、Fast、图像、音频和按分钟/张收费的数据不会混入此文本单价表。不支持自动解析的模型显示待确认；这里只维护价格资料，不增加收费或扣减逻辑。

管理接口：`GET /admin/api/pricing?platform=openai&q=模型ID&missing=true&limit=100&offset=0`；`POST /admin/api/pricing/sync?platform=openai`。均需管理员会话，手动同步还需 CSRF Token；平台同步记录保存在 `model_pricing_sync` 表。

本次迁移 `007_model_pricing.sql` 自动创建定价表并为已有模型建立待定价记录；网关项目管理接口和菜单已移除，旧项目资料归档为 `archived_gateway_projects`，上游请求完全使用密钥自身范围。历史迁移文件仍保留，保证从早期版本升级可重放迁移。升级保持原数据卷和 `MASTER_KEY`，无须重新导入密钥。

## 调度、额度与统计

- 独立请求按模型轮询所有符合条件的 Key。相同 OpenAI 项目/组织中的 Key 共享上游配额及限流，不会增加上游实际容量。
- 首次模型同步失败的 Key 不参与调度，后续同步失败保留旧列表和原始响应。每次成功同步事务保存完整模型响应（含模型元数据），可调度列表排除已下线模型。正常每 10 分钟同步，可手动刷新。
- 仅上游明确拒绝的认证、额度、限流及模型不存在错误可触发候选切换。网络超时、5xx 或流已开始均不自动重放，避免重复执行或收费。
- `credit_balance_exhausted`、组织/项目支出限制和 `insufficient_quota` 均只阻断返回错误的 Key，不影响其他 Key。限流按 Key + 模型冷却并遵守 `Retry-After`；没有上游组织归属信息时，不推断多个 Key 的共享额度。
- 恢复探测按受阻 Key 执行；失败后间隔为 5、10、20、40、60 分钟并封顶。探测模型由该 Key 配置，必须支持 Responses，并限制 `max_output_tokens=64`、`store=false`。探测会产生少量费用，记录为“恢复探测”；手动停用和凭据无效不自动恢复。
- 概览和调用记录仅统计本网关返回的 Token usage；流式接口缺少终态 usage 时不估算。Response 轮询及重复流按 Response ID 去重。不查询或展示余额、官方账单及费用。

官方参考：[模型列表](https://developers.openai.com/api/reference/resources/models/methods/list)、[错误码](https://developers.openai.com/api/docs/guides/error-codes)。

## GitHub Actions 自动构建镜像

工作流位于 [.github/workflows/ci.yml](.github/workflows/ci.yml)，在 GitHub 的 [Actions 页面](https://github.com/guohai163/openai-gateway/actions/workflows/ci.yml) 查看日志、发布标签和镜像摘要。

1. PR、`main` 分支提交、`v*` 标签及手动触发都会执行前端测试与构建、PostgreSQL 集成与竞态测试、Go 静态分析、两种 Compose 配置校验和 Docker 镜像启动检查。
2. 检查通过后，`main` 分支和 `v*` 标签会构建 `linux/amd64`、`linux/arm64` 镜像，并推送到 GHCR；PR 只验证，不发布镜像。
3. 工作流使用 GitHub 自动提供的 `GITHUB_TOKEN`，镜像发布任务具有 `packages: write`，不需要配置 Docker Hub 账号或另存发布令牌。

| 触发来源 | 镜像标签示例 |
| --- | --- |
| `main` 分支 | `latest`、`sha-<完整提交SHA>` |
| `v1.2.3` 标签 | `v1.2.3`、`1.2.3`、`1.2`、`sha-<完整提交SHA>` |
| Actions 手动执行（选择 `main`） | `latest`、`sha-<完整提交SHA>` |

`latest` 跟随通过检查的 `main` 分支。正式部署可以将 `GATEWAY_IMAGE` 固定为某个版本、`sha-...` 标签或 `ghcr.io/guohai163/openai-gateway@sha256:...` 摘要。首次构建需要等待 Actions 成功后再拉取。

发布新版本的示例（用实际版本号替换）：

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

数据库卷保存配置、密钥密文、会话、响应归属、日志与统计。必须同时保管 `MASTER_KEY`；丢失后无法解密上游凭据。

```sh
docker compose exec -T postgres pg_dump -U gyai -d gyai -Fc > gyai.dump
# 在新建空数据库中恢复，恢复时先停止网关。
docker compose stop gateway
cat gyai.dump | docker compose exec -T postgres pg_restore -U gyai -d gyai --clean --if-exists
docker compose up -d gateway
```

迁移在启动时执行，使用版本表和数据库锁，升级前备份。日志和响应归属首版不自动删除，请按容量规划保留时间；删除归属记录会使对应响应无法续接。会话和过期冷却记录自动清理。

首版运行一个 Go 实例。轮询游标与登录限速在进程内；状态、额度、探测计划和会话在 PostgreSQL。需要多副本时应先增加共享调度、探测锁和分布式登录限速。
