package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

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
	// As storage creates multiple connections, all of them must reach the same
	// in-memory DB. In turns, it means that the connections must be properly closed
	// with env.Close() at the end of the test - otherwise the content won't
	// be empty for the next test.
	env.st, err = newStorageNoInit(ctx, "file::memory:?cache=shared", "", "read")
	if err != nil {
		t.Fatal(err)
	}
	env.db = env.st.rwDB

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

func (env *DBTestEnv) Close() {
	if env.st != nil {
		env.st.Close()
	}
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

// TestNoCrossUserStatuses verifies that fetching statuses for a new users does not pick
// statuses from another user.
func TestNoCrossUserStatuses(t *testing.T) {
	ctx := context.Background()
	env := (&DBTestEnv{}).Init(ctx, t)
	defer env.Close()

	// Create a user and add some statuses.
	_, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}

	var statuses []*mastodon.Status
	for i := int64(0); i < 10; i++ {
		statuses = append(statuses, testserver.NewFakeStatus(mastodon.ID(strconv.FormatInt(i+10, 10)), "123"))
	}
	env.st.InsertStatuses(ctx, env.db, accountState1.ASID, streamState1, statuses, []*mastodon.Filter{})

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
	defer env.Close()

	_, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}
	var statuses []*mastodon.Status
	for i := int64(0); i < 4; i++ {
		statuses = append(statuses, testserver.NewFakeStatus(mastodon.ID(strconv.FormatInt(i+10, 10)), "123"))
	}
	env.st.InsertStatuses(ctx, env.db, accountState1.ASID, streamState1, statuses, []*mastodon.Filter{})

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

// TestCreateStateStateIncreases verifies that stream IDs
// are not accidently reused.
func TestCreateStreamStateIncreases(t *testing.T) {
	ctx := context.Background()

	// Prepare at version 14
	env := (&DBTestEnv{}).Init(ctx, t)
	defer env.Close()

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
func TestV18ToV18(t *testing.T) {
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

func TestSearchStatusID(t *testing.T) {
	ctx := context.Background()
	env := (&DBTestEnv{}).Init(ctx, t)
	defer env.Close()

	// Prep the environment with users and statuses.
	userState1, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}
	err = env.st.InsertStatuses(ctx, env.db, accountState1.ASID, streamState1, []*mastodon.Status{
		testserver.NewFakeStatus(mastodon.ID("100"), "123"),
		testserver.NewFakeStatus(mastodon.ID("101"), "123"),
		testserver.NewFakeStatus(mastodon.ID("102"), "123"),
	}, []*mastodon.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	userState2, accountState2, streamState2, err := env.st.CreateUser(ctx, nil, "localhost", "456", "user2")
	if err != nil {
		t.Fatal(err)
	}
	err = env.st.InsertStatuses(ctx, env.db, accountState2.ASID, streamState2, []*mastodon.Status{
		testserver.NewFakeStatus(mastodon.ID("200"), "456"),
		testserver.NewFakeStatus(mastodon.ID("201"), "456"),
	}, []*mastodon.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	err = env.st.InTxnRO(ctx, func(ctx context.Context, txn SQLReadOnly) error {
		// Make sure the statuses that were inserted are available.
		results, err := env.st.SearchByStatusID(ctx, txn, userState1.UID, "101")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := len(results), 1; got != want {
			t.Errorf("Got %d results, wanted %d; results:\n%v", got, want, results)
		}
		if got, want := results[0].Position, int64(0); got != want {
			t.Errorf("Got position %d, wanted %d", got, want)
		}

		// Search for an unknown status.
		results, err = env.st.SearchByStatusID(ctx, txn, userState1.UID, "199")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := len(results), 0; got != want {
			t.Errorf("Got %d results, wanted %d; results:\n%v", got, want, results)
		}

		// Check that searchs look only for the provided user statuses.
		results, err = env.st.SearchByStatusID(ctx, txn, userState2.UID, "101")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := len(results), 0; got != want {
			t.Errorf("Got %d results, wanted %d; results:\n%v", got, want, results)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFilters(t *testing.T) {
	ctx := context.Background()
	env := (&DBTestEnv{}).Init(ctx, t)
	defer env.Close()

	_, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}

	f1 := mastodon.Filter{"123", "content", []string{"home"}, false, time.Unix(0, 0), true}
	f2 := mastodon.Filter{"456", "smurf", []string{"home"}, false, time.Unix(0, 0), true}
	err = env.st.InsertStatuses(ctx, env.db, accountState1.ASID, streamState1, []*mastodon.Status{
		testserver.NewFakeStatus(mastodon.ID("100"), "123"),
		testserver.NewFakeStatus(mastodon.ID("101"), "123"),
		testserver.NewFakeStatus(mastodon.ID("102"), "123"),
	}, []*mastodon.Filter{&f1, &f2})
	if err != nil {
		t.Fatal(err)
	}

	var got string
	var num uint64
	err = env.db.QueryRowContext(ctx, `SELECT DISTINCT statusstate, count(DISTINCT statusstate) from statuses`).Scan(&got, &num)
	if err == sql.ErrNoRows {
		t.Fatal(err)
	}
	if num != 1 {
		t.Errorf("Got %d lines, expected1", num)
	}
	status := StatusState{}
	err = json.Unmarshal([]byte(got), &status)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Filters) != 2 {
		t.Errorf("Got %d filters, wanted 2", len(status.Filters))
	}
	if status.Filters[0].ID != "123" || !status.Filters[0].Matched {
		t.Errorf("Got filter %#v, wanted {123, true}", status.Filters[0])
	}
	if status.Filters[1].ID != "456" || status.Filters[1].Matched {
		t.Errorf("Got filter %#v, wanted {456, false}", status.Filters[1])
	}
}

func TestComputeState(t *testing.T) {
	status := mastodon.Status{
		Content: "Here is some text to be able to filter <span>#</span>filter #<span>NoFilter</span>",
		Tags:    make([]mastodon.Tag, 0),
	}
	status.Tags = append(status.Tags, mastodon.Tag{Name: "filter"})
	status.Tags = append(status.Tags, mastodon.Tag{Name: "nofilter"})

	f1 := mastodon.Filter{ID: "1", Phrase: "#nofilter"}
	f2 := mastodon.Filter{ID: "2", Phrase: "Filters"}
	f3 := mastodon.Filter{ID: "3", Phrase: "smurf"}
	f4 := mastodon.Filter{ID: "3", Phrase: "text"}

	statusState := computeState(&status, []*mastodon.Filter{&f1, &f2, &f3, &f4})
	if len(statusState.Filters) != 4 {
		t.Errorf("Got %d filters, wanted 1", len(statusState.Filters))
	}
	if !statusState.Filters[0].Matched ||
		statusState.Filters[1].Matched ||
		statusState.Filters[2].Matched ||
		!statusState.Filters[3].Matched {
		t.Errorf("Got filter %#v, wanted {true, false, false, true}", statusState.Filters)
	}

}
