package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
		query := `
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
		`
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
	env := (&DBTestEnv{}).Init(ctx, t)
	defer env.Close()

	sch, err := canonicalSchema(ctx, env.db)
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
	env := (&DBTestEnv{
		targetVersion: 12,
		// Insert some dummy data that will need to be converted
		// JSON content is likely written as []byte, thus producing BLOB
		// value - so also test that.
		sqlInit: `
			INSERT INTO accountstate (asid, content, uid) VALUES
				(2, '{"username": "testuser1", "uid": 1}', 1),
				(3, CAST('{"username": "testuser2", "uid": 2}' AS BLOB), 2)
			;
		`,
	}).Init(ctx, t)
	defer env.Close()

	// Update to last version
	if err := prepareDB(ctx, env.db, maxSchemaVersion); err != nil {
		t.Fatal(err)
	}

	// Verify that the account state can be loaded.
	accountState, err := env.st.FirstAccountStateByUID(ctx, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := accountState.Username, "testuser1"; got != want {
		t.Errorf("Got username %s, wanted %s", got, want)
	}

	accountState, err = env.st.FirstAccountStateByUID(ctx, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := accountState.Username, "testuser2"; got != want {
		t.Errorf("Got username %s, wanted %s", got, want)
	}
}

// TestV13ToV14 verifies userstate conversion to strict.
func TestV13ToV14(t *testing.T) {
	ctx := context.Background()

	// Prepare at version 13
	env := (&DBTestEnv{
		targetVersion: 13,
		// Insert some dummy data that will need to be converted
		// JSON content is likely written as []byte, thus producing BLOB
		// value - so also test that.
		sqlInit: `
			INSERT INTO userstate (uid, state) VALUES
				(2, '{"uid": 2}'),
				(3, CAST('{"uid": 3}' AS BLOB))
			;
		`,
	}).Init(ctx, t)
	defer env.Close()

	// Update to last version.
	if err := prepareDB(ctx, env.db, maxSchemaVersion); err != nil {
		t.Fatal(err)
	}

	// Verify that the account state can be loaded.
	userState, err := env.st.UserState(ctx, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := userState.UID, UID(2); got != want {
		t.Errorf("Got uid %d, wanted %d", got, want)
	}

	userState, err = env.st.UserState(ctx, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := userState.UID, UID(3); got != want {
		t.Errorf("Got uid %d, wanted %d", got, want)
	}
}

// TestV14ToV15 verifies statuses conversion to strict
// and change of uid->asid.
func TestV14ToV15(t *testing.T) {
	ctx := context.Background()

	// Prepare at version 14
	env := (&DBTestEnv{
		targetVersion: 14,
		// Insert some dummy data that will need to be converted
		// JSON content is likely written as []byte, thus producing BLOB
		// value - so also test that.
		sqlInit: `
			INSERT INTO statuses (sid, uid, status, uri) VALUES
				(2, 4, '{"id": "234"}', 'uri1'),
				(3, 4, CAST('{"id": "465"}' as BLOB), 'uri1')
			;
		`,
	}).Init(ctx, t)
	defer env.Close()

	// Try update to last version.
	// There is no user defined, so it should fine to do the uid->asid conversion.
	if got, want := prepareDB(ctx, env.db, maxSchemaVersion), "after update"; !strings.Contains(got.Error(), want) {
		t.Errorf("Got error '%v', but needed error about missing statuses", got)
	}

	_, err := env.st.CreateAccountState(ctx, nil, 4, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}

	if err := prepareDB(ctx, env.db, maxSchemaVersion); err != nil {
		t.Fatal(err)
	}
}

// TestV15ToV16 verifies streamcontent conversion to strict.
func TestV15ToV16(t *testing.T) {
	ctx := context.Background()

	env := (&DBTestEnv{
		targetVersion: 15,
		// Insert some dummy data that will need to be converted
		// JSON content is likely written as []byte, thus producing BLOB
		// value - so also test that.
		sqlInit: `
			INSERT INTO streamcontent (stid, sid, position) VALUES
				(1, 2, 42),
				(2, 3, 43)
			;
		`,
	}).Init(ctx, t)
	defer env.Close()

	if err := prepareDB(ctx, env.db, maxSchemaVersion); err != nil {
		t.Fatal(err)
	}
}

func TestV16ToV17(t *testing.T) {
	ctx := context.Background()

	env := (&DBTestEnv{
		targetVersion: 16,
		sqlInit: `
			INSERT INTO userstate (uid, state) VALUES
				(1, "{'default_stid': 2}")
			;

			INSERT INTO accountstate (uid, asid, state) VALUES
				(1, 3, "")
			;

			INSERT INTO statuses (sid, asid, status) VALUES
			    (1, 3, ""),
				(2, 3, ""),
				(3, 3, ""),
				(4, 3, ""),
				(5, 4, "")
			;

			INSERT INTO streamcontent (stid, sid, position) VALUES
				(2, 1, 42),
				(2, 2, 43)
			;
		`,
	}).Init(ctx, t)
	defer env.Close()

	if err := prepareDB(ctx, env.db, maxSchemaVersion); err != nil {
		t.Fatal(err)
	}

	var got int64
	err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM streamcontent WHERE stid = 2`,
	).Scan(&got)
	if err == sql.ErrNoRows {
		t.Fatal(err)
	}

	if got != 4 {
		t.Errorf("Got %d statuses, expected 4", got)
	}
}

// TestV17ToV18 verifies streamstate conversion to strict
func TestV17ToV18(t *testing.T) {
	ctx := context.Background()

	env := (&DBTestEnv{
		targetVersion: 17,
		// Insert some dummy data that will need to be converted
		// JSON content is likely written as []byte, thus producing BLOB
		// value - so also test that.
		sqlInit: `
			INSERT INTO streamstate (stid, state) VALUES
				(2, '{"stid": "234"}'),
				(3, CAST('{"stid": "465"}' as BLOB))
			;
		`,
	}).Init(ctx, t)
	defer env.Close()

	if err := prepareDB(ctx, env.db, maxSchemaVersion); err != nil {
		t.Fatal(err)
	}
}

// TestV18ToV19 verifies serverstate conversion to strict
// and renaming.
func TestV18ToV19(t *testing.T) {
	ctx := context.Background()

	env := (&DBTestEnv{
		targetVersion: 18,
		// Insert some dummy data that will need to be converted
		// JSON content is likely written as []byte, thus producing BLOB
		// value - so also test that.
		sqlInit: `
			INSERT INTO serverstate (state, key) VALUES
				('{"server_addr": "234"}', "key1"),
				(CAST('{"server_addr": "465"}' as BLOB), "key2")
			;
		`,
	}).Init(ctx, t)
	defer env.Close()

	if err := prepareDB(ctx, env.db, maxSchemaVersion); err != nil {
		t.Fatal(err)
	}

	// Verify data under the new name.
	var got int64
	err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM appregstate`,
	).Scan(&got)
	if err == sql.ErrNoRows {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("Got %d entries, expected 2", got)
	}
}

func TestV19ToV20(t *testing.T) {
	ctx := context.Background()

	env := (&DBTestEnv{
		targetVersion: 19,
		sqlInit: `
			INSERT INTO statuses (sid, asid, status) VALUES
				(1, 3, ""),
				(2, 3, "")
			;
	`}).Init(ctx, t)
	defer env.Close()

	if err := prepareDB(ctx, env.db, maxSchemaVersion); err != nil {
		t.Fatal(err)
	}

	var got string
	var num uint64
	err := env.db.QueryRowContext(ctx, `SELECT DISTINCT statusstate, count(DISTINCT statusstate) from statuses`).Scan(&got, &num)
	if err == sql.ErrNoRows {
		t.Fatal(err)
	}
	if num != 1 || got != "{}" {
		t.Errorf("Got %d lines with %s as first result, expected single line containing '{}'", num, got)
	}
}
