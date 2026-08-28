package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type directObjectStore struct {
	mu      sync.Mutex
	baseURL string
	data    map[string][]byte
}

func (s *directObjectStore) Stat(_ context.Context, key string) (objectMeta, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.data[key]
	return objectMeta{Size: int64(len(data))}, ok, nil
}

func (s *directObjectStore) PresignPut(_ context.Context, key string, _ time.Duration) (string, map[string]string, error) {
	return s.baseURL + "/oss/" + key, map[string]string{
		"Content-Type":           "application/octet-stream",
		"x-oss-forbid-overwrite": "true",
	}, nil
}

func (s *directObjectStore) PresignGet(_ context.Context, key string, _ time.Duration) (string, map[string]string, error) {
	return s.baseURL + "/oss/" + key, nil, nil
}

func (s *directObjectStore) Copy(_ context.Context, sourceKey, destinationKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.data[sourceKey]
	if !ok {
		return errors.New("source object not found")
	}
	s.data[destinationKey] = append([]byte(nil), data...)
	return nil
}

func (s *directObjectStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *directObjectStore) EnsureDownloadName(_ context.Context, _ string, _ string) error {
	return nil
}

type directDownloadSigner struct {
	baseURL string
}

func (s directDownloadSigner) SignDownload(_ context.Context, key string, _ time.Duration, _ time.Time) (string, map[string]string, error) {
	return s.baseURL + "/cdn/" + key + "?auth_key=test", nil, nil
}

func (s directDownloadSigner) SignDownloadWithFilename(_ context.Context, key, filename string, _ time.Duration, _ time.Time) (string, map[string]string, error) {
	return s.baseURL + "/cdn/" + key + "/" + filename + "?auth_key=test", nil, nil
}

func TestDirectUploadVerifyAndCDNDownloadFlow(t *testing.T) {
	content := []byte("hello-lfs-object\n")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	size := int64(len(content))

	store := &directObjectStore{data: map[string][]byte{}}
	objectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.URL.Path, "/oss/")
		if ok && r.Method == http.MethodPut {
			if r.Header.Get("x-oss-forbid-overwrite") != "true" {
				t.Errorf("missing x-oss-forbid-overwrite header")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			store.mu.Lock()
			store.data[key] = body
			store.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}

		key, ok = strings.CutPrefix(r.URL.Path, "/cdn/")
		if ok && r.Method == http.MethodGet {
			store.mu.Lock()
			body, exists := store.data[key]
			store.mu.Unlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer objectServer.Close()
	store.baseURL = objectServer.URL

	cfg := testConfig()
	cfg.PublicURL = ""
	gateway := NewHandler(cfg, fakeRepoClient{info: repoWithPermissions(true, true)}, store, directDownloadSigner{baseURL: objectServer.URL}, fakeMetaStore{objects: map[string]objectMeta{}}, NewVerifyTokens("test-secret"))
	gatewayServer := httptest.NewServer(gateway)
	defer gatewayServer.Close()

	uploadBatch := postBatch(t, gatewayServer.URL, "upload", oid, size)
	uploadAction := uploadBatch.Objects[0].Actions["upload"]
	if strings.HasPrefix(uploadAction.Href, gatewayServer.URL) {
		t.Fatalf("upload href points at gateway: %q", uploadAction.Href)
	}

	putReq, err := http.NewRequest(http.MethodPut, uploadAction.Href, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range uploadAction.Header {
		putReq.Header.Set(key, value)
	}
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", putResp.StatusCode)
	}

	verifyAction := uploadBatch.Objects[0].Actions["verify"]
	verifyBody := bytes.NewBufferString(`{"oid":"` + oid + `","size":` + strconv.FormatInt(size, 10) + `}`)
	verifyReq, err := http.NewRequest(http.MethodPost, gatewayServer.URL+verifyAction.Href, verifyBody)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range verifyAction.Header {
		verifyReq.Header.Set(key, value)
	}
	verifyResp, err := http.DefaultClient.Do(verifyReq)
	if err != nil {
		t.Fatal(err)
	}
	verifyResp.Body.Close()
	if verifyResp.StatusCode != http.StatusOK {
		t.Fatalf("verify status = %d", verifyResp.StatusCode)
	}

	downloadBatch := postBatch(t, gatewayServer.URL, "download", oid, size)
	downloadAction := downloadBatch.Objects[0].Actions["download"]
	if !strings.Contains(downloadAction.Href, "/cdn/") || !strings.Contains(downloadAction.Href, "auth_key=test") {
		t.Fatalf("download href = %q", downloadAction.Href)
	}
	getResp, err := http.Get(downloadAction.Href)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	downloaded, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloaded, content) {
		t.Fatalf("downloaded = %q", downloaded)
	}
}

func postBatch(t *testing.T, baseURL, operation, oid string, size int64) batchResponse {
	t.Helper()
	body := bytes.NewBufferString(`{"operation":"` + operation + `","transfers":["basic"],"objects":[{"oid":"` + oid + `","size":` + strconv.FormatInt(size, 10) + `}]}`)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/acme/demo.git/info/lfs/objects/batch", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", lfsMediaType)
	req.Header.Set("Authorization", "Basic dGVzdDp0b2tlbg==")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("batch status = %d, body = %s", resp.StatusCode, responseBody)
	}

	var batch batchResponse
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	return batch
}
