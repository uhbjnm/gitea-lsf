package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type objReq struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type batchReq struct {
	Operation string   `json:"operation"`
	Objects   []objReq `json:"objects"`
}

type lfsLink struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type objResp struct {
	OID           string             `json:"oid"`
	Size          int64              `json:"size"`
	Authenticated bool               `json:"authenticated,omitempty"`
	Actions       map[string]lfsLink `json:"actions,omitempty"`
}

type batchResp struct {
	Transfer string    `json:"transfer,omitempty"`
	Objects  []objResp `json:"objects"`
}

type server struct {
	baseURL string
	mu      sync.Mutex
	data    map[string][]byte
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18081", "listen address")
	flag.Parse()

	s := &server{
		baseURL: "http://" + *addr,
		data:    map[string][]byte{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/acme/demo.git/info/lfs/objects/batch", s.batch)
	mux.HandleFunc("/verify/", s.verify)
	mux.HandleFunc("/oss/", s.put)
	mux.HandleFunc("/cdn/", s.get)

	log.Printf("fake git-lfs server listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func (s *server) batch(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	var req batchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp := batchResp{Transfer: "basic"}
	for _, obj := range req.Objects {
		out := objResp{OID: obj.OID, Size: obj.Size, Authenticated: true}
		switch req.Operation {
		case "upload":
			s.mu.Lock()
			_, exists := s.data[obj.OID]
			s.mu.Unlock()
			if !exists {
				out.Actions = map[string]lfsLink{
					"upload": {
						Href: s.baseURL + "/oss/" + obj.OID,
						Header: map[string]string{
							"Content-Type":           "application/octet-stream",
							"x-oss-forbid-overwrite": "true",
						},
						ExpiresAt: time.Now().Add(time.Hour).UTC(),
					},
					"verify": {
						Href:      s.baseURL + "/verify/" + obj.OID,
						ExpiresAt: time.Now().Add(time.Hour).UTC(),
					},
				}
			}
		case "download":
			out.Actions = map[string]lfsLink{
				"download": {
					Href:      s.baseURL + "/cdn/" + obj.OID + "?auth_key=test",
					ExpiresAt: time.Now().Add(time.Hour).UTC(),
				},
			}
		default:
			http.Error(w, "invalid operation", http.StatusBadRequest)
			return
		}
		resp.Objects = append(resp.Objects, out)
	}

	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *server) put(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s header=%s", r.Method, r.URL.Path, r.Header.Get("x-oss-forbid-overwrite"))
	if r.Header.Get("x-oss-forbid-overwrite") != "true" {
		http.Error(w, "missing x-oss-forbid-overwrite", http.StatusBadRequest)
		return
	}

	key := strings.TrimPrefix(r.URL.Path, "/oss/")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != key {
		http.Error(w, "hash mismatch", http.StatusUnprocessableEntity)
		return
	}

	s.mu.Lock()
	s.data[key] = body
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *server) verify(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	key := strings.TrimPrefix(r.URL.Path, "/verify/")
	var obj objReq
	_ = json.NewDecoder(r.Body).Decode(&obj)

	s.mu.Lock()
	body, exists := s.data[key]
	s.mu.Unlock()
	if !exists || int64(len(body)) != obj.Size {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	fmt.Fprint(w, "{}")
}

func (s *server) get(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.String())
	key := strings.TrimPrefix(r.URL.Path, "/cdn/")

	s.mu.Lock()
	body, exists := s.data[key]
	s.mu.Unlock()
	if !exists {
		http.NotFound(w, r)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
