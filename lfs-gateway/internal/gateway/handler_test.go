package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testOID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeRepoClient struct {
	info          repoInfo
	err           error
	mediaRedirect *url.URL
	mediaStatus   int
	mediaHeader   http.Header
	mediaBody     string
}

func (c fakeRepoClient) GetRepo(_ context.Context, _ string, _ string, _ string) (repoInfo, error) {
	return c.info, c.err
}

func (c fakeRepoClient) GetMedia(_ context.Context, _ http.Header, _ string) (*mediaResponse, error) {
	if c.mediaStatus == 0 {
		c.mediaStatus = http.StatusOK
	}
	return &mediaResponse{
		StatusCode: c.mediaStatus,
		Header:     c.mediaHeader,
		Body:       io.NopCloser(strings.NewReader(c.mediaBody)),
		Redirect:   c.mediaRedirect,
	}, nil
}

type fakeStore struct {
	objects map[string]objectMeta
}

func (s fakeStore) Stat(_ context.Context, key string) (objectMeta, bool, error) {
	meta, ok := s.objects[key]
	return meta, ok, nil
}

func (s fakeStore) PresignPut(_ context.Context, key string, _ time.Duration) (string, map[string]string, error) {
	return "https://oss.example.com/" + key, map[string]string{
		"Content-Type":           "application/octet-stream",
		"x-oss-forbid-overwrite": "true",
	}, nil
}

func (s fakeStore) PresignGet(_ context.Context, key string, _ time.Duration) (string, map[string]string, error) {
	return "https://oss.example.com/" + key, nil, nil
}

type fakeMetaStore struct {
	objects map[string]objectMeta
}

func (s fakeMetaStore) Get(_ context.Context, repoID int64, oid string) (objectMeta, bool, error) {
	meta, ok := s.objects[metaKey(repoID, oid)]
	return meta, ok, nil
}

func (s fakeMetaStore) Upsert(_ context.Context, repoID int64, obj objectRequest) error {
	s.objects[metaKey(repoID, obj.OID)] = objectMeta{Size: obj.Size}
	return nil
}

func TestUploadBatchReturnsOSSUploadAndVerifyActions(t *testing.T) {
	handler := testHandler(t, fakeStore{objects: map[string]objectMeta{}}, repoWithPermissions(true, true))
	req := newBatchRequest(t, "upload", testOID, 12)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp batchResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Objects) != 1 {
		t.Fatalf("objects = %d", len(resp.Objects))
	}
	obj := resp.Objects[0]
	if obj.Error != nil {
		t.Fatalf("unexpected object error: %+v", obj.Error)
	}
	if obj.Actions["upload"].Href == "" {
		t.Fatal("missing upload href")
	}
	if obj.Actions["upload"].Header["Content-Type"] != "application/octet-stream" {
		t.Fatalf("upload header = %#v", obj.Actions["upload"].Header)
	}
	if obj.Actions["upload"].Header["x-oss-forbid-overwrite"] != "true" {
		t.Fatalf("upload header = %#v", obj.Actions["upload"].Header)
	}
	if obj.Actions["upload"].ExpiresIn <= 0 {
		t.Fatalf("upload expires_in = %d", obj.Actions["upload"].ExpiresIn)
	}
	if !strings.HasPrefix(obj.Actions["verify"].Href, "https://git.example.com/") {
		t.Fatalf("verify href = %q", obj.Actions["verify"].Href)
	}
	if obj.Actions["verify"].Header[verifyHeader] == "" {
		t.Fatal("missing verify token")
	}
}

func TestDownloadBatchReturnsCDNAction(t *testing.T) {
	key := objectKey("gitea-lfs", "repo", 42, testOID)
	store := fakeStore{objects: map[string]objectMeta{key: {Size: 12}}}
	handler := testHandler(t, store, repoWithPermissions(false, true))
	req := newBatchRequest(t, "download", testOID, 12)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp batchResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	href := resp.Objects[0].Actions["download"].Href
	if !strings.HasPrefix(href, "https://cdn.example.com/lfs/gitea-lfs/repositories/42/01/23/"+testOID[4:]) {
		t.Fatalf("download href = %q", href)
	}
	if !strings.Contains(href, "auth_key=") {
		t.Fatalf("download href missing auth_key: %q", href)
	}
}

