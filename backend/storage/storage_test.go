package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	"github.com/google/go-cmp/cmp"
	"github.com/mattn/go-mastodon"
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

type DBTestEnv struct {
	// -- To be provided
	// Create the DB up to this version.
	// If 0, uses the most recent one.
	targetVersion int
	// Run this as SQL after setup.
	sqlInit string

	// -- Available after Init
	db *sql.DB
	st *Storage
}

func (env *DBTestEnv) Init(ctx context.Context, t testing.TB) *DBTestEnv {
	t.Helper()
	var err error
	env.db, err = sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	env.st, err = NewStorage(env.db, "", "read")
	if err != nil {
		t.Fatal(err)
	}

	v := env.targetVersion
	if v == 0 {
		v = maxSchemaVersion
	}
	if err := env.st.initVersion(ctx, v); err != nil {
		t.Fatal(err)
	}

	if env.sqlInit != "" {
		if _, err := env.db.ExecContext(ctx, env.sqlInit); err != nil {
			t.Fatal(err)
		}
	}

	return env
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

	// Update to last version
	if err := prepareDB(ctx, env.db, maxSchemaVersion); err != nil {
		t.Fatal(err)
	}

	// Verify that the account state can be loaded.
	accountState, err := env.st.AccountStateByUID(ctx, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := accountState.Username, "testuser1"; got != want {
		t.Errorf("Got username %s, wanted %s", got, want)
	}

	accountState, err = env.st.AccountStateByUID(ctx, nil, 2)
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

// TestNoCrossUserStatuses verifies that fetching statuses for a new users does not pick
// statuses from another user.
func TestNoCrossUserStatuses(t *testing.T) {
	ctx := context.Background()
	env := (&DBTestEnv{}).Init(ctx, t)

	// Create a user and add some statuses.
	_, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}

	var statuses []*mastodon.Status
	for i := int64(0); i < 10; i++ {
		statuses = append(statuses, testserver.NewFakeStatus(mastodon.ID(strconv.FormatInt(i+10, 10)), "123"))
	}
	env.st.InsertStatuses(ctx, env.db, accountState1.ASID, streamState1, statuses)

	// Create a second user
	_, _, streamState2, err := env.st.CreateUser(ctx, nil, "localhost", "456", "user2")
	if err != nil {
		t.Fatal(err)
	}

	// Try to pick some statuses for user 2.
	item, err := env.st.PickNext(ctx, streamState2.StID)
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Errorf("Got item with status ID %v, should have gotten nothing", item.Status.ID)
	}
}

func TestPick(t *testing.T) {
	ctx := context.Background()
	env := (&DBTestEnv{}).Init(ctx, t)

	_, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}
	var statuses []*mastodon.Status
	for i := int64(0); i < 4; i++ {
		statuses = append(statuses, testserver.NewFakeStatus(mastodon.ID(strconv.FormatInt(i+10, 10)), "123"))
	}
	env.st.InsertStatuses(ctx, env.db, accountState1.ASID, streamState1, statuses)

	// Make sure the statuses that were inserted are available.
	foundIDs := map[mastodon.ID]int{}
	for i := 0; i < len(statuses); i++ {
		item, err := env.st.PickNext(ctx, streamState1.StID)
		if err != nil {
			t.Fatal(err)
		}
		if item == nil {
			t.Fatalf("Got no statuses, while one was expected")
		}
		foundIDs[item.Status.ID]++
	}
	// Verify that each status was returned.
	for _, status := range statuses {
		if got, want := foundIDs[status.ID], 1; got != want {
			t.Errorf("Got %d copy of status %v, wanted %d", got, status.ID, want)
		}
	}

	// But no more.
	item, err := env.st.PickNext(ctx, streamState1.StID)
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Errorf("Got status ID %v, while none was expected", item.Status.ID)
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

// TestCreateStateStateIncreases verifies that stream IDs
// are not accidently reused.
func TestCreateStreamStateIncreases(t *testing.T) {
	ctx := context.Background()

	// Prepare at version 14
	env := (&DBTestEnv{}).Init(ctx, t)

	seenUIDs := map[UID]bool{}
	seenASIDs := map[ASID]bool{}
	seenStIDs := map[StID]bool{}
	for i := 1; i < 5; i++ {
		userState, accountState, streamState, err := env.st.CreateUser(ctx, nil, "localhost", mastodon.ID(fmt.Sprintf("%d", i)), fmt.Sprintf("user%d", i))
		if err != nil {
			t.Fatal(err)
		}

		if seenUIDs[userState.UID] {
			t.Errorf("duplicate UID %d", userState.UID)
		}
		seenUIDs[userState.UID] = true

		if seenASIDs[accountState.ASID] {
			t.Errorf("duplicate ASID %d", accountState.ASID)
		}
		seenASIDs[accountState.ASID] = true

		if seenStIDs[streamState.StID] {
			t.Errorf("duplicate StID %d", streamState.StID)
		}
		seenStIDs[streamState.StID] = true
	}
}
