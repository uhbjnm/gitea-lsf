package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const verifyHeader = "X-LFS-Gateway-Verify"

type VerifyTokens struct {
	secret []byte
}

type verifyClaims struct {
	RepoID    int64
	OID       string
	Size      int64
	ExpiresAt time.Time
}

func NewVerifyTokens(secret string) *VerifyTokens {
	return &VerifyTokens{secret: []byte(secret)}
}

func (v *VerifyTokens) Sign(repoID int64, oid string, size int64, expiresAt time.Time) string {
	payload := fmt.Sprintf("%d:%s:%d:%d", repoID, oid, size, expiresAt.Unix())
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(payload))
	signature := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(signature)
}

func (v *VerifyTokens) Verify(token string, now time.Time) (verifyClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return verifyClaims{}, false
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return verifyClaims{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return verifyClaims{}, false
	}

	mac := hmac.New(sha256.New, v.secret)
	mac.Write(payloadBytes)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return verifyClaims{}, false
	}

	claims, err := parseVerifyPayload(string(payloadBytes))
	if err != nil || now.After(claims.ExpiresAt) {
		return verifyClaims{}, false
	}
	return claims, true
}

func parseVerifyPayload(payload string) (verifyClaims, error) {
	parts := strings.Split(payload, ":")
	if len(parts) != 4 {
		return verifyClaims{}, errors.New("invalid token payload")
	}
	repoID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return verifyClaims{}, err
	}
	size, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return verifyClaims{}, err
	}
	expires, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return verifyClaims{}, err
	}
	return verifyClaims{
		RepoID:    repoID,
		OID:       parts[1],
		Size:      size,
		ExpiresAt: time.Unix(expires, 0),
	}, nil
}
