package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var oidPattern = regexp.MustCompile(`\A[0-9a-f]{64}\z`)
var uuidPattern = regexp.MustCompile(`\A[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\z`)

type repoClient interface {
	GetRepo(ctx context.Context, auth, owner, repo string) (repoInfo, error)
	CheckReleaseWriter(ctx context.Context, headers http.Header, owner, repo string) (string, error)
	GetMedia(ctx context.Context, headers http.Header, path string) (*mediaResponse, error)
	GetWeb(ctx context.Context, method string, headers http.Header, path string) (*mediaResponse, error)
}

type Handler struct {
	cfg           Config
	repos         repoClient
	store         ObjectStore
	downloads     DownloadSigner
	metas         MetaStore
	tokens        *VerifyTokens
	releaseTokens *ReleaseUploadTokens
	mux           *http.ServeMux
}

func NewHandler(cfg Config, repos repoClient, store ObjectStore, downloads DownloadSigner, metas MetaStore, tokens *VerifyTokens) http.Handler {
	h := &Handler{
		cfg:           cfg,
		repos:         repos,
		store:         store,
		downloads:     downloads,
		metas:         metas,
		tokens:        tokens,
		releaseTokens: NewReleaseUploadTokens(cfg.VerifySecret),
		mux:           http.NewServeMux(),
	}
	h.mux.HandleFunc("/", h.serve)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	route, ok := parseRoute(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	switch {
	case route.Kind == "batch" && r.Method == http.MethodPost:
		h.handleBatch(w, r, route)
	case route.Kind == "verify" && r.Method == http.MethodPost:
		h.handleVerify(w, r, route)
	case route.Kind == "locksVerify" && r.Method == http.MethodPost:
		h.handleLocksVerify(w, r, route)
	case route.Kind == "media" && r.Method == http.MethodGet:
		h.handleMedia(w, r, route)
	case route.Kind == "releaseUpload" && r.Method == http.MethodPost:
		h.handleReleaseUpload(w, r, route)
	case route.Kind == "releaseUploadComplete" && r.Method == http.MethodPost:
		h.handleReleaseUploadComplete(w, r, route)
	case route.Kind == "releaseDownload" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.handleReleaseDownload(w, r, route)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) handleReleaseUpload(w http.ResponseWriter, r *http.Request, route route) {
	if !h.cfg.ReleaseDirectUpload {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !isJSONRequest(r) {
		writeAPIError(w, http.StatusUnsupportedMediaType, "application/json is required")
		return
	}
	repoID, uploaderID, ok := h.authorizeReleaseWriter(w, r, route)
	if !ok {
		return
	}

	var req releaseUploadRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid json")
		return
	}
	name := sanitizeFilename(req.Name)
	if name == "" || name != req.Name || len([]rune(name)) > 255 {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid file name")
		return
	}
	if req.Size < 0 || req.Size > h.cfg.ReleaseMaxFileSize {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "file size exceeds release limit")
		return
	}

	uuid, err := newUUID()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "generate upload id failed")
		return
	}
	expiresAt := time.Now().UTC().Add(h.cfg.SignExpires).Truncate(time.Second)
	claims := releaseUploadClaims{
		RepoID: repoID, UploaderID: uploaderID, UUID: uuid, Name: name, Size: req.Size, ExpiresAt: expiresAt.Unix(),
	}
	token, err := h.releaseTokens.Sign(claims)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "sign upload token failed")
		return
	}
	href, headers, err := h.store.PresignPut(r.Context(), releasePendingKey(h.cfg.ReleasePendingPrefix, repoID, uuid), h.cfg.SignExpires)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "sign upload failed")
		return
	}

	writeAPIJSON(w, http.StatusOK, releaseUploadResponse{
		Upload: lfsLink{
			Href: href, Header: headers, ExpiresAt: expiresAt, ExpiresIn: int64(h.cfg.SignExpires.Seconds()),
		},
		CompleteURL: releaseCompleteURL(route),
		Token:       token,
		ExpiresAt:   expiresAt,
	})
}

