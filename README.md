# Gitea LFS Gateway

这个服务实现 Git LFS Batch API 的最小可用子集，用来替换 Gitea 内置 LFS：

- `git push`：Git LFS 客户端向网关请求上传动作，网关返回 OSS `PUT` 预签名 URL，文件直传 OSS；上传签名带 `x-oss-forbid-overwrite: true`，避免覆盖已有对象。
- `git pull`：Git LFS 客户端向网关请求下载动作，网关返回 CDN 鉴权 URL，文件直连 CDN 下载。
- Gitea Web 下载：页面里的 LFS 下载 `/media/...` 由网关改写到带原始文件名的 CDN 鉴权 URL。
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
