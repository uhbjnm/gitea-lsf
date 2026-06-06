package gateway

import (
	"context"
	"regexp"
	"testing"

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