func (h *Handler) handleReleaseUploadComplete(w http.ResponseWriter, r *http.Request, route route) {
	if !h.cfg.ReleaseDirectUpload {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !isJSONRequest(r) {
		writeAPIError(w, http.StatusUnsupportedMediaType, "application/json is required")
		return
	}
	repoID, uploaderID, ok := h.authorizeReleaseWriter(w, r, route)
	if !ok {
		return
	}

	var req releaseUploadCompleteRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid json")
		return
	}
	claims, valid := h.releaseTokens.Verify(req.Token, time.Now())
	if !valid || claims.RepoID != repoID || claims.UploaderID != uploaderID {
		writeAPIError(w, http.StatusUnauthorized, "invalid upload token")
		return
	}

	pendingKey := releasePendingKey(h.cfg.ReleasePendingPrefix, repoID, claims.UUID)
	finalKey := releaseAttachmentKey(h.cfg.ReleaseAttachmentPrefix, claims.UUID)
	pendingMeta, pendingExists, err := h.store.Stat(r.Context(), pendingKey)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "stat uploaded object failed")
		return
	}
	if !pendingExists || pendingMeta.Size != claims.Size {
		writeAPIError(w, http.StatusUnprocessableEntity, "uploaded object size mismatch")
		return
	}

	if finalMeta, finalExists, err := h.store.Stat(r.Context(), finalKey); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "stat release object failed")
		return
	} else if finalExists {
		if finalMeta.Size != claims.Size {
			writeAPIError(w, http.StatusConflict, "release object already exists")
			return
		}
	} else {
		if err := h.store.Copy(r.Context(), pendingKey, finalKey); err != nil {
			if meta, exists, statErr := h.store.Stat(r.Context(), finalKey); statErr != nil || !exists || meta.Size != claims.Size {
				writeAPIError(w, http.StatusInternalServerError, "finalize release object failed")
				return
			}
		}
	}
	if err := h.store.EnsureDownloadName(r.Context(), finalKey, claims.Name); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "set release download name failed")
		return
	}

	attachment := releaseAttachment{
		UUID: claims.UUID, RepoID: claims.RepoID, UploaderID: claims.UploaderID,
		Name: claims.Name, Size: claims.Size, CreatedAt: time.Now().UTC(),
	}
	if err := h.metas.EnsureAttachment(r.Context(), attachment); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save release attachment failed")
		return
	}
	_ = h.store.Delete(r.Context(), pendingKey)
	writeAPIJSON(w, http.StatusOK, map[string]string{"uuid": claims.UUID})
}

