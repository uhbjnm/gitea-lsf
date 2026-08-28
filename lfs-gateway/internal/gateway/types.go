package gateway

import "time"

const lfsMediaType = "application/vnd.git-lfs+json"

type batchRequest struct {
	Operation string          `json:"operation"`
	Transfers []string        `json:"transfers,omitempty"`
	Ref       *refSpec        `json:"ref,omitempty"`
	HashAlgo  string          `json:"hash_algo,omitempty"`
	Objects   []objectRequest `json:"objects"`
}

type refSpec struct {
	Name string `json:"name"`
}

type objectRequest struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type batchResponse struct {
	Transfer string           `json:"transfer,omitempty"`
	HashAlgo string           `json:"hash_algo,omitempty"`
	Objects  []objectResponse `json:"objects"`
}

type objectResponse struct {
	OID           string             `json:"oid"`
	Size          int64              `json:"size"`
	Authenticated bool               `json:"authenticated,omitempty"`
	Actions       map[string]lfsLink `json:"actions,omitempty"`
	Error         *objectError       `json:"error,omitempty"`
}

type lfsLink struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresAt time.Time         `json:"expires_at"`
	ExpiresIn int64             `json:"expires_in,omitempty"`
}

type objectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lfsError struct {
	Message string `json:"message"`
}

type locksVerifyResponse struct {
	Ours   []lfsLock `json:"ours"`
	Theirs []lfsLock `json:"theirs"`
}

type lfsLock struct{}

type route struct {
	Owner string
	Repo  string
	Kind  string
	OID   string
	Path  string
}

type repoInfo struct {
	ID          int64 `json:"id"`
	Permissions struct {
		Admin bool `json:"admin"`
		Push  bool `json:"push"`
		Pull  bool `json:"pull"`
	} `json:"permissions"`
}

func (r repoInfo) canPull() bool {
	return r.Permissions.Admin || r.Permissions.Push || r.Permissions.Pull
}

func (r repoInfo) canPush() bool {
	return r.Permissions.Admin || r.Permissions.Push
}

type objectMeta struct {
	Size int64
}

type releaseUploadRequest struct {
	ReleaseID int64  `json:"release_id,omitempty"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
}

type releaseUploadCompleteRequest struct {
	Token string `json:"token"`
}

type releaseUploadResponse struct {
	Upload      lfsLink   `json:"upload"`
	CompleteURL string    `json:"complete_url"`
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type releaseAttachment struct {
	UUID       string
	RepoID     int64
	ReleaseID  int64
	UploaderID int64
	Name       string
	Size       int64
	CreatedAt  time.Time
}

type releaseInfo struct {
	ID     int64 `json:"id"`
	Author struct {
		ID int64 `json:"id"`
	} `json:"author"`
}
