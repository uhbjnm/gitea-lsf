# Gitea LFS Gateway

这个服务实现 Git LFS Batch API 的最小可用子集，用来替换 Gitea 内置 LFS：

- `git push`：Git LFS 客户端向网关请求上传动作，网关返回 OSS `PUT` 预签名 URL，文件直传 OSS；上传签名带 `x-oss-forbid-overwrite: true`，避免覆盖已有对象。
- `git pull`：Git LFS 客户端向网关请求下载动作，网关返回 CDN 鉴权 URL，文件直连 CDN 下载。
- 权限校验：网关把客户端的 `Authorization` 头转发到 Gitea API，使用 Gitea 仓库权限决定是否允许上传或下载。
- 元数据同步：verify 成功后写入 Gitea PostgreSQL 的 `lfs_meta_object` 表，避免 Gitea LFS 元数据缺失。
- 默认对象隔离：OSS key 中包含 Gitea 仓库 `id`，避免不同仓库之间只靠相同 LFS OID 互相读取内容。

## 接口范围

已实现：

- `POST /{owner}/{repo}.git/info/lfs/objects/batch`
- `POST /{owner}/{repo}.git/info/lfs/objects/{oid}/verify`
- `POST /{owner}/{repo}.git/info/lfs/locks/verify`，返回空锁集合，用于兼容 Git LFS 客户端的锁探测。
- `GET /{owner}/{repo}/media/{branch|commit|tag}/...`，用于 Gitea Web 页面下载 LFS 文件时跳转到带原始文件名的 CDN 鉴权 URL。
- `POST /{owner}/{repo}/releases/attachments/direct`，为 Gitea Release 页面签发 OSS 直传 URL。
- `POST /{owner}/{repo}/releases/attachments/direct/complete`，校验并登记直传附件。
- `GET|HEAD /{owner}/{repo}/releases/download/{tag}/{filename}`，鉴权后跳转到 CDN。
- `GET /healthz`

未实现：

- LFS Locks API 的创建、查询、解锁。普通 `git lfs push/pull` 不依赖这些接口；如果你的仓库使用 `git lfs lock`，需要后续补完整锁接口。
- tus/resumable transfer。当前只支持 Git LFS `basic` transfer。

## 必需环境变量

```env
GITEA_API_URL=http://gitea:3000/api/v1
GITEA_SECRET_KEY=replace-with-a-long-random-secret
LFS_PUBLIC_URL=https://git.example.com
LFS_META_DB_DRIVER=postgres
LFS_META_DB_DSN=host=postgres port=5432 user=gitea password=your-db-password dbname=gitea sslmode=disable

OSS_ENDPOINT=https://oss-cn-hangzhou.aliyuncs.com
OSS_BUCKET=your-bucket
OSS_KEY_PREFIX=gitea-lfs
OSS_KEY_STYLE=repo

CDN_BASE_URL=https://cdn.example.com
CDN_AUTH_KEY=your-cdn-url-auth-key
CDN_AUTH_UID=0

LFS_VERIFY_SECRET=replace-with-a-long-random-secret
LFS_SIGN_EXPIRES=30m

RELEASE_DIRECT_UPLOAD=true
RELEASE_ATTACHMENT_OSS_PREFIX=gitea/attachments
RELEASE_PENDING_OSS_PREFIX=gitea/release-upload-pending
RELEASE_MAX_FILE_SIZE_MB=5120
```

OSS AccessKey 不建议写入 `gitea/env`。Compose 已配置为从宿主机环境变量透传：

```powershell
$env:ALIYUN_OSS_ACCESS_KEY_ID="..."
$env:ALIYUN_OSS_ACCESS_KEY_SECRET="..."
```

网关同时兼容 `OSS_ACCESS_KEY_ID/OSS_ACCESS_KEY_SECRET` 和 `ALIYUN_OSS_ACCESS_KEY_ID/ALIYUN_OSS_ACCESS_KEY_SECRET`，优先使用前者。

`CDN_BASE_URL` 指向 CDN 域名。如果 CDN 回源到 OSS bucket 根路径，保持 `https://cdn.example.com`；如果 CDN 只回源某个目录，写成 `https://cdn.example.com/path-prefix`。

`LFS_META_DB_DSN` 用来写 Gitea 的 `lfs_meta_object` 表。verify 成功后网关会 upsert 元数据，download 时会读取元数据做 size 校验。为空时网关仍能工作，但 Gitea 页面里的 LFS 统计和历史元数据不会更新。PostgreSQL 用户需要有 `SELECT/INSERT/UPDATE` `lfs_meta_object` 的权限；直接复用 Gitea 数据库用户最简单。

