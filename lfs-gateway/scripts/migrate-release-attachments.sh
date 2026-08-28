#!/bin/sh
set -eu

: "${ALIYUN_OSS_ACCESS_KEY_ID:?ALIYUN_OSS_ACCESS_KEY_ID is required}"
: "${ALIYUN_OSS_ACCESS_KEY_SECRET:?ALIYUN_OSS_ACCESS_KEY_SECRET is required}"
: "${GITEA_ATTACHMENT_OSS_ENDPOINT:?GITEA_ATTACHMENT_OSS_ENDPOINT is required}"
: "${GITEA_ATTACHMENT_OSS_LOCATION:?GITEA_ATTACHMENT_OSS_LOCATION is required}"
: "${OSS_BUCKET:?OSS_BUCKET is required}"

attachment_prefix=${RELEASE_ATTACHMENT_OSS_PREFIX:-gitea/attachments}

docker exec \
  -e MINIO_ACCESS_KEY="$ALIYUN_OSS_ACCESS_KEY_ID" \
  -e MINIO_SECRET_KEY="$ALIYUN_OSS_ACCESS_KEY_SECRET" \
  gitea gitea migrate-storage \
  --type attachments \
  --storage minio \
  --minio-endpoint "$GITEA_ATTACHMENT_OSS_ENDPOINT" \
  --minio-bucket "$OSS_BUCKET" \
  --minio-location "$GITEA_ATTACHMENT_OSS_LOCATION" \
  --minio-base-path "$attachment_prefix/" \
  --minio-use-ssl \
  --minio-bucket-lookup-type dns
