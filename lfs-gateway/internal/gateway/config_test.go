package gateway

import (
	"testing"
	"time"
)

func TestLoadConfigAcceptsAliyunOSSAccessKeyAliases(t *testing.T) {
	t.Setenv("GITEA_API_URL", "http://gitea:3000/api/v1")
	t.Setenv("OSS_ENDPOINT", "https://oss-cn-shenzhen.aliyuncs.com")
	t.Setenv("OSS_BUCKET", "example-lfs-bucket")
	t.Setenv("ALIYUN_OSS_ACCESS_KEY_ID", "test-access-key-id")
	t.Setenv("ALIYUN_OSS_ACCESS_KEY_SECRET", "test-access-key-secret")
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
	t.Setenv("CDN_AUTH_KEY", "test-cdn-key")
	t.Setenv("LFS_VERIFY_SECRET", "test-verify-secret")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OSSAccessKeyID != "test-access-key-id" {
		t.Fatalf("OSSAccessKeyID = %q", cfg.OSSAccessKeyID)
	}
	if cfg.OSSAccessKeySecret != "test-access-key-secret" {
		t.Fatalf("OSSAccessKeySecret = %q", cfg.OSSAccessKeySecret)
	}
	if cfg.SignExpires != 30*time.Minute {
		t.Fatalf("SignExpires = %s", cfg.SignExpires)
	}
}
