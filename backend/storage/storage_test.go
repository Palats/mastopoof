package storage

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func canonicalSchema(ctx context.Context, db *sql.DB) (string, error) {
	resp := ""

	// Find all tables.
	tablesRows, err := db.QueryContext(ctx, `
		SELECT
			name
		FROM
			sqlite_schema
		WHERE
			type = "table"
			AND name != "sqlite_sequence"
			AND tbl_name != "sessions"
		ORDER BY
			name
	;`)
	if err != nil {
		return "", fmt.Errorf("unable to obtain schema info: %w", err)
	}

	tableNames := []string{}
	for tablesRows.Next() {
		var tableName string
		if err := tablesRows.Scan(&tableName); err != nil {
			return "", fmt.Errorf("unable to extract table name: %w", err)
		}
		tableNames = append(tableNames, tableName)
	}
	if err := tablesRows.Err(); err != nil {
		return "", fmt.Errorf("failed to list tables: %w", err)
	}

	// And for each tables, find all the columns.
	for _, tableName := range tableNames {
		resp += fmt.Sprintf("# %s\n", tableName)

		query := fmt.Sprintf(`
			SELECT
				cid,
				name,
				type,
				'notnull',
				dflt_value,
				pk
			FROM
				pragma_table_info(?)
			ORDER BY
				cid
		`)

		columnsRows, err := db.QueryContext(ctx, query, tableName)
		if err != nil {
			return "", fmt.Errorf("unable to get columns info for %s: %w", tableName, err)
		}

		// Go over the columns of the table.
		for columnsRows.Next() {
			var colCID int64
			var colName string
			var colType string
			var colNotNull string
			var colDefaultValue sql.NullString
			var colPrimaryKey int64
			if err := columnsRows.Scan(&colCID, &colName, &colType, &colNotNull, &colDefaultValue, &colPrimaryKey); err != nil {
				return "", fmt.Errorf("unable to extract column info in table %s: %w", tableName, err)
			}

			resp += fmt.Sprintf("table=%s, cid=%d, name=%s, type=%s, notnull=%v, dflt_value=(%t, %q), pk=%d\n",
				tableName, colCID, colName, colType, colNotNull, colDefaultValue.Valid, colDefaultValue.String, colPrimaryKey,
			)
		}
		if err := columnsRows.Err(); err != nil {
			return "", fmt.Errorf("failed to list columns for %s: %w", tableName, err)
		}

	}
	return resp, nil
}

const refSchema = `
	CREATE TABLE accountstate (
		asid INTEGER PRIMARY KEY,
		content TEXT NOT NULL,
		uid TEXT
	);

	CREATE TABLE serverstate (
		server_addr STRING NOT NULL,
		state TEXT NOT NULL
	);

	CREATE TABLE statuses (
		sid INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL,
		status TEXT NOT NULL,
		uri TEXT
	);

	CREATE TABLE "streamcontent" (
		stid INTEGER NOT NULL,
		sid INTEGER NOT NULL,
		position INTEGER NOT NULL
	);

	CREATE TABLE "streamstate" (
		stid INTEGER PRIMARY KEY,
		state TEXT NOT NULL
	);

	CREATE TABLE userstate (
		uid INTEGER PRIMARY KEY,
		state TEXT NOT NULL
	);
`

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
	sch, err := canonicalSchema(ctx, st.DB)
	if err != nil {
		t.Fatal(err)
	}

	dbRef, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbRef.ExecContext(ctx, refSchema); err != nil {
		t.Fatal(err)
	}

	refSch, err := canonicalSchema(ctx, dbRef)
	if err != nil {
		t.Fatal(err)
	}

	if sch != refSch {
		t.Fatal(sch, "\n\n\n", refSch)
	}
}
