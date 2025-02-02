package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	settingspb "github.com/Palats/mastopoof/proto/gen/mastopoof/settings"
	stpb "github.com/Palats/mastopoof/proto/gen/mastopoof/storage"
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
	t    testing.TB
	rwDB *sql.DB
	roDB *sql.DB
	st   *Storage
}

// dbNameCounter makes it so that there is a unique in memory DB for each test.
// If the db connections were closed properly, that would not be an issue -
// probably something to fix. Though in any case, having a DB per test avoids
// issue if tests are run for any reason in parallel.
var dbNameCounter atomic.Int64

func (env *DBTestEnv) Init(ctx context.Context, t testing.TB) *DBTestEnv {
	t.Helper()
	env.t = t
	var err error
	// As storage creates multiple connections, all of them must reach the same
	// in-memory DB. In turns, it means that the connections must be properly closed
	// with env.Close() at the end of the test - otherwise the content won't
	// be empty for the next test.
	n := dbNameCounter.Add(1)
	env.st, err = newStorageNoInit(ctx, fmt.Sprintf("file:memdb%d?mode=memory&cache=shared", n))
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

func (env *DBTestEnv) pickNext(ctx context.Context, userState *stpb.UserState, streamState *stpb.StreamState) (*Item, error) {
	var item *Item
	err := env.st.InTxnRW(ctx, func(ctx context.Context, txn SQLReadWrite) error {
		var err error
		item, err = env.st.pickNextInTxn(ctx, txn, userState, streamState)
		return err
	})
	return item, err
}

func (env *DBTestEnv) mustPickNext(ctx context.Context, userState *stpb.UserState, streamState *stpb.StreamState) *Item {
	env.t.Helper()
	item, err := env.pickNext(ctx, userState, streamState)
	if err != nil {
		env.t.Fatal(err)
	}
	return item
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
	env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, ASID(accountState1.Asid), streamState1, statuses, []*mastodon.Filter{})

	// Create a second user
	userState2, _, streamState2, err := env.st.CreateUser(ctx, nil, "localhost", "456", "user2")
	if err != nil {
		t.Fatal(err)
	}

	// Try to pick some statuses for user 2.
	item := env.mustPickNext(ctx, userState2, streamState2)
	if item != nil {
		t.Errorf("Got item with status ID %v, should have gotten nothing", item.Status.ID)
	}
}

