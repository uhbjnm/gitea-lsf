package gateway

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
)

type DownloadSigner interface {
	SignDownload(ctx context.Context, key string, expires time.Duration, expiresAt time.Time) (string, map[string]string, error)
	SignDownloadWithFilename(ctx context.Context, key, filename string, expires time.Duration, expiresAt time.Time) (string, map[string]string, error)
}

func NewDownloadSigner(cfg Config, fallback ObjectStore) DownloadSigner {
	if cfg.CDNBaseURL == "" {
		return ossDownloadSigner{store: fallback}
	}
	return aliyunCDNSigner{
		baseURL: cfg.CDNBaseURL,
		authKey: cfg.CDNAuthKey,
		uid:     cfg.CDNAuthUID,
	}
}

type ossDownloadSigner struct {
	store ObjectStore
}

func (s ossDownloadSigner) SignDownload(ctx context.Context, key string, expires time.Duration, _ time.Time) (string, map[string]string, error) {
	return s.store.PresignGet(ctx, key, expires)
}

func (s ossDownloadSigner) SignDownloadWithFilename(ctx context.Context, key, _ string, expires time.Duration, expiresAt time.Time) (string, map[string]string, error) {
	return s.SignDownload(ctx, key, expires, expiresAt)
}

type aliyunCDNSigner struct {
	baseURL string
	authKey string
	uid     string
}

func (s aliyunCDNSigner) SignDownload(_ context.Context, key string, expires time.Duration, expiresAt time.Time) (string, map[string]string, error) {
	return s.sign(key, "", expires, expiresAt)
}

func (s aliyunCDNSigner) SignDownloadWithFilename(_ context.Context, key, filename string, expires time.Duration, expiresAt time.Time) (string, map[string]string, error) {
	return s.sign(key, filename, expires, expiresAt)
}

func (s aliyunCDNSigner) sign(key, filename string, expires time.Duration, expiresAt time.Time) (string, map[string]string, error) {
	u, err := url.Parse(s.baseURL)
	if err != nil {
		return "", nil, err
	}

	u.Path = joinURLPath(u.Path, key)
	if filename != "" {
		u.Path = joinURLPath(u.Path, filename)
	}
	uri := u.EscapedPath()
	issuedAt := expiresAt.Add(-expires)
	timestamp := fmt.Sprintf("%d", issuedAt.Unix())
	random, err := randomHex(8)
	if err != nil {
		return "", nil, err
	}

	query := u.Query()
	query.Set("auth_key", aliyunCDNAuthKey(uri, timestamp, random, s.uid, s.authKey))
	u.RawQuery = query.Encode()
	return u.String(), nil, nil
}

func aliyunCDNAuthKey(uri, timestamp, random, uid, privateKey string) string {
	sum := md5.Sum([]byte(fmt.Sprintf("%s-%s-%s-%s-%s", uri, timestamp, random, uid, privateKey)))
	return fmt.Sprintf("%s-%s-%s-%s", timestamp, random, uid, hex.EncodeToString(sum[:]))
}

func joinURLPath(basePath, objectKey string) string {
	basePath = strings.TrimRight(basePath, "/")
	objectKey = strings.TrimLeft(objectKey, "/")
	if basePath == "" {
		return "/" + objectKey
	}
	return path.Clean(basePath + "/" + objectKey)
}

func randomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
