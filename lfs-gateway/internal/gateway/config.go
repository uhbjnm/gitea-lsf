package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr                    string
	GiteaAPIURL             string
	GiteaWebURL             string
	PublicURL               string
	OSSEndpoint             string
	OSSBucket               string
	OSSAccessKeyID          string
	OSSAccessKeySecret      string
	OSSKeyPrefix            string
	OSSKeyStyle             string
	SignExpires             time.Duration
	CDNBaseURL              string
	CDNAuthKey              string
	CDNAuthUID              string
	VerifySecret            string
	ReleaseDirectUpload     bool
	ReleaseAttachmentPrefix string
	ReleasePendingPrefix    string
	ReleaseMaxFileSize      int64
	MetaDBDriver            string
	MetaDBDSN               string
	ReadTimeout             time.Duration
	WriteTimeout            time.Duration
}

func LoadConfig() (Config, error) {
	cfg := Config{
		Addr:           env("LFS_GATEWAY_ADDR", ":8080"),
		GiteaAPIURL:    strings.TrimRight(env("GITEA_API_URL", "http://gitea:3000/api/v1"), "/"),
		PublicURL:      strings.TrimRight(os.Getenv("LFS_PUBLIC_URL"), "/"),
		OSSEndpoint:    strings.TrimSpace(os.Getenv("OSS_ENDPOINT")),
		OSSBucket:      strings.TrimSpace(os.Getenv("OSS_BUCKET")),
		OSSAccessKeyID: firstEnv("OSS_ACCESS_KEY_ID", "ALIYUN_OSS_ACCESS_KEY_ID"),
		OSSAccessKeySecret: strings.TrimSpace(
			firstEnv("OSS_ACCESS_KEY_SECRET", "ALIYUN_OSS_ACCESS_KEY_SECRET"),
		),
		OSSKeyPrefix:            env("OSS_KEY_PREFIX", "gitea-lfs"),
		OSSKeyStyle:             env("OSS_KEY_STYLE", "repo"),
		CDNBaseURL:              strings.TrimRight(os.Getenv("CDN_BASE_URL"), "/"),
		CDNAuthKey:              strings.TrimSpace(os.Getenv("CDN_AUTH_KEY")),
		CDNAuthUID:              env("CDN_AUTH_UID", "0"),
		ReleaseAttachmentPrefix: env("RELEASE_ATTACHMENT_OSS_PREFIX", "gitea/attachments"),
		ReleasePendingPrefix:    env("RELEASE_PENDING_OSS_PREFIX", "gitea/release-upload-pending"),
		MetaDBDriver:            env("LFS_META_DB_DRIVER", "postgres"),
		MetaDBDSN:               strings.TrimSpace(os.Getenv("LFS_META_DB_DSN")),
	}
	cfg.GiteaWebURL = strings.TrimRight(env("GITEA_WEB_URL", defaultGiteaWebURL(cfg.GiteaAPIURL)), "/")

	var err error
	cfg.ReleaseDirectUpload, err = parseBoolEnv("RELEASE_DIRECT_UPLOAD", false)
	if err != nil {
		return Config{}, err
	}
	cfg.ReleaseMaxFileSize, err = parseMegabytesEnv("RELEASE_MAX_FILE_SIZE_MB", 2048)
	if err != nil {
		return Config{}, err
	}
	cfg.SignExpires, err = parseDurationEnv("LFS_SIGN_EXPIRES", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.ReadTimeout, err = parseDurationEnv("LFS_READ_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.WriteTimeout, err = parseDurationEnv("LFS_WRITE_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	if cfg.VerifySecret = os.Getenv("LFS_VERIFY_SECRET"); cfg.VerifySecret == "" {
		cfg.VerifySecret, err = randomSecret()
		if err != nil {
			return Config{}, fmt.Errorf("generate verify secret: %w", err)
		}
	}

	if cfg.GiteaAPIURL == "" {
		return Config{}, errors.New("GITEA_API_URL is required")
	}
	if cfg.GiteaWebURL == "" {
		return Config{}, errors.New("GITEA_WEB_URL is required")
	}
	if cfg.OSSEndpoint == "" {
		return Config{}, errors.New("OSS_ENDPOINT is required")
	}
	if cfg.OSSBucket == "" {
		return Config{}, errors.New("OSS_BUCKET is required")
	}
	if cfg.OSSAccessKeyID == "" {
		return Config{}, errors.New("OSS_ACCESS_KEY_ID is required")
	}
	if cfg.OSSAccessKeySecret == "" {
		return Config{}, errors.New("OSS_ACCESS_KEY_SECRET is required")
	}
	if cfg.SignExpires <= 0 {
		return Config{}, errors.New("LFS_SIGN_EXPIRES must be positive")
	}
	if cfg.OSSKeyStyle != "repo" && cfg.OSSKeyStyle != "gitea" {
		return Config{}, errors.New("OSS_KEY_STYLE must be repo or gitea")
	}
	if cfg.PublicURL != "" {
		if _, err := url.ParseRequestURI(cfg.PublicURL); err != nil {
			return Config{}, fmt.Errorf("LFS_PUBLIC_URL: %w", err)
		}
	}
	if u, err := url.ParseRequestURI(cfg.GiteaWebURL); err != nil {
		return Config{}, fmt.Errorf("GITEA_WEB_URL: %w", err)
	} else if u.Scheme == "" || u.Host == "" {
		return Config{}, errors.New("GITEA_WEB_URL must include scheme and host")
	}
	if cfg.CDNBaseURL != "" {
		u, err := url.ParseRequestURI(cfg.CDNBaseURL)
		if err != nil {
			return Config{}, fmt.Errorf("CDN_BASE_URL: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return Config{}, errors.New("CDN_BASE_URL must include scheme and host")
		}
	}
	if cfg.CDNBaseURL != "" && cfg.CDNAuthKey == "" {
		return Config{}, errors.New("CDN_AUTH_KEY is required when CDN_BASE_URL is set")
	}
	if cfg.MetaDBDSN != "" && cfg.MetaDBDriver != "postgres" {
		return Config{}, errors.New("LFS_META_DB_DRIVER currently only supports postgres")
	}
	if cfg.ReleaseDirectUpload && cfg.MetaDBDSN == "" {
		return Config{}, errors.New("LFS_META_DB_DSN is required when RELEASE_DIRECT_UPLOAD is enabled")
	}
	if cfg.ReleaseDirectUpload && cfg.ReleaseMaxFileSize <= 0 {
		return Config{}, errors.New("RELEASE_MAX_FILE_SIZE_MB must be positive")
	}

	cfg.OSSKeyPrefix = strings.Trim(cfg.OSSKeyPrefix, "/")
	cfg.ReleaseAttachmentPrefix = strings.Trim(cfg.ReleaseAttachmentPrefix, "/")
	cfg.ReleasePendingPrefix = strings.Trim(cfg.ReleasePendingPrefix, "/")
	if cfg.ReleaseDirectUpload && (cfg.ReleaseAttachmentPrefix == "" || cfg.ReleasePendingPrefix == "") {
		return Config{}, errors.New("release OSS prefixes must not be empty")
	}
	if cfg.ReleaseDirectUpload && cfg.ReleaseAttachmentPrefix == cfg.ReleasePendingPrefix {
		return Config{}, errors.New("release attachment and pending OSS prefixes must differ")
	}
	return cfg, nil
}

func defaultGiteaWebURL(apiURL string) string {
	return strings.TrimSuffix(strings.TrimRight(apiURL, "/"), "/api/v1")
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func parseDurationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return duration, nil
}

func parseBoolEnv(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}

func parseMegabytesEnv(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback << 20, nil
	}
	megabytes, err := strconv.ParseInt(value, 10, 64)
	if err != nil || megabytes <= 0 || megabytes > (1<<63-1)>>20 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return megabytes << 20, nil
}

func randomSecret() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
