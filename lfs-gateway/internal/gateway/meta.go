package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type MetaStore interface {
	Get(ctx context.Context, repoID int64, oid string) (objectMeta, bool, error)
	Upsert(ctx context.Context, repoID int64, obj objectRequest) error
}

type noopMetaStore struct{}

func (noopMetaStore) Get(context.Context, int64, string) (objectMeta, bool, error) {
	return objectMeta{}, false, nil
}

func (noopMetaStore) Upsert(context.Context, int64, objectRequest) error {
	return nil
}

type PostgresMetaStore struct {
	db *sql.DB
}

func NewMetaStore(cfg Config) (MetaStore, error) {
	if cfg.MetaDBDSN == "" {
		return noopMetaStore{}, nil
	}
	db, err := sql.Open(cfg.MetaDBDriver, cfg.MetaDBDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &PostgresMetaStore{db: db}, nil
}

func (s *PostgresMetaStore) Get(ctx context.Context, repoID int64, oid string) (objectMeta, bool, error) {
	var size int64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT size FROM lfs_meta_object WHERE repository_id = $1 AND oid = $2`,
		repoID,
		oid,
	).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return objectMeta{}, false, nil
	}
	if err != nil {
		return objectMeta{}, false, err
	}
	return objectMeta{Size: size}, true, nil
}

func (s *PostgresMetaStore) Upsert(ctx context.Context, repoID int64, obj objectRequest) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO lfs_meta_object (oid, size, repository_id, created_unix, updated_unix)
		 VALUES ($1, $2, $3, $4, $4)
		 ON CONFLICT (repository_id, oid)
		 DO UPDATE SET size = EXCLUDED.size, updated_unix = EXCLUDED.updated_unix`,
		obj.OID,
		obj.Size,
		repoID,
		now,
	)
	if err != nil {
		return fmt.Errorf("upsert lfs_meta_object: %w", err)
	}
	return nil
}
