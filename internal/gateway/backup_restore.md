# AI 网关异地恢复说明

备份包含 PostgreSQL 数据、原 MASTER_KEY、运行设置及 S3 连接设置。
用户密码、TOTP、上游/私有密钥、OAuth 账户、代理、日志与费用记录随数据库恢复。
登录会话、登录挑战、未完成的 OAuth 授权、进行中的并发名额及备份租约不恢复，需要重新登录。
S3 桶内的请求 JSON、域名配置、Nginx 配置和 HTTPS 证书需另外备份或重新配置。

## 1. 解密备份

在新服务器安装 Docker Engine 和 Docker Compose v2，准备本项目的 docker-compose.yaml。
使用包含备份功能且不旧于备份源的网关镜像，并使用备份对应的 PostgreSQL 大版本或更新版本。
项目默认使用 PostgreSQL 17。备份的 PostgreSQL 版本和迁移列表可在解包后的 manifest.json 中查看。

从存储桶 backup-data/YYYY-MM/ 下载需要的完整日期备份，或使用后台手动下载的文件。
将其放在当前目录，下面以重命名后的 gateway-backup.aigw 为例。定时备份使用当时保存的定时备份密码；
手动下载使用生成时输入的密码。修改密码不会改变已有文件的解密密码。
下面的命令适用于 Bash 和 Zsh；先拉取明确版本，避免使用服务器上缓存的旧镜像。
如备份来自更新的网关版本，请改用不低于源站的版本。按顺序执行：

```bash
docker pull ghcr.io/guohai163/openai-gateway:1.25.1
printf '备份密码: '
IFS= read -r -s BACKUP_PASSWORD
printf '\n'
export BACKUP_PASSWORD
docker run --rm --user "$(id -u):$(id -g)" -e BACKUP_PASSWORD -v "$PWD:/restore" ghcr.io/guohai163/openai-gateway:1.25.1 backup-extract --file /restore/gateway-backup.aigw --out /restore/restored
unset BACKUP_PASSWORD
```

输入密码时不会显示字符，输入完成后按回车。`gateway-backup.aigw` 应替换成实际下载的文件名，
或先在当前目录将文件重命名；`restored` 输出目录必须不存在。
如果看到 `MASTER_KEY must be a base64 encoded 32-byte key`，通常是运行了不支持解包的旧镜像，
或 `backup-extract` 没有作为 `docker run` 的参数传入。请确认镜像版本，并整行复制上面的命令。

也可先按上方方法输入并导出 BACKUP_PASSWORD，再用同版本或更新版本的本机二进制：

```bash
./gateway backup-extract \
  --file gateway-backup.aigw --out restored
unset BACKUP_PASSWORD
```

输出目录必须不存在；密码错误或文件被截断、篡改时不会生成恢复目录。
解包后包含 database.dump、.env.restore、manifest.json、RESTORE.md。
恢复目录中的配置含解密主密钥，应限制访问。

## 2. 准备新的空站点

只在新的空站点执行。若目标已有数据，先为目标站点另做备份。
将恢复配置放到项目目录；不要用 source 执行该文件。

```bash
cp restored/.env.restore .env
chmod 600 .env
```

检查 .env 中的 GATEWAY_IMAGE（应包含备份功能且不旧于备份源）、TRUSTED_PROXIES、
COOKIE_SECURE、BIND_ADDRESS 和 S3 设置是否适合新服务器。MASTER_KEY 必须保留恢复值。
POSTGRES_USER/POSTGRES_DB 默认为 openai，数据库密码已重新随机生成，用于新数据库初始化。
ADMIN_PASSWORD 也重新随机生成，但恢复后实际登录密码和 TOTP 沿用原数据库账户。
不要在已有 PostgreSQL 数据卷上直接替换数据库密码。

```bash
docker compose pull
docker compose up -d --wait postgres
```

## 3. 导入数据库并启动网关

保持网关停止，只启动 PostgreSQL。不要先运行网关的自动迁移。
导入采用单个事务，失败时不会留下部分恢复数据。

```bash
docker compose stop gateway
docker compose exec -T postgres sh -c \
  'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --no-privileges --exit-on-error --single-transaction' \
  < restored/database.dump
docker compose up -d gateway
docker compose ps
curl --fail http://127.0.0.1:8080/healthz
```

恢复成功后，使用原管理员账号、密码和 TOTP 登录。检查私有密钥、代理配置、模型列表和历史记录。
如果源数据库使用自定义 schema/search_path，目标 DATABASE_URL 也须配置相同的 search_path。
确认反向代理与域名证书、可信代理 IP、上游允许 IP 和代理出口符合新服务器环境，再切换流量。

## 4. OAuth 与对象存储

灾难恢复时，旧备份中的 OAuth Refresh Token 可能已被源站轮换或撤销，此时在 OAuth 菜单重新授权。
计划迁移时，停止客户端写入，下载最后一次备份后立即停止旧网关，避免新旧站点同时续期同一 OAuth 账户。
备份之后发生的请求和配置变更不会包含在该备份中。

S3 输入/输出正文不在本备份中。原存储桶仍可访问时可继续使用；迁移存储桶时须另行复制所有对象，
保留对象路径，并在新站点 .env 中更新相应的 S3 连接设置。
请求 JSON 位于 key-json-data/，站点备份位于 backup-data/YYYY-MM/。
旧备份中的历史 JSON 路径会兼容读取已迁移的对象，并在后台继续迁移。

定时备份的频率、时间、保留天数、站点标识和已加密的密码也会恢复。
检查系统设置中的配置与报表时区，确认目标桶的 List/Get/Put/Delete 权限可用。
新站点继续使用原站点标识，因此保留清理会处理同一桶中原站点超过保留天数的备份。
恢复快照中尚在运行的任务会显示“已中断”，不会恢复旧租约。

备份密码无法找回。保存好加密备份文件和密码，并在启用前做一次异地恢复演练。
