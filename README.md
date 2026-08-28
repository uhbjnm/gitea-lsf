# Gitea LFS Gateway

这个服务实现 Git LFS Batch API 的最小可用子集，用来替换 Gitea 内置 LFS：

- `git push`：Git LFS 客户端向网关请求上传动作，网关返回 OSS `PUT` 预签名 URL，文件直传 OSS；上传签名带 `x-oss-forbid-overwrite: true`，避免覆盖已有对象。
- `git pull`：Git LFS 客户端向网关请求下载动作，网关返回 CDN 鉴权 URL，文件直连 CDN 下载。
- Gitea Web 下载：页面里的 LFS 下载 `/media/...` 由网关改写到带原始文件名的 CDN 鉴权 URL。
- Release 上传：Gitea 网页中的 Release 文件通过 OSS 预签名 URL 直传，不经过 Gitea/Gateway 数据面。
- Release 下载：Gitea 完成权限校验后，由网关改写到带原始文件名的 CDN 鉴权 URL。
- 权限校验：网关把客户端的 `Authorization` 或浏览器 `Cookie` 转发给 Gitea，使用 Gitea 仓库权限决定是否允许上传或下载。
- 元数据同步：verify 成功后写入 Gitea PostgreSQL 的 `lfs_meta_object` 表，避免 Gitea LFS 元数据缺失。

## 接口范围

已实现：

- `POST /{owner}/{repo}.git/info/lfs/objects/batch`
- `POST /{owner}/{repo}.git/info/lfs/objects/{oid}/verify`
- `POST /{owner}/{repo}.git/info/lfs/locks/verify`
- `GET /{owner}/{repo}/media/{branch|commit|tag}/...`
- `GET /healthz`

未实现：

- LFS Locks API 的创建、查询、解锁。
- tus/resumable transfer。当前只支持 Git LFS `basic` transfer。

## 关键配置

```env
GITEA_API_URL=http://gitea:3000/api/v1
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
```

OSS AccessKey 不建议写入配置文件。Compose 可从宿主机环境变量透传：

```powershell
$env:ALIYUN_OSS_ACCESS_KEY_ID="..."
$env:ALIYUN_OSS_ACCESS_KEY_SECRET="..."
```

`OSS_KEY_STYLE`：

- `repo`：新对象写入 `OSS_KEY_PREFIX/repositories/{repoID}/{oid[0:2]}/{oid[2:4]}/{oid[4:]}`，按仓库隔离。
- `gitea`：兼容 Gitea 内置 LFS 的全局对象路径 `OSS_KEY_PREFIX/{oid[0:2]}/{oid[2:4]}/{oid[4:]}`。

## Gitea 配置

Gitea Web 需要开启 LFS 服务以识别 pointer：

```yaml
GITEA__server__LFS_START_SERVER: true
```

如果 Gitea 的 `[storage.lfs]` 使用 OSS/S3 兼容存储，Web 页面下载需要让 Gitea 生成直链，再由网关改写到 CDN：

```ini
[storage.lfs]
SERVE_DIRECT = true
```

Git LFS 客户端的 `/info/lfs/*` 请求仍然由反向代理转给网关；Gitea 自己的 LFS Batch API 不参与客户端上传下载。

## Nginx 路由

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
```

完整示例见 `gitea/proxy/nginx-lfs-gateway.conf.example`。

## CDN 回源 Path 重写

为了让 Gitea Web 下载保留原始文件名，网关会把 Web 下载 URL 签成：

```text
/{object-key}/{filename}?auth_key=...
```

浏览器会用最后一段路径作为保存文件名；CDN 回源时需要把最后的文件名段去掉，回源到真实 OSS key。

阿里云 CDN 回源 Path 重写示例：

```text
待重写 Path:
^/(gitea/lfs/[0-9a-f][0-9a-f]/[0-9a-f][0-9a-f]/[0-9a-f]+)/[^/]+$

目标 Path:
/$1

