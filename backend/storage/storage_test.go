package storage

import (
	"context"
	"database/sql"
	"testing"
)

func TestDBCreate(t *testing.T) {
	ctx := context.Background()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStorage(db)
	if err := st.Init(ctx); err != nil {
		t.Fatal(err)
	}

	var dbType string
	var dbName string
	var dbSQL string
	_, err = st.DB.QueryContext(ctx, `
		SELECT
			type,
			name,
			sql
		FROM
			sqlite_schema
	;`, &dbType, &dbName, &dbSQL)
	if err != nil {
		t.Fatal(err)
	}
}