func (h *Handler) handleBatch(w http.ResponseWriter, r *http.Request, route route) {
	var req batchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	req.Operation = strings.ToLower(req.Operation)
	if req.Operation != "upload" && req.Operation != "download" {
		writeError(w, http.StatusBadRequest, "operation must be upload or download")
		return
	}
	if !supportsBasicTransfer(req.Transfers) {
		writeError(w, http.StatusNotAcceptable, "only basic transfer is supported")
		return
	}
	if req.HashAlgo != "" && req.HashAlgo != "sha256" {
		writeError(w, http.StatusConflict, "only sha256 hash algorithm is supported")
		return
	}
	if len(req.Objects) == 0 {
		writeError(w, http.StatusBadRequest, "objects is required")
		return
	}

	repo, ok := h.authorize(w, r, route, req.Operation)
	if !ok {
		return
	}

	expiresAt := time.Now().UTC().Add(h.cfg.SignExpires).Truncate(time.Second)
	resp := batchResponse{
		Transfer: "basic",
		HashAlgo: "sha256",
		Objects:  make([]objectResponse, 0, len(req.Objects)),
	}

	for _, obj := range req.Objects {
		resp.Objects = append(resp.Objects, h.buildObjectResponse(r.Context(), route, repo, obj, req.Operation, expiresAt))
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) buildObjectResponse(ctx context.Context, route route, repo repoInfo, obj objectRequest, operation string, expiresAt time.Time) objectResponse {
	resp := objectResponse{
		OID:           obj.OID,
		Size:          obj.Size,
		Authenticated: true,
	}

	if !validObject(obj) {
		resp.Error = &objectError{Code: http.StatusUnprocessableEntity, Message: "invalid oid or size"}
		return resp
	}

	key := objectKey(h.cfg.OSSKeyPrefix, h.cfg.OSSKeyStyle, repo.ID, obj.OID)
	meta, exists, err := h.store.Stat(ctx, key)
	if err != nil {
		resp.Error = &objectError{Code: http.StatusInternalServerError, Message: "stat object failed"}
		return resp
	}
	if exists && meta.Size != obj.Size {
		resp.Error = &objectError{Code: http.StatusUnprocessableEntity, Message: "object size mismatch"}
		return resp
	}

	switch operation {
	case "upload":
		if exists {
			if err := h.metas.Upsert(ctx, repo.ID, obj); err != nil {
				resp.Error = &objectError{Code: http.StatusInternalServerError, Message: "save object metadata failed"}
			}
			return resp
		}
		href, headers, err := h.store.PresignPut(ctx, key, h.cfg.SignExpires)
		if err != nil {
			resp.Error = &objectError{Code: http.StatusInternalServerError, Message: "sign upload failed"}
			return resp
		}
		resp.Actions = map[string]lfsLink{
			"upload": {
				Href:      href,
				Header:    headers,
				ExpiresAt: expiresAt,
				ExpiresIn: int64(h.cfg.SignExpires.Seconds()),
			},
			"verify": {
				Href:      h.verifyURL(route, obj.OID),
				Header:    map[string]string{verifyHeader: h.tokens.Sign(repo.ID, obj.OID, obj.Size, expiresAt)},
				ExpiresAt: expiresAt,
				ExpiresIn: int64(h.cfg.SignExpires.Seconds()),
			},
		}
	case "download":
		if !exists {
			resp.Error = &objectError{Code: http.StatusNotFound, Message: "object does not exist"}
			return resp
		}
		if meta, ok, err := h.metas.Get(ctx, repo.ID, obj.OID); err != nil {
			resp.Error = &objectError{Code: http.StatusInternalServerError, Message: "load object metadata failed"}
			return resp
		} else if ok && meta.Size != obj.Size {
			resp.Error = &objectError{Code: http.StatusUnprocessableEntity, Message: "object metadata size mismatch"}
			return resp
		}
		href, headers, err := h.downloads.SignDownload(ctx, key, h.cfg.SignExpires, expiresAt)
		if err != nil {
			resp.Error = &objectError{Code: http.StatusInternalServerError, Message: "sign download failed"}
			return resp
		}
		resp.Actions = map[string]lfsLink{
			"download": {
				Href:      href,
				Header:    headers,
				ExpiresAt: expiresAt,
				ExpiresIn: int64(h.cfg.SignExpires.Seconds()),
			},
		}
	}

	return resp
}

func (h *Handler) handleVerify(w http.ResponseWriter, r *http.Request, route route) {
	token := r.Header.Get(verifyHeader)
	claims, ok := h.tokens.Verify(token, time.Now())
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid verify token")
		return
	}
	if claims.OID != route.OID {
		writeError(w, http.StatusBadRequest, "oid mismatch")
		return
	}

	var req objectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.OID != claims.OID || req.Size != claims.Size {
		writeError(w, http.StatusBadRequest, "object mismatch")
		return
	}

	key := objectKey(h.cfg.OSSKeyPrefix, h.cfg.OSSKeyStyle, claims.RepoID, claims.OID)
	meta, exists, err := h.store.Stat(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stat object failed")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "object does not exist")
		return
	}
	if meta.Size != claims.Size {
		writeError(w, http.StatusUnprocessableEntity, "object size mismatch")
		return
	}
	if err := h.metas.Upsert(r.Context(), claims.RepoID, req); err != nil {
		writeError(w, http.StatusInternalServerError, "save object metadata failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{})
}

func (h *Handler) handleLocksVerify(w http.ResponseWriter, r *http.Request, route route) {
	if _, ok := h.authorize(w, r, route, "upload"); !ok {
		return
	}

	writeJSON(w, http.StatusOK, locksVerifyResponse{
		Ours:   []lfsLock{},
		Theirs: []lfsLock{},
	})
}

func (h *Handler) handleMedia(w http.ResponseWriter, r *http.Request, route route) {
	media, err := h.repos.GetMedia(r.Context(), r.Header, r.URL.RequestURI())
	if err != nil {
		writeError(w, http.StatusBadGateway, "gitea media lookup failed")
		return
	}
	defer media.Close()

	if media.Redirect == nil {
		copyHTTPHeaders(w.Header(), media.Header)
		w.WriteHeader(media.StatusCode)
		if media.Body != nil {
			_, _ = io.Copy(w, media.Body)
		}
		return
	}

	key, ok := h.objectKeyFromGiteaMediaRedirect(media.Redirect)
	if !ok {
		copyHTTPHeaders(w.Header(), media.Header)
		w.WriteHeader(media.StatusCode)
		return
	}
	href, _, err := h.downloads.SignDownloadWithFilename(
		r.Context(),
		key,
		filenameFromMediaPath(route.Path),
		h.cfg.SignExpires,
		time.Now().UTC().Add(h.cfg.SignExpires).Truncate(time.Second),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sign download failed")
		return
	}
	http.Redirect(w, r, href, http.StatusFound)
}

func (h *Handler) handleReleaseDownload(w http.ResponseWriter, r *http.Request, route route) {
	response, err := h.repos.GetWeb(r.Context(), r.Method, r.Header, r.URL.RequestURI())
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "gitea release lookup failed")
		return
	}
	defer response.Close()
	if response.Redirect == nil {
		copyHTTPHeaders(w.Header(), response.Header)
		w.WriteHeader(response.StatusCode)
		if r.Method != http.MethodHead && response.Body != nil {
			_, _ = io.Copy(w, response.Body)
		}
		return
	}

	key, ok := h.objectKeyFromGiteaReleaseRedirect(response.Redirect)
	if !ok {
		copyHTTPHeaders(w.Header(), response.Header)
		w.WriteHeader(response.StatusCode)
		return
	}
	filename := filenameFromMediaPath(route.Path)
	if err := h.store.EnsureDownloadName(r.Context(), key, filename); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "set release download name failed")
		return
	}
	href, _, err := h.downloads.SignDownload(
		r.Context(), key, h.cfg.SignExpires,
		time.Now().UTC().Add(h.cfg.SignExpires).Truncate(time.Second),
	)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "sign release download failed")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	http.Redirect(w, r, href, http.StatusFound)
}

