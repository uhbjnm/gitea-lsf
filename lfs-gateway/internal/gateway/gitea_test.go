package gateway

import (
	"errors"
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

func TestGiteaTokenReleaseAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token test" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/user":
			_, _ = w.Write([]byte(`{"id":7}`))
		case "/api/v1/repos/owner/repo/releases/99":
			_, _ = w.Write([]byte(`{"id":99}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewGiteaClient(server.URL+"/api/v1", server.URL)
	user, err := client.GetCurrentUser(t.Context(), "token test")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 7 {
		t.Fatalf("user id = %d", user.ID)
	}
	if err := client.GetRelease(t.Context(), "token test", "owner", "repo", 99); err != nil {
		t.Fatal(err)
	}
	if err := client.GetRelease(t.Context(), "token test", "owner", "repo", 100); !errors.Is(err, errNotFound) {
		t.Fatalf("missing release error = %v", err)
	}
}