func TestPick(t *testing.T) {
	ctx := context.Background()
	env := (&DBTestEnv{}).Init(ctx, t)
	defer env.Close()

	userState1, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}
	var statuses []*mastodon.Status
	for i := int64(0); i < 4; i++ {
		statuses = append(statuses, testserver.NewFakeStatus(mastodon.ID(strconv.FormatInt(i+10, 10)), "123"))
	}
	env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, ASID(accountState1.Asid), streamState1, statuses, []*mastodon.Filter{})

	// Make sure the statuses that were inserted are available.
	foundIDs := map[mastodon.ID]int{}
	for i := 0; i < len(statuses); i++ {
		item := env.mustPickNext(ctx, userState1, streamState1)
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
	item := env.mustPickNext(ctx, userState1, streamState1)
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

		if seenUIDs[UID(userState.Uid)] {
			t.Errorf("duplicate UID %d", userState.Uid)
		}
		seenUIDs[UID(userState.Uid)] = true

		if seenASIDs[ASID(accountState.Asid)] {
			t.Errorf("duplicate ASID %d", accountState.Asid)
		}
		seenASIDs[ASID(accountState.Asid)] = true

		if seenStIDs[StID(streamState.Stid)] {
			t.Errorf("duplicate StID %d", streamState.Stid)
		}
		seenStIDs[StID(streamState.Stid)] = true
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
	err = env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, ASID(accountState1.Asid), streamState1, []*mastodon.Status{
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
	err = env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, ASID(accountState2.Asid), streamState2, []*mastodon.Status{
		testserver.NewFakeStatus(mastodon.ID("200"), "456"),
		testserver.NewFakeStatus(mastodon.ID("201"), "456"),
	}, []*mastodon.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	err = env.st.InTxnRO(ctx, func(ctx context.Context, txn SQLReadOnly) error {
		// Make sure the statuses that were inserted are available.
		results, err := env.st.SearchByStatusID(ctx, txn, UID(userState1.Uid), "101")
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
		results, err = env.st.SearchByStatusID(ctx, txn, UID(userState1.Uid), "199")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := len(results), 0; got != want {
			t.Errorf("Got %d results, wanted %d; results:\n%v", got, want, results)
		}

		// Check that searchs look only for the provided user statuses.
		results, err = env.st.SearchByStatusID(ctx, txn, UID(userState2.Uid), "101")
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
	err = env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, ASID(accountState1.Asid), streamState1, []*mastodon.Status{
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
	status := &stpb.StatusMeta{}
	err = json.Unmarshal([]byte(got), &status)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Filters) != 2 {
		t.Errorf("Got %d filters, wanted 2", len(status.Filters))
	}
	if status.Filters[0].Id != "123" || !status.Filters[0].Matched {
		t.Errorf("Got filter %#v, wanted {123, true}", status.Filters[0])
	}
	if status.Filters[1].Id != "456" || status.Filters[1].Matched {
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

func getStreamStatusState(ctx context.Context, env *DBTestEnv, withID string) *stpb.StreamStatusState {
	streamStatusState := &stpb.StreamStatusState{}
	row := env.roDB.QueryRowContext(ctx, `
		SELECT stream_status_state FROM streamcontent WHERE status_id = ?
	`, withID)
	if err := row.Scan(SQLProto{streamStatusState}); err != nil {
		env.t.Fatal(err)
	}
	return streamStatusState
}

// Verify that the feature hiding already-seen status detects things properly.
func TestAlreadySeenActive(t *testing.T) {
	ctx := context.Background()
	env := (&DBTestEnv{}).Init(ctx, t)
	defer env.Close()

	// Prep the environment with users and statuses.
	userState1, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}

	// Activate the feature.
	userState1.Settings.SeenReblogs = &settingspb.SettingSeenReblogs{
		Value:    settingspb.SettingSeenReblogs_HIDE,
		Override: true,
	}

	// A basic status
	status1 := testserver.NewFakeStatus(mastodon.ID("101"), "123")
	// Another basic status
	status2 := testserver.NewFakeStatus(mastodon.ID("102"), "123")
	// A reblog of the first status.
	status3 := testserver.NewFakeStatus(mastodon.ID("103"), "123")
	status3.Reblog = status2
	// A reblog of a never seen before status
	status4 := testserver.NewFakeStatus(mastodon.ID("104"), "123")
	status4.Reblog = testserver.NewFakeStatus(mastodon.ID("991"), "123")
	// A reblog of a reblog
	status5 := testserver.NewFakeStatus(mastodon.ID("105"), "123")
	status5.Reblog = status4

	err = env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, ASID(accountState1.Asid), streamState1, []*mastodon.Status{
		status1, status2, status3, status4, status5,
	}, []*mastodon.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	env.mustPickNext(ctx, userState1, streamState1)
	if got, want := getStreamStatusState(ctx, env, "101").AlreadySeen, stpb.StreamStatusState_NO; got != want {
		t.Errorf("Got AlreadySeen = %v, wanted %v", got, want)
	}

	env.mustPickNext(ctx, userState1, streamState1)
	if got, want := getStreamStatusState(ctx, env, "102").AlreadySeen, stpb.StreamStatusState_NO; got != want {
		t.Errorf("Got AlreadySeen = %v, wanted %v", got, want)
	}

	env.mustPickNext(ctx, userState1, streamState1)
	if got, want := getStreamStatusState(ctx, env, "103").AlreadySeen, stpb.StreamStatusState_YES; got != want {
		t.Errorf("Got AlreadySeen = %v, wanted %v", got, want)
	}

	env.mustPickNext(ctx, userState1, streamState1)
	if got, want := getStreamStatusState(ctx, env, "104").AlreadySeen, stpb.StreamStatusState_NO; got != want {
		t.Errorf("Got AlreadySeen = %v, wanted %v", got, want)
	}

	env.mustPickNext(ctx, userState1, streamState1)
	if got, want := getStreamStatusState(ctx, env, "105").AlreadySeen, stpb.StreamStatusState_YES; got != want {
		t.Errorf("Got AlreadySeen = %v, wanted %v", got, want)
	}
}

// Verify that the feature hiding already-seen does not try thing when not active.
func TestAlreadySeenInactive(t *testing.T) {
	ctx := context.Background()
	env := (&DBTestEnv{}).Init(ctx, t)
	defer env.Close()

	// Prep the environment with users and statuses.
	userState1, accountState1, streamState1, err := env.st.CreateUser(ctx, nil, "localhost", "123", "user1")
	if err != nil {
		t.Fatal(err)
	}

	// Activate the feature.
	userState1.Settings.SeenReblogs = &settingspb.SettingSeenReblogs{
		Value:    settingspb.SettingSeenReblogs_DISPLAY,
		Override: true,
	}

	// A basic status
	status1 := testserver.NewFakeStatus(mastodon.ID("101"), "123")
	// A reblog of the first status.
	status2 := testserver.NewFakeStatus(mastodon.ID("102"), "123")
	status2.Reblog = status1
	// A reblog of a never seen before status
	status3 := testserver.NewFakeStatus(mastodon.ID("103"), "123")
	status3.Reblog = testserver.NewFakeStatus(mastodon.ID("991"), "123")
	// A reblog of a reblog
	status4 := testserver.NewFakeStatus(mastodon.ID("104"), "123")
	status4.Reblog = status3

	err = env.st.InsertStatuses(ctx, sqlAdapter{env.rwDB}, ASID(accountState1.Asid), streamState1, []*mastodon.Status{
		status1, status2, status3, status4,
	}, []*mastodon.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	env.mustPickNext(ctx, userState1, streamState1)
	if got, want := getStreamStatusState(ctx, env, "101").AlreadySeen, stpb.StreamStatusState_UNKNOWN; got != want {
		t.Errorf("Got AlreadySeen = %v, wanted %v", got, want)
	}

	env.mustPickNext(ctx, userState1, streamState1)
	if got, want := getStreamStatusState(ctx, env, "102").AlreadySeen, stpb.StreamStatusState_UNKNOWN; got != want {
		t.Errorf("Got AlreadySeen = %v, wanted %v", got, want)
	}

	env.mustPickNext(ctx, userState1, streamState1)
	if got, want := getStreamStatusState(ctx, env, "103").AlreadySeen, stpb.StreamStatusState_UNKNOWN; got != want {
		t.Errorf("Got AlreadySeen = %v, wanted %v", got, want)
	}

	env.mustPickNext(ctx, userState1, streamState1)
	if got, want := getStreamStatusState(ctx, env, "104").AlreadySeen, stpb.StreamStatusState_UNKNOWN; got != want {
		t.Errorf("Got AlreadySeen = %v, wanted %v", got, want)
	}
}
