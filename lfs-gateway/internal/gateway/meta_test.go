package gateway

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresMetaStoreGet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT size FROM lfs_meta_object WHERE repository_id = $1 AND oid = $2`)).
		WithArgs(int64(42), testOID).
		WillReturnRows(sqlmock.NewRows([]string{"size"}).AddRow(int64(12)))

	store := &PostgresMetaStore{db: db}
	meta, ok, err := store.Get(context.Background(), 42, testOID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || meta.Size != 12 {
		t.Fatalf("meta = %+v, ok = %v", meta, ok)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMetaStoreEnsureAttachment(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	attachment := releaseAttachment{
		UUID: "01234567-89ab-4def-8123-456789abcdef", RepoID: 42, ReleaseID: 99, UploaderID: 7,
		Name: "setup.zip", Size: 12, CreatedAt: time.Unix(100, 0),
	}
	mock.ExpectExec("INSERT INTO attachment").
		WithArgs(attachment.UUID, attachment.RepoID, attachment.ReleaseID, attachment.UploaderID, attachment.Name, attachment.Size, int64(100)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT uuid, repo_id, release_id, uploader_id, name, size, created_unix").
		WithArgs(attachment.UUID).
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "repo_id", "release_id", "uploader_id", "name", "size", "created_unix"}).
			AddRow(attachment.UUID, attachment.RepoID, attachment.ReleaseID, attachment.UploaderID, attachment.Name, attachment.Size, int64(100)))

	store := &PostgresMetaStore{db: db}
	if err := store.EnsureAttachment(context.Background(), attachment); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMetaStoreResolveReleaseUpload(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT repository.id, uploader.id").
		WithArgs("owner", "repo", "release-user").
		WillReturnRows(sqlmock.NewRows([]string{"repo_id", "uploader_id"}).AddRow(int64(42), int64(7)))

	store := &PostgresMetaStore{db: db}
	repoID, uploaderID, err := store.ResolveReleaseUpload(context.Background(), "owner", "repo", "release-user")
	if err != nil {
		t.Fatal(err)
	}
	if repoID != 42 || uploaderID != 7 {
		t.Fatalf("repoID = %d, uploaderID = %d", repoID, uploaderID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMetaStoreUpsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO lfs_meta_object (oid, size, repository_id, created_unix, updated_unix)
		 VALUES ($1, $2, $3, $4, $4)
		 ON CONFLICT (repository_id, oid)
		 DO UPDATE SET size = EXCLUDED.size, updated_unix = EXCLUDED.updated_unix`)).
		WithArgs(testOID, int64(12), int64(42), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	store := &PostgresMetaStore{db: db}
	if err := store.Upsert(context.Background(), 42, objectRequest{OID: testOID, Size: 12}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
