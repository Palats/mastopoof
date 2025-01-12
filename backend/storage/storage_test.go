package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	"github.com/mattn/go-mastodon"
)

type DBTestEnv struct {
	// -- To be provided
	// Create the DB up to this version.
	// If 0, uses the most recent one.
	targetVersion int
	// Run this as SQL after setup.
	sqlInit string

	// -- Available after Init
	rwDB *sql.DB
	roDB *sql.DB
	st   *Storage
}

func (env *DBTestEnv) Init(ctx context.Context, t testing.TB) *DBTestEnv {
	t.Helper()
	var err error
	// As storage creates multiple connections, all of them must reach the same
	// in-memory DB. In turns, it means that the connections must be properly closed
	// with env.Close() at the end of the test - otherwise the content won't
	// be empty for the next test.
	env.st, err = newStorageNoInit(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	env.rwDB = env.st.rwDB
	env.roDB = env.st.roDB

	v := env.targetVersion
	if v == 0 {
		v = maxSchemaVersion
	}
	if err := env.st.initVersion(ctx, v); err != nil {
		t.Fatal(err)
	}

	if env.sqlInit != "" {
		if _, err := env.rwDB.ExecContext(ctx, env.sqlInit); err != nil {
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
	env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, accountState1.ASID, streamState1, statuses, []*mastodon.Filter{})

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
	env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, accountState1.ASID, streamState1, statuses, []*mastodon.Filter{})

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

func TestSearchStatusID(t *testing.T) {
	ctx := context.Background()
	env := (&DBTestEnv{}).Init(ctx, t)
	defer env.Close()

	// Prep the environment with users and statuses.
	userState1, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}
	err = env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, accountState1.ASID, streamState1, []*mastodon.Status{
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
	err = env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, accountState2.ASID, streamState2, []*mastodon.Status{
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
	err = env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, accountState1.ASID, streamState1, []*mastodon.Status{
		testserver.NewFakeStatus(mastodon.ID("100"), "123"),
		testserver.NewFakeStatus(mastodon.ID("101"), "123"),
		testserver.NewFakeStatus(mastodon.ID("102"), "123"),
	}, []*mastodon.Filter{&f1, &f2})
	if err != nil {
		t.Fatal(err)
	}

	var got string
	var num uint64
	err = env.roDB.QueryRowContext(ctx, `SELECT DISTINCT status_meta, count(DISTINCT status_meta) from statuses`).Scan(&got, &num)
	if err == sql.ErrNoRows {
		t.Fatal(err)
	}
	if num != 1 {
		t.Errorf("Got %d lines, expected1", num)
	}
	status := StatusMeta{}
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

	statusMeta := computeStatusMeta(&status, []*mastodon.Filter{&f1, &f2, &f3, &f4})
	if len(statusMeta.Filters) != 4 {
		t.Errorf("Got %d filters, wanted 1", len(statusMeta.Filters))
	}
	if !statusMeta.Filters[0].Matched ||
		statusMeta.Filters[1].Matched ||
		statusMeta.Filters[2].Matched ||
		!statusMeta.Filters[3].Matched {
		t.Errorf("Got filter %#v, wanted {true, false, false, true}", statusMeta.Filters)
	}

}
