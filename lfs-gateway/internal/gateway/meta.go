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
	ResolveReleaseUpload(ctx context.Context, owner, repo, username string) (int64, int64, error)
	EnsureAttachment(ctx context.Context, attachment releaseAttachment) error
}

type noopMetaStore struct{}

func (noopMetaStore) Get(context.Context, int64, string) (objectMeta, bool, error) {
	return objectMeta{}, false, nil
}

func (noopMetaStore) Upsert(context.Context, int64, objectRequest) error {
	return nil
}

func (noopMetaStore) EnsureAttachment(context.Context, releaseAttachment) error {
	return errors.New("release attachment metadata store is disabled")
}

func (noopMetaStore) ResolveReleaseUpload(context.Context, string, string, string) (int64, int64, error) {
	return 0, 0, errors.New("release attachment metadata store is disabled")
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

func (s *PostgresMetaStore) EnsureAttachment(ctx context.Context, attachment releaseAttachment) error {
	createdUnix := attachment.CreatedAt.Unix()
	if createdUnix <= 0 {
		createdUnix = time.Now().Unix()
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO attachment
		 (uuid, repo_id, issue_id, release_id, uploader_id, comment_id, name, download_count, size, created_unix)
		 VALUES ($1, $2, 0, $3, $4, 0, $5, 0, $6, $7)
		 ON CONFLICT (uuid) DO NOTHING`,
		attachment.UUID,
		attachment.RepoID,
		attachment.ReleaseID,
		attachment.UploaderID,
		attachment.Name,
		attachment.Size,
		createdUnix,
	)
	if err != nil {
		return fmt.Errorf("insert release attachment: %w", err)
	}

	var stored releaseAttachment
	err = s.db.QueryRowContext(
		ctx,
		`SELECT uuid, repo_id, release_id, uploader_id, name, size, created_unix
		 FROM attachment WHERE uuid = $1`,
		attachment.UUID,
	).Scan(
		&stored.UUID,
		&stored.RepoID,
		&stored.ReleaseID,
		&stored.UploaderID,
		&stored.Name,
		&stored.Size,
		&createdUnix,
	)
	if err != nil {
		return fmt.Errorf("load release attachment: %w", err)
	}
	if stored.UUID != attachment.UUID || stored.RepoID != attachment.RepoID || stored.ReleaseID != attachment.ReleaseID ||
		stored.UploaderID != attachment.UploaderID || stored.Name != attachment.Name || stored.Size != attachment.Size {
		return errors.New("release attachment UUID already belongs to different metadata")
	}
	return nil
}

func (s *PostgresMetaStore) ResolveReleaseUpload(ctx context.Context, owner, repo, username string) (int64, int64, error) {
	var repoID, uploaderID int64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT repository.id, uploader.id
		 FROM repository
		 JOIN "user" owner ON owner.id = repository.owner_id
		 CROSS JOIN "user" uploader
		 WHERE lower(owner.name) = lower($1)
		   AND lower(repository.name) = lower($2)
		   AND lower(uploader.name) = lower($3)`,
		owner,
		repo,
		username,
	).Scan(&repoID, &uploaderID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, errNotFound
	}
	if err != nil {
		return 0, 0, fmt.Errorf("resolve release upload identities: %w", err)
	}
	return repoID, uploaderID, nil
}