执行规则:
break
```

如果使用 `OSS_KEY_STYLE=repo`，按实际 OSS key 前缀调整正则。

## 验证

```powershell
cd lfs-gateway
go test ./...
```

真实仓库验证：

```bash
git lfs install
git lfs track "*.zip"
git add .gitattributes large.zip
git commit -m "test lfs direct upload"
GIT_TRACE=1 GIT_CURL_VERBOSE=1 git push
GIT_TRACE=1 GIT_CURL_VERBOSE=1 git lfs pull
```

期望：

- `git push` 的 LFS 对象上传目标是 OSS 域名。
- `git lfs pull` 的 LFS 对象下载目标是 CDN 域名，并带 `auth_key`。
- Gitea Web 页面显示 LFS 文件真实大小和 `LFS` 标签。
- Gitea Web 下载 `/media/.../filename` 跳转到 CDN，且 CDN URL 末尾保留原始文件名。

## Release 直传 OSS

Release 页面使用 Gitea 原生临时附件 UUID 流程，但由自定义 Dropzone 传输层完成两阶段上传：

1. 浏览器向网关申请 OSS 预签名 `PUT` URL。
2. 浏览器把文件直接上传到 `RELEASE_PENDING_OSS_PREFIX`。
3. 网关校验 OSS 对象大小，在 OSS 内部复制到 `RELEASE_ATTACHMENT_OSS_PREFIX`。
4. 网关写入未绑定的 Gitea `attachment` 元数据并向页面返回 UUID。
5. 用户提交 Release 表单后，Gitea 原生逻辑把 UUID 绑定到 Release。

该实现已固定并验证 `Gitea 1.26.2`。升级 Gitea 前必须重新验证 Release 页面上传组件。

`GITEA_ATTACHMENT_OSS_ENDPOINT` 必须使用阿里云 S3 兼容端点（例如 `s3.oss-cn-hangzhou.aliyuncs.com`），不能使用 OSS 原生端点 `oss-cn-hangzhou.aliyuncs.com`。

Gitea 1.26.2 使用的 MinIO SDK 实际读取 `MINIO_ACCESS_KEY/MINIO_SECRET_KEY`。Compose 已将宿主机的 `ALIYUN_OSS_ACCESS_KEY_ID/ALIYUN_OSS_ACCESS_KEY_SECRET` 映射为这两个变量，避免把凭据写入 `app.ini`。

### OSS CORS

Release 直传需要在 OSS bucket 配置：

```text
AllowedOrigin: https://git.example.com
AllowedMethod: PUT
AllowedHeader: Content-Type, x-oss-forbid-overwrite
ExposeHeader: ETag
```

同时为 `RELEASE_PENDING_OSS_PREFIX` 配置生命周期规则，建议 1 天后自动删除，清理关闭页面或上传中断留下的 pending 对象。

### 历史附件迁移

切换 Gitea `[attachment]` 到 OSS 前先备份 PostgreSQL 和本地附件目录，然后在旧的本地 attachment 配置仍生效时执行：

```bash
sh lfs-gateway/scripts/migrate-release-attachments.sh
```

该命令会复制所有 Release、Issue 和 PR 附件，不删除本地源文件。确认历史附件能从 OSS 下载后，再启用 Compose 中的 `GITEA__attachment__STORAGE_TYPE=minio` 配置。

### Release CDN 文件名

Gateway 在 OSS finalize 时给正式对象写入标准 `Content-Disposition`，然后使用真实 attachment key 生成 CDN URL：

```text
/gitea/attachments/a/b/{uuid}?auth_key=...
```

因此 Release 下载不需要额外的 CDN Path 重写，浏览器从响应头获得原文件名。历史迁移对象没有该元数据时，Gateway 会在首次下载前自动补齐。

## AI Agent 发布 Release

为 Agent 创建专用 Gitea 用户并授予目标仓库写权限，Access Token 至少需要 `read:user,write:repository` scope。Gateway 使用 `read:user` 获取可信 uploader ID，使用 `write:repository` 校验仓库和 Release。Token 只能通过环境变量或 Secret Store 注入。

```bash
export GITEA_URL="https://git.example.com"
export GITEA_TOKEN="..."

python lfs-gateway/scripts/publish_release_direct.py \
  --owner owner \
  --repo repo \
  --tag v1.2.3 \
  --target main \
  --title "v1.2.3" \
  --notes "Release notes" \
  ./dist/app.zip ./dist/app.sha256
```

脚本仅使用 Python 标准库，大文件按 1 MiB 分块流式上传，不会整体读入内存。它先通过 Gitea API 创建 draft Release，再调用 Gateway 获取 OSS 预签名 URL，文件直传 OSS。所有附件完成并通过 Gitea API 校验后，脚本才把 draft 发布为正式 Release。中途失败时 Release 保持 draft，不会发布附件不完整的版本。

Agent 模式必须在直传请求中传入 Gitea 返回的 `release_id`；网页 Cookie 模式不传该字段，两种流程共用相同 endpoint 且互不混用。不要调用 Gitea 标准 `POST /api/v1/repos/{owner}/{repo}/releases/{id}/assets`，该接口会让文件内容经过 Gitea服务器。
