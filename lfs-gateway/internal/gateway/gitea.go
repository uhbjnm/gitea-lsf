package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
	errNotFound     = errors.New("not found")
)

var signedInUserPattern = regexp.MustCompile(`(?s)<div class="menu user-menu">.*?<div class="header">.*?<strong>([A-Za-z0-9_.-]+)</strong>`)

type GiteaClient struct {
	apiURL string
	webURL string
	client *http.Client
}

func (c *GiteaClient) GetRelease(ctx context.Context, auth, owner, repo string, releaseID int64) (releaseInfo, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/releases/%s",
		c.apiURL,
		url.PathEscape(owner),
		url.PathEscape(repo),
		strconv.FormatInt(releaseID, 10),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return releaseInfo{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", auth)
	resp, err := c.client.Do(req)
	if err != nil {
		return releaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return releaseInfo{}, errUnauthorized
		case http.StatusForbidden:
			return releaseInfo{}, errForbidden
		case http.StatusNotFound:
			return releaseInfo{}, errNotFound
		default:
			return releaseInfo{}, fmt.Errorf("gitea release api: %s", resp.Status)
		}
	}
	var release releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return releaseInfo{}, err
	}
	if release.ID != releaseID || release.Author.ID <= 0 {
		return releaseInfo{}, errors.New("gitea release response missing id or author")
	}
	return release, nil
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
	headers := make(http.Header)
	if auth != "" {
		headers.Set("Authorization", auth)
	}
	return c.GetRepoWithHeaders(ctx, headers, owner, repo)
}

func (c *GiteaClient) GetRepoWithHeaders(ctx context.Context, headers http.Header, owner, repo string) (repoInfo, error) {
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
	copyForwardHeaders(req.Header, headers)

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

func (c *GiteaClient) CheckReleaseWriter(ctx context.Context, headers http.Header, owner, repo string) (string, error) {
	path := fmt.Sprintf("/%s/%s/releases/new", url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.webURL+path, nil)
	if err != nil {
		return "", err
	}
	copyForwardHeaders(req.Header, headers)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil {
			return "", err
		}
		match := signedInUserPattern.FindSubmatch(body)
		if len(match) != 2 {
			return "", errors.New("gitea release page missing signed-in user")
		}
		return html.UnescapeString(string(match[1])), nil
	case http.StatusUnauthorized:
		return "", errUnauthorized
	case http.StatusForbidden:
		return "", errForbidden
	case http.StatusNotFound:
		return "", errNotFound
	case http.StatusFound, http.StatusSeeOther:
		return "", errUnauthorized
	default:
		return "", fmt.Errorf("gitea release authorization: %s", resp.Status)
	}
}

func (c *GiteaClient) GetMedia(ctx context.Context, headers http.Header, path string) (*mediaResponse, error) {
	return c.GetWeb(ctx, http.MethodGet, headers, path)
}

func (c *GiteaClient) GetWeb(ctx context.Context, method string, headers http.Header, path string) (*mediaResponse, error) {
	endpoint := c.webURL + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
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