func TestUploadRequiresPushPermission(t *testing.T) {
	handler := testHandler(t, fakeStore{objects: map[string]objectMeta{}}, repoWithPermissions(false, true))
	req := newBatchRequest(t, "upload", testOID, 12)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestBatchRequiresAuthentication(t *testing.T) {
	handler := testHandler(t, fakeStore{objects: map[string]objectMeta{}}, repoWithPermissions(true, true))
	req := newBatchRequest(t, "download", testOID, 12)
	req.Header.Del("Authorization")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if req := rr.Header().Get("LFS-Authenticate"); req != `Basic realm="Git LFS"` {
		t.Fatalf("LFS-Authenticate = %q", req)
	}
}

func TestBatchRejectsUnsupportedHashAlgorithm(t *testing.T) {
	handler := testHandler(t, fakeStore{objects: map[string]objectMeta{}}, repoWithPermissions(true, true))
	body := bytes.NewBufferString(`{"operation":"download","hash_algo":"sha512","objects":[{"oid":"` + testOID + `","size":12}]}`)
	req := httptest.NewRequest(http.MethodPost, "/acme/demo.git/info/lfs/objects/batch", body)
	req.Header.Set("Authorization", "Basic dGVzdDp0b2tlbg==")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestBatchRouteAcceptsRepoWithoutGitSuffix(t *testing.T) {
	handler := testHandler(t, fakeStore{objects: map[string]objectMeta{}}, repoWithPermissions(true, true))
	req := newBatchRequest(t, "upload", testOID, 12)
	req.URL.Path = "/acme/demo/info/lfs/objects/batch"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestVerifyChecksUploadedObjectSize(t *testing.T) {
	key := objectKey("gitea-lfs", "repo", 42, testOID)
	store := fakeStore{objects: map[string]objectMeta{key: {Size: 12}}}
	metas := fakeMetaStore{objects: map[string]objectMeta{}}
	tokens := NewVerifyTokens("test-secret")
	cfg := testConfig()
	handler := NewHandler(cfg, fakeRepoClient{info: repoWithPermissions(true, true)}, store, NewDownloadSigner(cfg, store), metas, tokens)

	body := bytes.NewBufferString(`{"oid":"` + testOID + `","size":12}`)
	req := httptest.NewRequest(http.MethodPost, "/acme/demo.git/info/lfs/objects/"+testOID+"/verify", body)
	req.Header.Set("Content-Type", lfsMediaType)
	req.Header.Set(verifyHeader, tokens.Sign(42, testOID, 12, time.Now().Add(time.Minute)))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if meta, ok := metas.objects[metaKey(42, testOID)]; !ok || meta.Size != 12 {
		t.Fatalf("metadata = %+v, exists = %v", meta, ok)
	}
}

func TestLocksVerifyReturnsEmptyLockSets(t *testing.T) {
	handler := testHandler(t, fakeStore{objects: map[string]objectMeta{}}, repoWithPermissions(true, true))
	req := httptest.NewRequest(http.MethodPost, "/acme/demo.git/info/lfs/locks/verify", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", lfsMediaType)
	req.Header.Set("Authorization", "Basic dGVzdDp0b2tlbg==")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp locksVerifyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Ours == nil || resp.Theirs == nil {
		t.Fatalf("locks response = %#v", resp)
	}
	if len(resp.Ours) != 0 || len(resp.Theirs) != 0 {
		t.Fatalf("locks response = %#v", resp)
	}
}

func TestLocksVerifyRequiresPushPermission(t *testing.T) {
	handler := testHandler(t, fakeStore{objects: map[string]objectMeta{}}, repoWithPermissions(false, true))
	req := httptest.NewRequest(http.MethodPost, "/acme/demo.git/info/lfs/locks/verify", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", lfsMediaType)
	req.Header.Set("Authorization", "Basic dGVzdDp0b2tlbg==")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestMediaRedirectRewritesGiteaOSSLocationToCDN(t *testing.T) {
	cfg := testConfig()
	cfg.OSSBucket = "example-lfs-bucket"
	cfg.OSSKeyPrefix = "gitea/lfs"
	cfg.OSSKeyStyle = "gitea"
	redirectURL, err := url.Parse("https://example-lfs-bucket.s3.oss-cn-shenzhen.aliyuncs.com/gitea/lfs/01/23/456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef?X-Amz-Signature=test")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(
		cfg,
		fakeRepoClient{mediaRedirect: redirectURL, mediaStatus: http.StatusSeeOther, mediaHeader: http.Header{"Location": []string{redirectURL.String()}}},
		fakeStore{objects: map[string]objectMeta{}},
		NewDownloadSigner(cfg, fakeStore{objects: map[string]objectMeta{}}),
		fakeMetaStore{objects: map[string]objectMeta{}},
		NewVerifyTokens("test-secret"),
	)
	req := httptest.NewRequest(http.MethodGet, "/acme/demo/media/commit/deadbeef/file.zip", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	location := rr.Header().Get("Location")
	if !strings.HasPrefix(location, "https://cdn.example.com/lfs/gitea/lfs/01/23/456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/file.zip") {
		t.Fatalf("location = %q", location)
	}
	if !strings.Contains(location, "auth_key=") {
		t.Fatalf("location missing auth_key: %q", location)
	}
}

func TestMediaFallsBackToGiteaResponseForNonLFSFile(t *testing.T) {
	handler := NewHandler(
		testConfig(),
		fakeRepoClient{
			mediaStatus: http.StatusOK,
			mediaHeader: http.Header{
				"Content-Type": []string{"text/plain"},
			},
			mediaBody: "plain file",
		},
		fakeStore{objects: map[string]objectMeta{}},
		NewDownloadSigner(testConfig(), fakeStore{objects: map[string]objectMeta{}}),
		fakeMetaStore{objects: map[string]objectMeta{}},
		NewVerifyTokens("test-secret"),
	)
	req := httptest.NewRequest(http.MethodGet, "/acme/demo/media/commit/deadbeef/readme.txt", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "plain file" {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func testHandler(t *testing.T, store fakeStore, repo repoInfo) http.Handler {
	t.Helper()
	cfg := testConfig()
	return NewHandler(cfg, fakeRepoClient{info: repo}, store, NewDownloadSigner(cfg, store), fakeMetaStore{objects: map[string]objectMeta{}}, NewVerifyTokens("test-secret"))
}

func testConfig() Config {
	return Config{
		PublicURL:    "https://git.example.com",
		OSSKeyPrefix: "gitea-lfs",
		OSSKeyStyle:  "repo",
		SignExpires:  time.Minute,
		CDNBaseURL:   "https://cdn.example.com/lfs",
		CDNAuthKey:   "cdn-secret",
		CDNAuthUID:   "0",
		VerifySecret: "test-secret",
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}
}

func TestObjectKeySupportsGiteaCompatibleStyle(t *testing.T) {
	key := objectKey("lfs", "gitea", 42, testOID)
	if key != "lfs/01/23/"+testOID[4:] {
		t.Fatalf("key = %q", key)
	}
}

func repoWithPermissions(push, pull bool) repoInfo {
	info := repoInfo{ID: 42}
	info.Permissions.Push = push
	info.Permissions.Pull = pull
	return info
}

func metaKey(repoID int64, oid string) string {
	return strconv.FormatInt(repoID, 10) + ":" + oid
}

func newBatchRequest(t *testing.T, operation, oid string, size int64) *http.Request {
	t.Helper()
	body := bytes.NewBufferString(`{"operation":"` + operation + `","transfers":["basic"],"objects":[{"oid":"` + oid + `","size":` + strconv.FormatInt(size, 10) + `}]}`)
	req := httptest.NewRequest(http.MethodPost, "/acme/demo.git/info/lfs/objects/batch", body)
	req.Header.Set("Content-Type", lfsMediaType)
	req.Header.Set("Authorization", "Basic dGVzdDp0b2tlbg==")
	return req
}
