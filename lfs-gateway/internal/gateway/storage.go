package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

type ObjectStore interface {
	Stat(ctx context.Context, key string) (objectMeta, bool, error)
	PresignPut(ctx context.Context, key string, expires time.Duration) (string, map[string]string, error)
	PresignGet(ctx context.Context, key string, expires time.Duration) (string, map[string]string, error)
}

type OSSStore struct {
	bucket *oss.Bucket
}

func NewOSSStore(cfg Config) (*OSSStore, error) {
	client, err := oss.New(cfg.OSSEndpoint, cfg.OSSAccessKeyID, cfg.OSSAccessKeySecret)
	if err != nil {
		return nil, err
	}
	bucket, err := client.Bucket(cfg.OSSBucket)
	if err != nil {
		return nil, err
	}
	return &OSSStore{bucket: bucket}, nil
}

func (s *OSSStore) Stat(_ context.Context, key string) (objectMeta, bool, error) {
	header, err := s.bucket.GetObjectDetailedMeta(key)
	if err != nil {
		if isOSSNotFound(err) {
			return objectMeta{}, false, nil
		}
		return objectMeta{}, false, err
	}

	size, err := parseContentLength(header)
	if err != nil {
		return objectMeta{}, false, err
	}
	return objectMeta{Size: size}, true, nil
}

func (s *OSSStore) PresignPut(_ context.Context, key string, expires time.Duration) (string, map[string]string, error) {
	headers := map[string]string{
		"Content-Type":           "application/octet-stream",
		"x-oss-forbid-overwrite": "true",
	}
	href, err := s.bucket.SignURL(
		key,
		oss.HTTPPut,
		int64(expires.Seconds()),
		oss.ContentType("application/octet-stream"),
		oss.ForbidOverWrite(true),
	)
	if err != nil {
		return "", nil, err
	}
	return href, headers, nil
}

func (s *OSSStore) PresignGet(_ context.Context, key string, expires time.Duration) (string, map[string]string, error) {
	href, err := s.bucket.SignURL(key, oss.HTTPGet, int64(expires.Seconds()))
	if err != nil {
		return "", nil, err
	}
	return href, nil, nil
}

func parseContentLength(header http.Header) (int64, error) {
	value := header.Get("Content-Length")
	if value == "" {
		return 0, errors.New("oss object missing Content-Length")
	}
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse Content-Length %q: %w", value, err)
	}
	return size, nil
}

func isOSSNotFound(err error) bool {
	var serviceErr oss.ServiceError
	if errors.As(err, &serviceErr) {
		return serviceErr.StatusCode == http.StatusNotFound
	}
	return false
}
