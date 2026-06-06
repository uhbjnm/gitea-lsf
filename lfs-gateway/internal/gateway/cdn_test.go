package gateway

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAliyunCDNSignerUsesIssuedAtTimestamp(t *testing.T) {
	signer := aliyunCDNSigner{
		baseURL: "https://cdn.example.com/base",
		authKey: "secret",
		uid:     "0",
	}
	expires := 15 * time.Minute
	expiresAt := time.Unix(1710000900, 0)

	href, _, err := signer.SignDownload(t.Context(), "gitea-lfs/repositories/42/01/23/"+testOID, expires, expiresAt)
	if err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(href)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(u.Query().Get("auth_key"), "-")
	if len(parts) != 4 {
		t.Fatalf("auth_key = %q", u.Query().Get("auth_key"))
	}
	if parts[0] != "1710000000" {
		t.Fatalf("timestamp = %q", parts[0])
	}

	sum := md5.Sum([]byte(fmt.Sprintf("%s-%s-%s-%s-%s", u.EscapedPath(), parts[0], parts[1], parts[2], "secret")))
	if parts[3] != hex.EncodeToString(sum[:]) {
		t.Fatalf("signature = %q", parts[3])
	}
}

func TestAliyunCDNAuthKeyMatchesOfficialExample(t *testing.T) {
	authKey := aliyunCDNAuthKey(
		"/video/standard/test.mp4",
		"1444435200",
		"0",
		"0",
		"aliyuncdnexp1234",
	)
	want := "1444435200-0-0-23bf85053008f5c0e791667a313e28ce"
	if authKey != want {
		t.Fatalf("auth_key = %q, want %q", authKey, want)
	}
}
