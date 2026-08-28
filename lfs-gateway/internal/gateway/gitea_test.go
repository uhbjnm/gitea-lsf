package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckReleaseWriterReturnsServerRenderedUsername(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "session=test" {
			http.Error(w, "missing session", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`<div class="menu user-menu"><div class="header">Signed in as <strong>release-user</strong></div></div>`))
	}))
	defer server.Close()

	client := NewGiteaClient(server.URL+"/api/v1", server.URL)
	headers := make(http.Header)
	headers.Set("Cookie", "session=test")
	username, err := client.CheckReleaseWriter(t.Context(), headers, "owner", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if username != "release-user" {
		t.Fatalf("username = %q", username)
	}
}
