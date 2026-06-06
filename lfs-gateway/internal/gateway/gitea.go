package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
	errNotFound     = errors.New("not found")
)

type GiteaClient struct {
	apiURL string
	webURL string
	client *http.Client
}

type mediaResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
	Redirect   *url.URL
}

func (r *mediaResponse) Close() {
	if r != nil && r.Body != nil {
		r.Body.Close()
	}
}

func NewGiteaClient(apiURL, webURL string) *GiteaClient {
	return &GiteaClient{
		apiURL: apiURL,
		webURL: webURL,
		client: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *GiteaClient) GetRepo(ctx context.Context, auth, owner, repo string) (repoInfo, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s",
		c.apiURL,
		url.PathEscape(owner),
		url.PathEscape(repo),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return repoInfo{}, err
	}
	req.Header.Set("Accept", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return repoInfo{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var info repoInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return repoInfo{}, err
		}
		return info, nil
	case http.StatusUnauthorized:
		io.Copy(io.Discard, resp.Body)
		return repoInfo{}, errUnauthorized
	case http.StatusForbidden:
		io.Copy(io.Discard, resp.Body)
		return repoInfo{}, errForbidden
	case http.StatusNotFound:
		io.Copy(io.Discard, resp.Body)
		return repoInfo{}, errNotFound
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return repoInfo{}, fmt.Errorf("gitea api %s: %s", resp.Status, string(body))
	}
}

func (c *GiteaClient) GetMedia(ctx context.Context, headers http.Header, path string) (*mediaResponse, error) {
	endpoint := c.webURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	copyForwardHeaders(req.Header, headers)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	result := &mediaResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       resp.Body,
	}
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		return result, nil
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return result, nil
	}
	redirectURL, err := url.Parse(location)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	result.Redirect = redirectURL
	return result, nil
}

func copyForwardHeaders(dst, src http.Header) {
	for _, name := range []string{"Cookie", "Authorization", "User-Agent", "Accept-Language"} {
		if values, ok := src[name]; ok {
			dst[name] = append([]string(nil), values...)
		}
	}
}