func (h *Handler) objectKeyFromGiteaMediaRedirect(redirectURL *url.URL) (string, bool) {
	pathKey := strings.TrimLeft(redirectURL.EscapedPath(), "/")
	if strings.HasPrefix(redirectURL.Host, h.cfg.OSSBucket+".") {
		if key, err := url.PathUnescape(pathKey); err == nil && validObjectKey(h.cfg.OSSKeyPrefix, h.cfg.OSSKeyStyle, key) {
			return key, true
		}
	}
	if strings.HasPrefix(pathKey, h.cfg.OSSBucket+"/") {
		key, err := url.PathUnescape(strings.TrimPrefix(pathKey, h.cfg.OSSBucket+"/"))
		if err == nil && validObjectKey(h.cfg.OSSKeyPrefix, h.cfg.OSSKeyStyle, key) {
			return key, true
		}
	}
	return "", false
}

func (h *Handler) objectKeyFromGiteaReleaseRedirect(redirectURL *url.URL) (string, bool) {
	pathKey := strings.TrimLeft(redirectURL.EscapedPath(), "/")
	endpointHost := ossEndpointHost(h.cfg.OSSEndpoint)
	redirectHost := strings.ToLower(redirectURL.Hostname())
	allowedEndpointHosts := []string{endpointHost, "s3." + endpointHost}
	pathStyle := false
	virtualHosted := false
	for _, host := range allowedEndpointHosts {
		pathStyle = pathStyle || redirectHost == host
		virtualHosted = virtualHosted || redirectHost == strings.ToLower(h.cfg.OSSBucket+"."+host)
	}
	if endpointHost == "" || (!pathStyle && !virtualHosted) {
		return "", false
	}
	if virtualHosted {
		if key, err := url.PathUnescape(pathKey); err == nil && validReleaseAttachmentKey(h.cfg.ReleaseAttachmentPrefix, key) {
			return key, true
		}
	}
	if strings.HasPrefix(pathKey, h.cfg.OSSBucket+"/") {
		key, err := url.PathUnescape(strings.TrimPrefix(pathKey, h.cfg.OSSBucket+"/"))
		if err == nil && validReleaseAttachmentKey(h.cfg.ReleaseAttachmentPrefix, key) {
			return key, true
		}
	}
	return "", false
}

func ossEndpointHost(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	if parsed.Hostname() == "" {
		parsed, err = url.Parse("//" + endpoint)
		if err != nil {
			return ""
		}
	}
	return strings.ToLower(parsed.Hostname())
}

func filenameFromMediaPath(mediaPath string) string {
	lastSlash := strings.LastIndex(mediaPath, "/")
	filename := mediaPath
	if lastSlash >= 0 {
		filename = mediaPath[lastSlash+1:]
	}
	filename, err := url.PathUnescape(filename)
	if err != nil {
		return ""
	}
	return sanitizeFilename(filename)
}

