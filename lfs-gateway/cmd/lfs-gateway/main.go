package main

import (
	"log"
	"net/http"

	"git.hemehealth.com/yanpeng/gitea-lsf/lfs-gateway/internal/gateway"
)

func main() {
	cfg, err := gateway.LoadConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	store, err := gateway.NewOSSStore(cfg)
	if err != nil {
		log.Fatalf("init oss: %v", err)
	}
	metas, err := gateway.NewMetaStore(cfg)
	if err != nil {
		log.Fatalf("init meta store: %v", err)
	}

	handler := gateway.NewHandler(
		cfg,
		gateway.NewGiteaClient(cfg.GiteaAPIURL, cfg.GiteaWebURL),
		store,
		gateway.NewDownloadSigner(cfg, store),
		metas,
		gateway.NewVerifyTokens(cfg.VerifySecret),
	)

	server := &http.Server{
		Addr:         cfg.Addr,
		Handler:      handler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	log.Printf("gitea-lfs-gateway listening on %s", cfg.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
