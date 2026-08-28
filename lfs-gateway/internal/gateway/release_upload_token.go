package gateway

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type releaseUploadClaims struct {
	RepoID     int64  `json:"repo_id"`
	UploaderID int64  `json:"uploader_id"`
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ExpiresAt  int64  `json:"expires_at"`
}

type ReleaseUploadTokens struct {
	secret []byte
}

func NewReleaseUploadTokens(secret string) *ReleaseUploadTokens {
	return &ReleaseUploadTokens{secret: []byte(secret)}
}

func (t *ReleaseUploadTokens) Sign(claims releaseUploadClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, t.secret)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (t *ReleaseUploadTokens) Verify(token string, now time.Time) (releaseUploadClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return releaseUploadClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return releaseUploadClaims{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return releaseUploadClaims{}, false
	}
	mac := hmac.New(sha256.New, t.secret)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return releaseUploadClaims{}, false
	}
	var claims releaseUploadClaims
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ExpiresAt <= now.Unix() {
		return releaseUploadClaims{}, false
	}
	if claims.RepoID <= 0 || claims.UploaderID <= 0 || claims.Size < 0 ||
		!uuidPattern.MatchString(claims.UUID) || claims.Name == "" {
		return releaseUploadClaims{}, false
	}
	return claims, true
}

func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32], nil
}