func sanitizeFilename(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" || filename == "." || filename == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range filename {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, route route, operation string) (repoInfo, bool) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		setLFSAuthenticate(w)
		writeError(w, http.StatusUnauthorized, "authentication required")
		return repoInfo{}, false
	}

	repo, err := h.repos.GetRepo(r.Context(), auth, route.Owner, route.Repo)
	if err != nil {
		switch {
		case errors.Is(err, errUnauthorized):
			setLFSAuthenticate(w)
			writeError(w, http.StatusUnauthorized, "authentication failed")
		case errors.Is(err, errForbidden):
			writeError(w, http.StatusForbidden, "repository access denied")
		case errors.Is(err, errNotFound):
			writeError(w, http.StatusNotFound, "repository not found")
		default:
			writeError(w, http.StatusBadGateway, "gitea authorization failed")
		}
		return repoInfo{}, false
	}

	if operation == "upload" && !repo.canPush() {
		writeError(w, http.StatusForbidden, "push permission required")
		return repoInfo{}, false
	}
	if operation == "download" && !repo.canPull() {
		writeError(w, http.StatusForbidden, "pull permission required")
		return repoInfo{}, false
	}
	return repo, true
}

func (h *Handler) authorizeReleaseWriter(w http.ResponseWriter, r *http.Request, route route) (int64, int64, bool) {
	username, err := h.repos.CheckReleaseWriter(r.Context(), r.Header, route.Owner, route.Repo)
	if err != nil {
		writeReleaseAuthorizationError(w, err)
		return 0, 0, false
	}
	repoID, uploaderID, err := h.metas.ResolveReleaseUpload(r.Context(), route.Owner, route.Repo, username)
	if err != nil {
		writeReleaseAuthorizationError(w, err)
		return 0, 0, false
	}
	return repoID, uploaderID, true
}

func writeReleaseAuthorizationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnauthorized):
		writeAPIError(w, http.StatusUnauthorized, "authentication required")
	case errors.Is(err, errForbidden):
		writeAPIError(w, http.StatusForbidden, "release write permission required")
	case errors.Is(err, errNotFound):
		writeAPIError(w, http.StatusNotFound, "repository not found")
	default:
		writeAPIError(w, http.StatusBadGateway, "gitea authorization failed")
	}
}

func parseRoute(rawPath string) (route, bool) {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) < 5 {
		return route{}, false
	}
	if parts[2] == "releases" {
		owner, err := url.PathUnescape(parts[0])
		if err != nil {
			return route{}, false
		}
		repo, err := url.PathUnescape(parts[1])
		if err != nil || owner == "" || repo == "" {
			return route{}, false
		}
		if len(parts) == 5 && parts[3] == "attachments" && parts[4] == "direct" {
			return route{Owner: owner, Repo: repo, Kind: "releaseUpload"}, true
		}
		if len(parts) == 6 && parts[3] == "attachments" && parts[4] == "direct" && parts[5] == "complete" {
			return route{Owner: owner, Repo: repo, Kind: "releaseUploadComplete"}, true
		}
		if len(parts) >= 6 && parts[3] == "download" {
			return route{Owner: owner, Repo: repo, Kind: "releaseDownload", Path: strings.Join(parts[2:], "/")}, true
		}
		return route{}, false
	}
	if len(parts) >= 5 && parts[2] == "media" {
		owner, err := url.PathUnescape(parts[0])
		if err != nil {
			return route{}, false
		}
		repo, err := url.PathUnescape(parts[1])
		if err != nil {
			return route{}, false
		}
		if owner == "" || repo == "" {
			return route{}, false
		}
		return route{Owner: owner, Repo: repo, Kind: "media", Path: strings.Join(parts[2:], "/")}, true
	}
	if parts[2] != "info" || parts[3] != "lfs" {
		return route{}, false
	}

	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		return route{}, false
	}
	repoPart, err := url.PathUnescape(parts[1])
	if err != nil {
		return route{}, false
	}
	repo := strings.TrimSuffix(repoPart, ".git")
	if owner == "" || repo == "" {
		return route{}, false
	}

	if parts[4] == "locks" {
		if len(parts) == 6 && parts[5] == "verify" {
			return route{Owner: owner, Repo: repo, Kind: "locksVerify"}, true
		}
		return route{}, false
	}
	if parts[4] != "objects" {
		return route{}, false
	}
	if len(parts) == 6 && parts[5] == "batch" {
		return route{Owner: owner, Repo: repo, Kind: "batch"}, true
	}
	if len(parts) == 7 && parts[6] == "verify" {
		oid, err := url.PathUnescape(parts[5])
		if err != nil || !oidPattern.MatchString(oid) {
			return route{}, false
		}
		return route{Owner: owner, Repo: repo, Kind: "verify", OID: oid}, true
	}
	return route{}, false
}