启用 `RELEASE_DIRECT_UPLOAD` 时，`LFS_META_DB_DSN` 也是必需项。网关会在 OSS finalize 后写入 Gitea `attachment` 表中的临时附件元数据；Gitea Release 表单提交时再按原生流程绑定 UUID。网页扩展已验证并固定于 Gitea 1.26.2，升级 Gitea 前必须重新验收 Dropzone 上传流程。

`OSS_KEY_STYLE` 有两个值：

- `repo`：默认值，新对象写入 `OSS_KEY_PREFIX/repositories/{repoID}/{oid[0:2]}/{oid[2:4]}/{oid[4:]}`，按仓库隔离。
- `gitea`：兼容 Gitea 内置 LFS 的全局对象路径 `OSS_KEY_PREFIX/{oid[0:2]}/{oid[2:4]}/{oid[4:]}`。这适合直接复用历史 OSS LFS 对象，但不会按仓库隔离；长期建议迁移到 `repo`。

如果你的 OSS bucket 里已经有 Gitea 内置 LFS 上传过的对象，先用 `OSS_KEY_STYLE=gitea` 验证旧对象能下载；确认新上传链路稳定后，再规划把旧对象迁移到 `repo` 布局。

## Gitea 配置

需要开启 Gitea LFS 服务，让 Gitea Web 页面能识别 LFS pointer 并展示真实大小、`LFS` 标签和下载入口：

```yaml
GITEA__security__INSTALL_LOCK: true
GITEA__security__SECRET_KEY: ${GITEA_SECRET_KEY}
GITEA__server__LFS_START_SERVER: true
```

`INSTALL_LOCK=true` 用来跳过首次安装页并让 Gitea 自动初始化数据库表；`GITEA_SECRET_KEY` 必须是固定随机值，不能每次启动变化。

如果 Gitea 的 `[storage.lfs]` 使用 OSS/S3 兼容存储，Web 页面下载需要让 Gitea 生成直链，再由网关改写到 CDN：

```ini
[storage.lfs]
SERVE_DIRECT = true
```

Git LFS 客户端的 `/info/lfs/*` 请求仍然必须由反向代理转给网关；Gitea 自己的 LFS Batch API 不参与客户端上传下载。

Git LFS 客户端默认请求路径仍是：

```text
https://git.example.com/{owner}/{repo}.git/info/lfs/objects/batch
```

因此反向代理必须把 `*/info/lfs/*` 转发给网关。为了让 Gitea Web 页面里的 LFS 下载也走 CDN，还必须把 `*/media/*` 转发给网关；其它请求仍转发给 Gitea。

当前实现面向 HTTPS remote。SSH remote 的 Git LFS 通常依赖 Gitea 的 `git-lfs-authenticate`，在关闭内置 LFS 后不能直接复用；如果团队仍用 SSH 推代码，需要在仓库或全局 Git 配置中显式设置 HTTPS LFS URL：

```bash
git config lfs.url https://git.example.com/{owner}/{repo}.git/info/lfs
```

### Nginx 示例

完整示例文件见 `gitea/proxy/nginx-lfs-gateway.conf.example`。

