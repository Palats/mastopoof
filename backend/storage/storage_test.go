package storage

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type SchemaDB struct {
	Tables []*SchemaTable
}

// Linear produces a string version of the whole schema suitable for line-based diffing.
func (s *SchemaDB) Linear() string {
	resp := ""
	for _, table := range s.Tables {
		for _, c := range table.Columns {
			resp += fmt.Sprintf("table=%s, cid=%d, name=%s, type=%s, notnull=%v, dflt_value=(%t, %q), pk=%d\n",
				c.TableName, c.CID, c.Name, c.Type, c.NotNull, c.DefaultValue.Valid, c.DefaultValue.String, c.PrimaryKey,
			)
		}
	}
	return resp
}

type SchemaTable struct {
	TableName string
	Columns   []*SchemaColumn
}

type SchemaColumn struct {
	TableName string

	CID          int64
	Name         string
	Type         string
	NotNull      string
	DefaultValue sql.NullString
	PrimaryKey   int64
}

func canonicalSchema(ctx context.Context, db *sql.DB) (*SchemaDB, error) {
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
		return nil, fmt.Errorf("unable to obtain schema info: %w", err)
	}

	tableNames := []string{}
	for tablesRows.Next() {
		var tableName string
		if err := tablesRows.Scan(&tableName); err != nil {
			return nil, fmt.Errorf("unable to extract table name: %w", err)
		}
		tableNames = append(tableNames, tableName)
	}
	if err := tablesRows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list tables: %w", err)
	}

	// And for each tables, find all the columns.
	schemaDB := &SchemaDB{}
	for _, tableName := range tableNames {
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
				name
		`)

		columnsRows, err := db.QueryContext(ctx, query, tableName)
		if err != nil {
			return nil, fmt.Errorf("unable to get columns info for %s: %w", tableName, err)
		}

		schemaTable := &SchemaTable{
			TableName: tableName,
		}
		schemaDB.Tables = append(schemaDB.Tables, schemaTable)

		// Go over the columns of the table.
		for columnsRows.Next() {
			schemaColumn := &SchemaColumn{
				TableName: tableName,
			}
			schemaTable.Columns = append(schemaTable.Columns, schemaColumn)
			if err := columnsRows.Scan(&schemaColumn.CID, &schemaColumn.Name, &schemaColumn.Type, &schemaColumn.NotNull, &schemaColumn.DefaultValue, &schemaColumn.PrimaryKey); err != nil {
				return nil, fmt.Errorf("unable to extract column info in table %s: %w", tableName, err)
			}

		}
		if err := columnsRows.Err(); err != nil {
			return nil, fmt.Errorf("failed to list columns for %s: %w", tableName, err)
		}
	}
	return schemaDB, nil
}

// TestDBCreate verifies that a DB created from scratch - i.e., following all
// update steps - is the same as the canonical schema.
func TestDBCreate(t *testing.T) {
	ctx := context.Background()

	// Create a reference database schema.
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

	// Create a mastopoof database.
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	st, err := NewStorage(db, "", "read")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Init(ctx); err != nil {
		t.Fatal(err)
	}
	sch, err := canonicalSchema(ctx, st.DB)
	if err != nil {
		t.Fatal(err)
	}

	// And compare them.
	if diff := cmp.Diff(refSch, sch); diff != "" {
		t.Errorf("DB schema mismatch (-want +got):\n%s", diff)
	}
}

// TestV12ToV13 verifies that accountstate table recreation
// works (new name for field, strict).
func TestV12ToV13(t *testing.T) {
	ctx := context.Background()

	// Prepare at version 12
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	st, err := NewStorage(db, "", "read")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.initVersion(ctx, 12); err != nil {
		t.Fatal(err)
	}

	// Insert some dummy data that will need to be converted
	// JSON content is likely written as []byte, thus producing BLOB
	// value - so also test that.
	sqlStmt := `
		INSERT INTO accountstate (asid, content, uid) VALUES
			(2, '{"username": "testuser1", "uid": 1}', 1),
			(3, CAST('{"username": "testuser2", "uid": 2}' AS BLOB), 2)
		;
	`
	if _, err := db.ExecContext(ctx, sqlStmt); err != nil {
		t.Fatal(err)
	}

	// Update to version 13
	if err := prepareDB(ctx, db, 13); err != nil {
		t.Fatal(err)
	}

	// Verify that the account state can be loaded.
	accountState, err := st.AccountStateByUID(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := accountState.Username, "testuser1"; got != want {
		t.Errorf("Got username %s, wanted %s", got, want)
	}

	accountState, err = st.AccountStateByUID(ctx, db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := accountState.Username, "testuser2"; got != want {
		t.Errorf("Got username %s, wanted %s", got, want)
	}
}