func releasePendingKey(prefix string, repoID int64, uuid string) string {
	return strings.Join([]string{prefix, "repositories", strconv.FormatInt(repoID, 10), uuid}, "/")
}

func releaseAttachmentKey(prefix, uuid string) string {
	return strings.Join([]string{prefix, uuid[0:1], uuid[1:2], uuid}, "/")
}

func validReleaseAttachmentKey(prefix, key string) bool {
	prefix = strings.Trim(prefix, "/")
	if !strings.HasPrefix(key, prefix+"/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(key, prefix+"/"), "/")
	if len(parts) != 3 || len(parts[0]) != 1 || len(parts[1]) != 1 || !uuidPattern.MatchString(parts[2]) {
		return false
	}
	return parts[0] == parts[2][0:1] && parts[1] == parts[2][1:2]
}

func releaseCompleteURL(route route) string {
	return fmt.Sprintf("/%s/%s/releases/attachments/direct/complete", url.PathEscape(route.Owner), url.PathEscape(route.Repo))
}

func isJSONRequest(r *http.Request) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	return contentType == "application/json"
}

func (h *Handler) verifyURL(route route, oid string) string {
	base := h.cfg.PublicURL
	if base == "" {
		return fmt.Sprintf("/%s/%s.git/info/lfs/objects/%s/verify", url.PathEscape(route.Owner), url.PathEscape(route.Repo), oid)
	}
	return fmt.Sprintf("%s/%s/%s.git/info/lfs/objects/%s/verify", base, url.PathEscape(route.Owner), url.PathEscape(route.Repo), oid)
}

func supportsBasicTransfer(transfers []string) bool {
	if len(transfers) == 0 {
		return true
	}
	for _, transfer := range transfers {
		if transfer == "basic" {
			return true
		}
	}
	return false
}

func validObject(obj objectRequest) bool {
	return obj.Size >= 0 && oidPattern.MatchString(obj.OID)
}

func objectKey(prefix, style string, repoID int64, oid string) string {
	segments := []string{}
	if prefix != "" {
		segments = append(segments, prefix)
	}
	if style == "gitea" {
		segments = append(segments, oid[:2], oid[2:4], oid[4:])
		return strings.Join(segments, "/")
	}
	segments = append(
		segments,
		"repositories",
		strconv.FormatInt(repoID, 10),
		oid[:2],
		oid[2:4],
		oid[4:],
	)
	return strings.Join(segments, "/")
}

func validObjectKey(prefix, style, key string) bool {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		keyPrefix := prefix + "/"
		if !strings.HasPrefix(key, keyPrefix) {
			return false
		}
		key = strings.TrimPrefix(key, keyPrefix)
	}
	parts := strings.Split(key, "/")
	if style == "gitea" {
		if len(parts) != 3 {
			return false
		}
		return len(parts[0]) == 2 && len(parts[1]) == 2 && len(parts[2]) == 60 &&
			oidPattern.MatchString(parts[0]+parts[1]+parts[2])
	}
	if len(parts) != 5 || parts[0] != "repositories" {
		return false
	}
	if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
		return false
	}
	return len(parts[2]) == 2 && len(parts[3]) == 2 && len(parts[4]) == 60 &&
		oidPattern.MatchString(parts[2]+parts[3]+parts[4])
}

func copyHTTPHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHopHeader(key) {
			continue
		}
		dst[key] = append([]string(nil), values...)
	}
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", lfsMediaType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, lfsError{Message: message})
}

func writeAPIJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeAPIJSON(w, status, map[string]string{"message": message})
}

func setLFSAuthenticate(w http.ResponseWriter) {
	value := `Basic realm="Git LFS"`
	w.Header().Set("LFS-Authenticate", value)
	w.Header().Set("WWW-Authenticate", value)
}