```nginx
location ~ ^/[^/]+/[^/]+(\.git)?/info/lfs/ {
    proxy_pass http://lfs-gateway:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header Authorization $http_authorization;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    client_max_body_size 0;
    proxy_request_buffering off;
}

location ~ ^/[^/]+/[^/]+/media/ {
    proxy_pass http://lfs-gateway:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header Cookie $http_cookie;
    proxy_set_header Authorization $http_authorization;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}

location / {
    proxy_pass http://gitea:3000;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

### Caddy 示例

完整示例文件见 `gitea/proxy/Caddyfile.example`。

```caddyfile
git.example.com {
    @lfs path_regexp lfs ^/[^/]+/[^/]+(\.git)?/info/lfs/.*
    reverse_proxy @lfs lfs-gateway:8080

    @lfs_media path_regexp lfs_media ^/[^/]+/[^/]+/media/.*
    reverse_proxy @lfs_media lfs-gateway:8080

    reverse_proxy gitea:3000
}
```

## Docker Compose 启动

本仓库现有配置文件名是 `env`，不是 Docker Compose 默认读取的 `.env`，启动时要显式指定：

```bash
cd gitea
cp env.example env
docker network create gitea-net
docker compose --env-file env up -d --build
```

把 `env` 中的 `change-me`、`your-*`、`cdn.example.com` 替换成真实值。如果 `gitea-net` 已存在，`docker network create` 会报已存在，可忽略。

上线前检查配置是否还含占位值：

```powershell
cd lfs-gateway
./scripts/check-config.ps1
```

如果脚本报 `placeholder`，说明 `env` 里仍有占位值；这时不要上线，否则网关启动或签名会失败。

Gitea/PostgreSQL 容器启动后，检查 LFS 元数据表结构：

```powershell
cd lfs-gateway
./scripts/check-db.ps1
```

该脚本只读检查 `lfs_meta_object` 是否包含必要列，以及是否存在 `repository_id + oid` 唯一索引。

## OSS 与 CDN 要求

1. OSS bucket 需要允许使用 AccessKey 创建 `PUT` 和 `GET` 签名 URL。
2. CDN 需要回源到同一个 OSS bucket，并且 CDN 鉴权方式使用阿里云 URL 鉴权 Type A。
3. CDN 鉴权 key 必须和 `CDN_AUTH_KEY` 一致。
4. CDN 控制台里的“鉴权 URL 有效时长”建议设置成和 `LFS_SIGN_EXPIRES` 一致。Type A 的 `auth_key` timestamp 是签发时间，实际失效时间由 CDN 控制台有效时长决定。
5. 如果 OSS bucket 不公开，CDN 回源需要配置 OSS 私有 bucket 回源鉴权。
6. 为了让 Gitea Web 下载保留原始文件名，CDN 需要配置回源 Path 重写。网关会把 Web 下载 URL 签成 `/{object-key}/{filename}?auth_key=...`，浏览器因此能用最后一段路径作为保存文件名；CDN 回源时需要把最后的文件名段去掉，回源到真实 OSS key。

阿里云 CDN 回源 Path 重写示例：

```text
待重写 Path:
^/(gitea/lfs/[0-9a-f][0-9a-f]/[0-9a-f][0-9a-f]/[0-9a-f]+)/[^/]+$

目标 Path:
/$1

执行规则:
break
```

如果使用 `OSS_KEY_STYLE=repo`，按实际 OSS key 前缀调整正则，例如：

```text
^/(gitea-lfs/repositories/[0-9]+/[0-9a-f][0-9a-f]/[0-9a-f][0-9a-f]/[0-9a-f]+)/[^/]+$
```

## 安全边界

直传 OSS 的代价是网关不再接收文件内容，所以无法在上传时计算 SHA-256。当前 verify 会校验对象存在和大小；客户端在下载后仍会按 LFS OID 校验内容哈希。不要把 OSS 写权限开放给网关以外的身份，避免有人绕过 LFS Batch API 写入对象。

## 客户端验证

先做不依赖真实 OSS/CDN 的本地协议验证：

```powershell
cd lfs-gateway
./scripts/git-lfs-client-flow.ps1
```

期望日志里出现：

```text
PUT /oss/<oid> header=true
GET /cdn/<oid>?auth_key=test
```

这证明真实 `git-lfs` 客户端上传数据面走 OSS URL，下载数据面走 CDN URL。

在任意仓库执行：

```bash
git lfs install
git lfs track "*.zip"
git add .gitattributes large.zip
git commit -m "test lfs direct upload"
GIT_TRACE=1 GIT_CURL_VERBOSE=1 git push
GIT_TRACE=1 GIT_CURL_VERBOSE=1 git lfs pull
```

验证点：

- `git push` 日志里大文件上传目标应是 OSS 域名。
- `git lfs pull` 日志里大文件下载目标应是 CDN 域名，并带 `auth_key` 参数。
- Gitea 服务器带宽不应再承担 LFS 对象内容上传/下载，只承担 Batch API 和 verify 小请求。

也可以用脚本创建临时测试分支并自动检查 trace：

```powershell
cd lfs-gateway
./scripts/check-live-lfs-flow.ps1 `
  -RemoteUrl "https://git.example.com/owner/repo.git" `
  -Branch "lfs-gateway-test"
```

脚本会在临时本地仓库提交一个 LFS 文件、推送到测试分支、删除本地 LFS 对象后重新 fetch，并检查日志里是否同时包含 OSS 上传域名、CDN 下载域名和 `auth_key`。
