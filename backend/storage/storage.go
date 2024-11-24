// Package storage manages Mastopoof persistence.
// This file is the abstraction between Mastopoof storage and its actual logic.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/alexedwards/scs/sqlite3store"
	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	_ "github.com/mattn/go-sqlite3"
)

var (
	txnCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mastopoof_txn_counter",
		Help: "Storage transactions",
	}, []string{"readonly"})

	actionLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mastopoof_storage_action_seconds",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"action", "code"})

	sqlLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mastopoof_sql_seconds",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"op", "stmt", "code"})
)

func errToCode(err error) string {
	if err == nil {
		return "0"
	}
	return "-1"
}

func recordAction(name string) func(error) {
	start := time.Now()
	return func(err error) {
		d := time.Since(start)
		actionLatency.With(prometheus.Labels{"action": name, "code": errToCode(err)}).Observe(d.Seconds())
	}
}

type SQLReadOnly interface {
	QueryRow(ctx context.Context, name string, query string, args ...any) *sql.Row
	Query(ctx context.Context, name string, query string, args ...any) (*sql.Rows, error)
}

type SQLReadWrite interface {
	SQLReadOnly
	Exec(ctx context.Context, name string, query string, args ...any) (sql.Result, error)
}

type txnInterface interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type sqlAdapter struct {
	pseudoTxn txnInterface
}

func (sa sqlAdapter) QueryRow(ctx context.Context, name string, query string, args ...any) *sql.Row {
	start := time.Now()
	defer func() {
		d := time.Since(start)
		sqlLatency.With(prometheus.Labels{"op": "query-row", "stmt": name, "code": errToCode(nil)}).Observe(d.Seconds())
	}()
	return sa.pseudoTxn.QueryRowContext(ctx, query, args...)
}

func (sa sqlAdapter) Query(ctx context.Context, name string, query string, args ...any) (_ *sql.Rows, retErr error) {
	start := time.Now()
	defer func() {
		d := time.Since(start)
		sqlLatency.With(prometheus.Labels{"op": "query", "stmt": name, "code": errToCode(retErr)}).Observe(d.Seconds())
	}()
	return sa.pseudoTxn.QueryContext(ctx, query, args...)
}

func (sa sqlAdapter) Exec(ctx context.Context, name string, query string, args ...any) (_ sql.Result, retErr error) {
	start := time.Now()
	defer func() {
		d := time.Since(start)
		sqlLatency.With(prometheus.Labels{"op": "exec", "stmt": name, "code": errToCode(retErr)}).Observe(d.Seconds())
	}()
	return sa.pseudoTxn.ExecContext(ctx, query, args...)
}

var ErrNotFound = errors.New("not found")

func IDNewer(id1 mastodon.ID, id2 mastodon.ID) bool {
	// From Mastodon docs https://docs.joinmastodon.org/api/guidelines/#id :
	//  - Sort by size. Newer statuses will have longer IDs.
	//  - Sort lexically. Newer statuses will have at least one digit that is higher when compared positionally.
	if len(id1) != len(id2) {
		return len(id1) > len(id2)
	}
	return id1 > id2
}

func IDLess(id1 mastodon.ID, id2 mastodon.ID) bool {
	if IDNewer(id1, id2) {
		return false
	}
	if id1 == id2 {
		return false
	}
	return true
}

type Storage struct {
	// Readonly access to the database.
	roDB *sql.DB
	// Read-write access to the database.
	rwDB *sql.DB
}

// NewStorage creates a new Mastopoof abstraction layer.
// Parameters:
//   - `dbURL`: the connection URLs to sqlite suitable for Go sql layer.
func NewStorage(ctx context.Context, dbURL string) (returnedSt *Storage, returnedErr error) {
	// Make sure to cleanup everything in case of errors.
	defer func() {
		if returnedErr != nil {
			returnedSt.Close()
		}
	}()

	st, err := newStorageNoInit(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	return st, st.initVersion(ctx, maxSchemaVersion)
}

// Storage setup is largely inspired from https://kerkour.com/sqlite-for-servers
const defaultDBsetup = `
		-- Write-Ahead Log is modern sqlite.
		PRAGMA journal_mode = WAL;
		-- Give more chance to concurrent requests to go through (SQLITE_BUSY)
		PRAGMA busy_timeout = 5000;
		-- Do not sync to disk all the time, but still at moment safe with WAL.
		PRAGMA synchronous = NORMAL;
		-- Give space for around 1G of cache. Value in kibibytes.
		PRAGMA cache_size = -1048576;
		-- Enforce foreign keys.
		PRAGMA foreign_keys = true;
		-- Keep temporary table & indices just in memory. As of 2024-05-12, it is
		-- not used.
		PRAGMA temp_store = memory;
`

func newStorageNoInit(ctx context.Context, dbURI string) (returnedSt *Storage, returnedErr error) {
	// Make sure to cleanup everything in case of errors.
	defer func() {
		if returnedErr != nil {
			returnedSt.Close()
		}
	}()

	st := &Storage{}

	if dbURI == ":memory:" {
		// Just ':memory:' is not parseable as URI, so special case it.
		dbURI = "file::memory:"
	}

	u, err := url.Parse(dbURI)
	if err != nil {
		return nil, fmt.Errorf("unable to parse DB URI %q: %w", dbURI, err)
	}

	inMemory := u.Opaque == ":memory:" || u.Query().Get("mode") == "memory"
	if inMemory {
		q := u.Query()
		// We must have shared cache for in-memory cases, as we get multiple SQL
		// connections. Otherwise, each connection would end up having its own view
		// of the world.
		q.Set("cache", "shared")
		u.RawQuery = q.Encode()
	}

	// -- Write access
	rwURI := *u
	q := rwURI.Query()
	// Do not override mode=memory.
	if !inMemory {
		q.Set("mode", "rwc")
	}
	// Indicate that all transactions are immediate, thus considered as write-txn
	// from the get go. In theory this should be in SQL statement `BEGIN IMMEDIATE`,
	// but Go SQL libraries do not allow for it.
	q.Set("_txlock", "immediate")
	rwURI.RawQuery = q.Encode()

	glog.Infof("Storage URI, read-write: %s", rwURI.String())

	// Start with read-write DB - otherwise, in-memory DB will be created
	// readonly, preventing creation of a read-write connection.
	st.rwDB, err = sql.Open("sqlite3", rwURI.String())
	if err != nil {
		return nil, fmt.Errorf("unable to open storage %s: %w", dbURI, err)
	}
	st.rwDB.SetMaxOpenConns(1)
	if _, err := st.rwDB.ExecContext(ctx, defaultDBsetup); err != nil {
		return nil, fmt.Errorf("unable to configure DB connection: %w", err)
	}

	// Read-only access
	roURI := *u
	q = roURI.Query()
	// Do not override mode=memory.
	if !inMemory {
		q.Set("mode", "ro")
	}
	roURI.RawQuery = q.Encode()
	glog.Infof("Storage URI, read-only: %s", roURI.String())

	st.roDB, err = sql.Open("sqlite3", roURI.String())
	if err != nil {
		return nil, fmt.Errorf("unable to open storage %s: %w", dbURI, err)
	}

	if _, err := st.roDB.ExecContext(ctx, defaultDBsetup); err != nil {
		return nil, fmt.Errorf("unable to configure DB connection: %w", err)
	}

	// Note that MaxOpenConns determine max nesting for SQL queries - i.e., how
	// many nested scan can be running.
	st.roDB.SetMaxOpenConns(max(4, runtime.NumCPU()))

	// Setup some regular optimization according to sqlite doc:
	//  https://www.sqlite.org/lang_analyze.html
	if _, err := st.rwDB.ExecContext(ctx, "PRAGMA optimize=0x10002;"); err != nil {
		return nil, fmt.Errorf("unable set optimize pragma: %w", err)
	}
	// ... that includes background optimization.
	go func() {
		for {
			select {
			case <-time.After(time.Hour + time.Duration(rand.Int63n(5*60))*time.Second):
				if _, err := st.rwDB.ExecContext(ctx, "PRAGMA optimize;"); err != nil {
					glog.ErrorContextf(ctx, "failed to optimize DB: %v", err)
				}
			case <-ctx.Done():
				glog.Infof("stopping background DB optimize, context: %v", ctx.Err())
				return
			}
		}
	}()

	return st, nil
}

// Close cleans up resources. It is safe to call it with a nil instance
// or on a badly initialized storage.
func (st *Storage) Close() error {
	if st == nil {
		return nil
	}
	if st.rwDB != nil {
		st.rwDB.Close()
		st.rwDB = nil
	}
	if st.roDB != nil {
		st.roDB.Close()
		st.roDB = nil
	}
	return nil
}

func (st *Storage) initVersion(ctx context.Context, targetVersion int) error {
	return prepareDB(ctx, st.rwDB, targetVersion)
}

func (st *Storage) NewSCSStore() *sqlite3store.SQLite3Store {
	// This should probbly differentiate between read-heavy cookie management and
	// the occasional writes. However, once that starts to be an actually issue,
	// splitting SCS store into its own DB would probably make more sense.
	return sqlite3store.New(st.rwDB)
}

// ErrCleanAbortTxn signals that the transaction must not be committed, but that it
// is not an error.
var ErrCleanAbortTxn = errors.New("transaction cancellation requested")

// InTxn runs the provided code in a DB transaction.
// If `f` returns an error matching `CleanAbortTxn` (using `errors.Is`), the transaction is rolledback, but InTxn return nil
// If `f` returns another non-nil error, the transaction will be aborted and the error is returned.
// If the function returns nil, the transaction is committed.
func (st *Storage) InTxnRO(ctx context.Context, f func(ctx context.Context, txn SQLReadOnly) error) error {
	return st.inTxnRO(ctx, nil, f)
}

func (st *Storage) InTxnRW(ctx context.Context, f func(ctx context.Context, txn SQLReadWrite) error) error {
	return st.inTxnRW(ctx, nil, f)
}

// inTxn is the implementation of InTxn.
// If the provided `txn` is nil, it will create a transaction.
// If the provided `txn` is not nil, it will use that transaction, and let the
// parent take care of commiting/rolling it back.
func (st *Storage) inTxnRO(ctx context.Context, txn SQLReadOnly, f func(ctx context.Context, txn SQLReadOnly) error) error {
	if txn != nil {
		// TODO: semantics of `CleanAbortTxn` is very dubious with those
		// pseudo nested transactions.
		return f(ctx, txn)
	}

	txnCounter.With(prometheus.Labels{"readonly": "1"}).Inc()

	localTxn, err := st.roDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer localTxn.Rollback()

	err = f(ctx, sqlAdapter{localTxn})
	if errors.Is(err, ErrCleanAbortTxn) {
		return nil
	}
	if err != nil {
		return err
	}
	return localTxn.Commit()
}

func (st *Storage) inTxnRW(ctx context.Context, txn SQLReadWrite, f func(ctx context.Context, txn SQLReadWrite) error) error {
	if txn != nil {
		// TODO: semantics of `CleanAbortTxn` is very dubious with those
		// pseudo nested transactions.
		return f(ctx, txn)
	}

	txnCounter.With(prometheus.Labels{"readonly": "0"}).Inc()

	localTxn, err := st.rwDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer localTxn.Rollback()

	err = f(ctx, sqlAdapter{localTxn})
	if errors.Is(err, ErrCleanAbortTxn) {
		return nil
	}
	if err != nil {
		return err
	}
	return localTxn.Commit()
}

type ListUserEntry struct {
	UserState    *UserState
	AccountState *AccountState
}

func (st *Storage) ListUsers(ctx context.Context) (_ []*ListUserEntry, retErr error) {
	defer recordAction("list-users")(retErr)
	resp := []*ListUserEntry{}
	err := st.InTxnRO(ctx, func(ctx context.Context, txn SQLReadOnly) error {
		rows, err := txn.Query(ctx, "read-userstate", `
			SELECT
				uid
			FROM
				userstate
			;
		`)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var uid UID
			if err := rows.Scan(&uid); err != nil {
				return err
			}

			userState, err := st.UserState(ctx, txn, uid)
			if err != nil {
				return err
			}

			accountState, err := st.FirstAccountStateByUID(ctx, txn, uid)
			if err != nil {
				return err
			}

			resp = append(resp, &ListUserEntry{
				UserState:    userState,
				AccountState: accountState,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// CreateUser creates a new mastopoof user, with all the necessary bit and pieces.
func (st *Storage) CreateUser(ctx context.Context, txn SQLReadWrite, serverAddr string, accountID mastodon.ID, username string) (_ *UserState, _ *AccountState, _ *StreamState, retErr error) {
	defer recordAction("creat-euser")(retErr)
	var userState *UserState
	var accountState *AccountState
	var streamState *StreamState
	err := st.inTxnRW(ctx, txn, func(ctx context.Context, txn SQLReadWrite) error {
		// Create the local user.
		var err error
		userState, err = st.CreateUserState(ctx, txn)
		if err != nil {
			return err
		}
		// Create the mastodon account state.
		accountState, err = st.CreateAccountState(ctx, txn, userState.UID, serverAddr, accountID, username)
		if err != nil {
			return err
		}

		// Create a stream.
		streamState, err = st.CreateStreamState(ctx, txn, userState.UID)
		if err != nil {
			return err
		}
		userState.DefaultStID = streamState.StID
		return st.SetUserState(ctx, txn, userState)
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return userState, accountState, streamState, nil
}

// CreateAppRegState creates a server with the given address.
func (st *Storage) CreateAppRegState(ctx context.Context, txn SQLReadWrite, src *AppRegState) (retErr error) {
	defer recordAction("create-app-reg-state")(retErr)
	if src.Key == "" {
		// Sanity checking - if it fails, it means there is a coding error.
		return fmt.Errorf("something's quite wrong: missing key on provided app registration info for server %q", src.ServerAddr)
	}

	err := st.inTxnRW(ctx, txn, func(ctx context.Context, txn SQLReadWrite) error {
		// Do not use SetAppRegState(), as it will not fail if that already exists.
		stmt := `INSERT INTO appregstate(key, state) VALUES(?, ?)`
		_, err := txn.Exec(ctx, "insert-appregstate", stmt, src.Key, src)
		return err
	})
	if err != nil {
		return err
	}
	return nil
}

// AppRegState returns the current AppRegState for a given, well, server.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) AppRegState(ctx context.Context, txn SQLReadOnly, nfo *AppRegInfo) (retARS *AppRegState, retErr error) {
	defer recordAction("app-reg-state")(retErr)
	as := &AppRegState{}
	err := st.inTxnRO(ctx, txn, func(ctx context.Context, txn SQLReadOnly) error {
		key := nfo.Key()
		err := txn.QueryRow(ctx, "app-reg-state",
			"SELECT state FROM appregstate WHERE key=?", key).Scan(&as)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no state for server_addr=%s, key=%s: %w", nfo.ServerAddr, key, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return as, nil
}

// CreateAccountState creates a new account for the given UID and assign it an ASID.
func (st *Storage) CreateAccountState(ctx context.Context, txn SQLReadWrite, uid UID, serverAddr string, accountID mastodon.ID, username string) (_ *AccountState, retErr error) {
	defer recordAction("create-account-state")(retErr)
	var as *AccountState
	err := st.inTxnRW(ctx, txn, func(ctx context.Context, txn SQLReadWrite) error {
		var asid sql.Null[ASID]
		err := txn.QueryRow(ctx, "create-account-state-read", "SELECT MAX(asid) FROM accountstate").Scan(&asid)
		if err != nil {
			return err
		}

		as = &AccountState{
			// DB is empty, consider previous asid is zero, to get first real entry at 1.
			ASID:       asid.V + 1,
			UID:        uid,
			ServerAddr: serverAddr,
			AccountID:  accountID,
			Username:   username,
		}
		return st.SetAccountState(ctx, txn, as)
	})
	if err != nil {
		return nil, err
	}
	return as, nil
}

// FirstAccountStateByUID gets a the mastodon account of a mastopoof user identified by its UID.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) FirstAccountStateByUID(ctx context.Context, txn SQLReadOnly, uid UID) (_ *AccountState, retErr error) {
	defer recordAction("first-account-state-by-uid")(retErr)
	as := &AccountState{}
	err := st.inTxnRO(ctx, txn, func(ctx context.Context, txn SQLReadOnly) error {
		err := txn.QueryRow(ctx, "first-account-state-by-uid", "SELECT state FROM accountstate WHERE uid=?", uid).Scan(as)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no mastodon account for uid=%v: %w", uid, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return as, nil
}

// AllAccountStateByUID returns all the Mastodon accounts of that one Mastopoof user.
func (st *Storage) AllAccountStateByUID(ctx context.Context, txn SQLReadOnly, uid UID) (_ []*AccountState, retErr error) {
	defer recordAction("all-account-state-by-uid")(retErr)
	var accountStates []*AccountState
	err := st.inTxnRO(ctx, txn, func(ctx context.Context, txn SQLReadOnly) error {
		rows, err := txn.Query(ctx, "all-accountstate-by-uid", `
			SELECT
				state
			FROM accountstate
			WHERE uid=?
			ORDER BY accountstate.asid
		`, uid)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			as := &AccountState{}
			if err := rows.Scan(&as); err != nil {
				return err
			}
			accountStates = append(accountStates, as)
		}

		if len(accountStates) == 0 {
			return fmt.Errorf("no mastodon account for uid=%v: %w", uid, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return accountStates, nil
}

// AccountStateByAccountID gets a the mastodon account based on server address and account ID on that server.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) AccountStateByAccountID(ctx context.Context, txn SQLReadOnly, serverAddr string, accountID mastodon.ID) (_ *AccountState, retErr error) {
	defer recordAction("account-state-by-account-id")(retErr)
	as := &AccountState{}
	err := st.inTxnRO(ctx, txn, func(ctx context.Context, txn SQLReadOnly) error {
		err := txn.QueryRow(ctx, "account-state-by-account-id", `
			SELECT state
			FROM accountstate
			WHERE
				json_extract(state, "$.server_addr") = ?
				AND json_extract(state, "$.account_id") = ?
		`, serverAddr, accountID).Scan(&as)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no mastodon account for server=%q, account id=%v: %w", serverAddr, accountID, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return as, nil
}

func (st *Storage) SetAccountState(ctx context.Context, txn SQLReadWrite, as *AccountState) (retErr error) {
	defer recordAction("set-account-state")(retErr)
	return st.inTxnRW(ctx, txn, func(ctx context.Context, txn SQLReadWrite) error {
		// TODO: make SetAccountState support only update and verify primary key existin for ON CONFLICT.
		stmt := `INSERT INTO accountstate(asid, state, uid) VALUES(?, ?, ?) ON CONFLICT(asid) DO UPDATE SET state = excluded.state`
		_, err := txn.Exec(ctx, "set-account-state", stmt, as.ASID, as, as.UID)
		return err
	})
}

// CreateUserState creates a new account and assign it a UID.
func (st *Storage) CreateUserState(ctx context.Context, txn SQLReadWrite) (_ *UserState, retErr error) {
	defer recordAction("create-user-state")(retErr)
	userState := &UserState{}
	err := st.inTxnRW(ctx, txn, func(ctx context.Context, txn SQLReadWrite) error {
		var uid sql.Null[UID]
		// If there is no entry, a row is still returned, but with a NULL value.
		err := txn.QueryRow(ctx, "create-user-state-max", "SELECT MAX(uid) FROM userstate").Scan(&uid)
		if err != nil {
			return fmt.Errorf("unable to create new user: %w", err)
		}
		// If DB is empty, consider previous uid is zero, to get first real entry at 1.
		userState.UID = uid.V + 1
		return st.SetUserState(ctx, txn, userState)
	})
	if err != nil {
		return nil, err
	}
	return userState, nil
}

// UserState returns information about a given mastopoof user.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) UserState(ctx context.Context, txn SQLReadOnly, uid UID) (_ *UserState, retErr error) {
	defer recordAction("user-state")(retErr)
	userState := &UserState{}
	err := st.inTxnRO(ctx, txn, func(ctx context.Context, txn SQLReadOnly) error {
		err := txn.QueryRow(ctx, "user-state-uid", "SELECT state FROM userstate WHERE uid = ?", uid).Scan(&userState)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no user for uid=%v: %w", uid, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return userState, nil
}

func (st *Storage) SetUserState(ctx context.Context, txn SQLReadWrite, userState *UserState) (retErr error) {
	defer recordAction("set-users-tate")(retErr)
	return st.inTxnRW(ctx, txn, func(ctx context.Context, txn SQLReadWrite) error {
		stmt := `INSERT INTO userstate(uid, state) VALUES(?, ?) ON CONFLICT(uid) DO UPDATE SET state = excluded.state`
		_, err := txn.Exec(ctx, "set-user-state", stmt, userState.UID, userState)
		return err
	})
}

// CreateStreamState creates a new stream for the given user and return the stream ID (stid).
func (st *Storage) CreateStreamState(ctx context.Context, txn SQLReadWrite, uid UID) (_ *StreamState, retErr error) {
	defer recordAction("create-stream-state")(retErr)
	var streamState *StreamState

	err := st.inTxnRW(ctx, txn, func(ctx context.Context, txn SQLReadWrite) error {
		var stid sql.Null[StID]
		err := txn.QueryRow(ctx, "create-stream-state-max", "SELECT MAX(stid) FROM streamstate").Scan(&stid)
		if err != nil {
			return err
		}

		streamState = &StreamState{
			// Pick the largest existing (or 0) stream ID and just add one to create a new one.
			StID: stid.V + 1,
			UID:  uid,
		}
		return st.SetStreamState(ctx, txn, streamState)
	})
	if err != nil {
		return nil, err
	}
	return streamState, nil
}

func (st *Storage) StreamState(ctx context.Context, txn SQLReadOnly, stid StID) (_ *StreamState, retErr error) {
	defer recordAction("stream-state")(retErr)
	streamState := &StreamState{}
	err := st.inTxnRO(ctx, txn, func(ctx context.Context, txn SQLReadOnly) error {
		err := txn.QueryRow(ctx, "stream-state", "SELECT state FROM streamstate WHERE stid = ?", stid).Scan(streamState)
		if err == sql.ErrNoRows {
			return fmt.Errorf("stream with stid=%d not found", stid)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return streamState, nil
}

func (st *Storage) SetStreamState(ctx context.Context, txn SQLReadWrite, streamState *StreamState) (retErr error) {
	defer recordAction("set-stream-state")(retErr)
	return st.inTxnRW(ctx, txn, func(ctx context.Context, txn SQLReadWrite) error {
		stmt := `INSERT INTO streamstate(stid, state) VALUES(?, ?) ON CONFLICT(stid) DO UPDATE SET state = excluded.state`
		_, err := txn.Exec(ctx, "set-stream-state", stmt, streamState.StID, streamState)
		return err
	})
}

// RecomputeStreamState recreates what it can about StreamState from
// the state of the DB.
func (st *Storage) RecomputeStreamState(ctx context.Context, txn SQLReadOnly, stid StID) (_ *StreamState, retErr error) {
	defer recordAction("recompute-stream-state")(retErr)
	if txn == nil {
		return nil, errors.New("missing transaction")
	}

	// Load the original one, as some values are not always recomputable.
	streamState, err := st.StreamState(ctx, txn, stid)
	if err != nil {
		return nil, err
	}

	// FirstPosition
	var position sql.NullInt64
	err = txn.QueryRow(ctx, "recompute-stream-state-first", "SELECT min(position) FROM streamcontent WHERE stid = ?", stid).Scan(&position)
	if err != nil {
		return nil, err
	}
	streamState.FirstPosition = 0
	if position.Valid {
		streamState.FirstPosition = position.Int64
	}

	// LastPosition
	err = txn.QueryRow(ctx, "recompute-stream-state-last", "SELECT max(position) FROM streamcontent WHERE stid = ?", stid).Scan(&position)
	if err != nil {
		return nil, err
	}
	streamState.LastPosition = 0
	if position.Valid {
		streamState.LastPosition = position.Int64
	}

	// Remaining
	err = txn.QueryRow(ctx, "recompute-stream-state-remaining", `
		SELECT
			COUNT(*)
		FROM
			streamcontent
		WHERE
			stid = ?
			AND position IS NULL
		;
	`, stid).Scan(&streamState.Remaining)
	if err != nil {
		return nil, err
	}

	// LastRead
	if streamState.LastRead > streamState.LastPosition {
		streamState.LastRead = streamState.LastPosition
	}

	return streamState, nil
}

// FixDuplicateStatuses look for statuses which have been inserted
// twice in a given stream. It keeps only the oldest entry.
func (st *Storage) FixDuplicateStatuses(ctx context.Context, txn SQLReadWrite, stid StID) (retErr error) {
	defer recordAction("fix-duplicate-statuses")(retErr)
	if txn == nil {
		return errors.New("missing transaction")
	}

	rows, err := txn.Query(ctx, "read-fix-duplicate-statuses", `
		WITH counts AS (
			SELECT
				sid,
				MIN(position) as position,
				COUNT(*) AS count
			FROM
				streamcontent
			WHERE
				stid = ?
			GROUP BY
				sid
		)
		SELECT
			*
		FROM
			counts
		WHERE
			count > 1
		ORDER BY count
		;
	`, stid)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var sid int64
		var minPosition int64
		var count int64
		if err := rows.Scan(&sid, &minPosition, &count); err != nil {
			return err
		}
		fmt.Printf("Status sid=%d: %d dups\n", sid, count)

		result, err := txn.Exec(ctx, "fix-duplicate-statuses-delete", `
			DELETE FROM streamcontent WHERE
				stid = ?
				AND sid = ?
				AND position != ?
		`, stid, sid, minPosition)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		fmt.Printf("... deleted %d rows, kept early position %d\n", affected, minPosition)
	}

	return nil
}

// FixCrossStatuses looks for statuses coming from another user.
// It removes all of them.
func (st *Storage) FixCrossStatuses(ctx context.Context, txn SQLReadWrite, stid StID) (retErr error) {
	defer recordAction("fix-cross-statuses")(retErr)
	if txn == nil {
		return errors.New("missing transaction")
	}
	streamState, err := st.StreamState(ctx, txn, stid)
	if err != nil {
		return fmt.Errorf("unable to get streamstate from DB: %w", err)
	}
	accountState, err := st.FirstAccountStateByUID(ctx, txn, streamState.UID)
	if err != nil {
		return err
	}

	rows, err := txn.Query(ctx, "fix-cross-statuses-read", `
		SELECT
			sid
		FROM
			streamcontent
		INNER JOIN statuses
			USING (sid)
		WHERE
			streamcontent.stid = ?
			AND statuses.asid != ?
		GROUP BY
			sid
	`, stid, accountState.ASID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var sid SID
		if err := rows.Scan(&sid); err != nil {
			return err
		}
		fmt.Printf("Status sid=%d is coming from another user\n", sid)

		result, err := txn.Exec(ctx, "fix-cross-statuses-delete", `
			DELETE FROM streamcontent WHERE
				stid = ?
				AND sid = ?
		`, stid, sid)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		fmt.Printf("... deleted %d rows\n", affected)
	}
	return nil
}

func (st *Storage) ClearApp(ctx context.Context) (retErr error) {
	defer recordAction("clear-app")(retErr)
	return st.InTxnRW(ctx, func(ctx context.Context, txn SQLReadWrite) error {
		// Remove everything from the stream.
		_, err := txn.Exec(ctx, "clear-app", `DELETE FROM appregstate`)
		return err
	})
}

func (st *Storage) ClearStream(ctx context.Context, stid StID) (retErr error) {
	defer recordAction("clear-stream")(retErr)
	return st.InTxnRW(ctx, func(ctx context.Context, txn SQLReadWrite) error {
		// Remove everything from the stream.
		if _, err := txn.Exec(ctx, "clear-stream", `DELETE FROM streamcontent WHERE stid = ?`, stid); err != nil {
			return err
		}

		// Also reset last-read and other state keeping.
		streamState, err := st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}
		streamState.LastRead = 0
		streamState.FirstPosition = 0
		streamState.LastPosition = 0
		return st.SetStreamState(ctx, txn, streamState)
	})
}

func (st *Storage) ClearPoolAndStream(ctx context.Context, uid UID) (retErr error) {
	defer recordAction("clear-pool-and-stream")(retErr)
	return st.InTxnRW(ctx, func(ctx context.Context, txn SQLReadWrite) error {
		userState, err := st.UserState(ctx, txn, uid)
		if err != nil {
			return err
		}

		// Reset the fetch-from-server state.
		accountState, err := st.FirstAccountStateByUID(ctx, txn, uid)
		if err != nil {
			return err
		}
		accountState.LastHomeStatusID = ""
		if err := st.SetAccountState(ctx, txn, accountState); err != nil {
			return err
		}

		// Remove all statuses.
		if _, err := txn.Exec(ctx, "delete-statuses", `DELETE FROM statuses WHERE asid = ?`, accountState.ASID); err != nil {
			return err
		}
		// Remove everything from the stream.
		stid := userState.DefaultStID
		if _, err := txn.Exec(ctx, "delete-stream", `DELETE FROM streamcontent WHERE stid = ?`, stid); err != nil {
			return err
		}
		// Also reset last-read and other state keeping.
		streamState, err := st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}
		streamState.LastRead = 0
		streamState.FirstPosition = 0
		streamState.LastPosition = 0
		return st.SetStreamState(ctx, txn, streamState)
	})
}

type Item struct {
	// Position in the stream.
	Position    int64           `json:"position"`
	Status      mastodon.Status `json:"status"`
	StatusState StatusState     `json:"statusstate"`
}

// PickNext
// Return (nil, nil) if there is no next status.
func (st *Storage) PickNext(ctx context.Context, stid StID) (_ *Item, retErr error) {
	defer recordAction("pick-next")(retErr)
	var item *Item
	err := st.InTxnRW(ctx, func(ctx context.Context, txn SQLReadWrite) error {
		streamState, err := st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}
		item, err = st.pickNextInTxn(ctx, txn, streamState)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

// pickNextInTxn adds a new status from the pool to the stream.
// It updates streamState IN PLACE.
func (st *Storage) pickNextInTxn(ctx context.Context, txn SQLReadWrite, streamState *StreamState) (*Item, error) {
	// List all statuses which are not listed yet in "streamcontent".
	rows, err := txn.Query(ctx, "pick-next-statuses", `
		SELECT
			streamcontent.sid,
			statuses.status,
			statuses.statusstate
		FROM
			streamcontent
			JOIN statuses USING (sid)
		WHERE
			streamcontent.position IS NULL
			AND streamcontent.stid = ?
		;
	`, streamState.StID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var selectedID SID
	var selected *mastodon.Status
	var selstatustate *StatusState
	var found int64
	for rows.Next() {
		found++
		var sid SID
		var status sqlStatus
		var statusState StatusState

		if err := rows.Scan(&sid, &status, &statusState); err != nil {
			return nil, err
		}

		// Apply the rules here - is this status better than the currently selected one?
		match := false
		if selected == nil {
			match = true
			selected = &status.Status
			selstatustate = &statusState
		} else {
			// For now, just pick the oldest one.
			if status.CreatedAt.Before(selected.CreatedAt) {
				match = true
			}
		}

		if match {
			selectedID = sid
			selected = &status.Status
			selstatustate = &statusState
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if selected == nil {
		fmt.Println("No next status available")
		// Update 'remaining' while at it.
		streamState.Remaining = found
		return nil, st.SetStreamState(ctx, txn, streamState)
	}

	if selstatustate == nil {
		glog.Errorf("No status state for current status")
		selstatustate = &StatusState{}
	}

	// Now, add that status to the stream.
	// Pick current last filled position.
	position := streamState.LastPosition
	// Pick the largest existing (or 0) position and just add one to create a new one.
	position += 1

	// Update boundaries of the stream.
	streamState.LastPosition = position
	if streamState.FirstPosition == 0 {
		streamState.FirstPosition = position
	}

	// One of the status will be added to the stream, so do not count it.
	streamState.Remaining = found - 1
	if err := st.SetStreamState(ctx, txn, streamState); err != nil {
		return nil, err
	}

	// Set the position for the stream.
	stmt := `UPDATE streamcontent SET position = ? WHERE stid = ? AND sid = ?;`
	_, err = txn.Exec(ctx, "pick-next-in-txn", stmt, position, streamState.StID, selectedID)
	if err != nil {
		return nil, err
	}

	return &Item{
		Position:    position,
		Status:      *selected,
		StatusState: *selstatustate,
	}, nil
}

type ListResult struct {
	Items       []*Item
	StreamState *StreamState
}

// ListBackward get statuses before the provided position.
// refPosition must be strictly positive - i.e., refer to an actual position.
func (st *Storage) ListBackward(ctx context.Context, stid StID, refPosition int64) (_ *ListResult, retErr error) {
	defer recordAction("list-backward")(retErr)
	if refPosition < 1 {
		return nil, fmt.Errorf("invalid position %d", refPosition)
	}

	result := &ListResult{}

	err := st.InTxnRO(ctx, func(ctx context.Context, txn SQLReadOnly) error {
		streamState, err := st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}

		if streamState.FirstPosition == streamState.LastPosition {
			return fmt.Errorf("backward requests on empty stream are not allowed")
		}
		if refPosition < streamState.FirstPosition || refPosition > streamState.LastPosition {
			return fmt.Errorf("position %d does not exists", refPosition)
		}

		result.StreamState = streamState

		maxCount := 10

		// Fetch what is currently available after refPosition
		rows, err := txn.Query(ctx, "list-backward", `
			SELECT
				streamcontent.position,
				statuses.status,
				statuses.statusstate
			FROM
				statuses
				INNER JOIN streamcontent
				USING (sid)
			WHERE
				streamcontent.stid = ?
				AND streamcontent.position < ?
			ORDER BY streamcontent.position DESC
			LIMIT ?
			;
		`, stid, refPosition, maxCount)
		if err != nil {
			return err
		}
		defer rows.Close()

		// Result is descending by position, so reversed compared to what we want.
		var reverseItems []*Item
		for rows.Next() {
			var position int64
			var status sqlStatus
			var statusState StatusState
			if err := rows.Scan(&position, &status, &statusState); err != nil {
				return err
			}
			reverseItems = append(reverseItems, &Item{
				Position:    position,
				Status:      status.Status,
				StatusState: statusState,
			})
		}
		if err := rows.Err(); err != nil {
			return err
		}

		for i := len(reverseItems) - 1; i >= 0; i-- {
			result.Items = append(result.Items, reverseItems[i])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ListForward get statuses after the provided position.
// It can triage things in the stream if necessary.
// If refPosition is 0, gives data around the provided position.
func (st *Storage) ListForward(ctx context.Context, stid StID, refPosition int64, isInitial bool) (_ *ListResult, retErr error) {
	defer recordAction("list-forward")(retErr)
	if refPosition < 0 {
		return nil, fmt.Errorf("invalid position %d", refPosition)
	}

	result := &ListResult{}

	// TODO: Make it readonly when no update of stream is needed.
	err := st.InTxnRW(ctx, func(ctx context.Context, txn SQLReadWrite) error {
		streamState, err := st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}
		result.StreamState = streamState

		// There are different cases to be careful of:
		//  - Initial load, when the frontend loads up.
		//  - Requesting more stuff, while the stream was empty before.
		//  - Requesting more stuff, while the stream was not empty.

		emptyStream := streamState.FirstPosition == streamState.LastPosition
		if emptyStream && refPosition != 0 {
			return fmt.Errorf("forward requests with non-null position on empty stream are not allowed")
		}

		if isInitial {
			// Load things after the last-read status.
			refPosition = streamState.LastRead
		} else {
			if refPosition < streamState.FirstPosition || refPosition > streamState.LastPosition {
				return fmt.Errorf("position %d does not exists", refPosition)
			}
		}

		maxCount := 10

		// Fetch what is currently available after refPosition
		rows, err := txn.Query(ctx, "list-forward", `
			SELECT
				streamcontent.position,
				statuses.status,
				statuses.statusstate
			FROM
				statuses
				INNER JOIN streamcontent
				USING (sid)
			WHERE
				streamcontent.stid = ?
				AND streamcontent.position > ?
			ORDER BY streamcontent.position
			LIMIT ?
			;
		`, stid, refPosition, maxCount)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var position int64
			var status sqlStatus
			var statusState StatusState
			if err := rows.Scan(&position, &status, &statusState); err != nil {
				return err
			}
			result.Items = append(result.Items, &Item{
				Position:    position,
				Status:      status.Status,
				StatusState: statusState,
			})
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// Do we want to triage more?
		// Nothing is triaged on initial load - the idea is to try to reload similarly as
		// what it was before and keep triage on explicit request (as long as no auto-loading is
		// enabled).
		for len(result.Items) < maxCount && !isInitial {
			// If we're here, it means we've reached the end of the current stream,
			// so we need to try to inject new items.
			ost, err := st.pickNextInTxn(ctx, txn, streamState)
			if err != nil {
				return err
			}
			if ost == nil {
				// Nothing is available anymore to insert at this point.
				break
			}
			result.Items = append(result.Items, ost)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// InsertStatuses add the given statuses to the user storage.
// It updates `streamState` IN PLACE.
func (st *Storage) InsertStatuses(ctx context.Context, txn SQLReadWrite, asid ASID, streamState *StreamState, statuses []*mastodon.Status, filters []*mastodon.Filter) (retErr error) {
	defer recordAction("insert-statuses")(retErr)
	for _, status := range statuses {
		// TODO: batching

		// Insert in the statuses cache.
		stmt := `
			INSERT INTO statuses(asid, status, statusstate) VALUES(?, ?, ?);
			INSERT INTO streamcontent(stid, sid) VALUES(?, last_insert_rowid());
		`

		// TODO move filtering out of transaction
		statusState := computeState(status, filters)
		_, err := txn.Exec(ctx, "insert-statuses", stmt, asid, &sqlStatus{*status}, &statusState, streamState.StID)
		if err != nil {
			return err
		}
	}

	// Keep stats up-to-date for the stream.
	streamState.Remaining += int64(len(statuses))
	if err := st.SetStreamState(ctx, txn, streamState); err != nil {
		return err
	}
	return nil
}

// UpdateStatus replace the status in the statuses table with a new version.
// TODO: have a race detection to avoid getting back some old status (though Mastodon
// does not seem to have notion of a version)
func (st *Storage) UpdateStatus(ctx context.Context, txn SQLReadWrite, asid ASID, status *mastodon.Status, filters []*mastodon.Filter) (retErr error) {
	defer recordAction("update-status")(retErr)

	// First, find the existing status.
	// This is done separately from the UPDATE to guarantee that one and only row exists.
	rows, err := txn.Query(ctx, "update-status-find", `
			SELECT sid FROM statuses WHERE asid = ? AND status_id = ?;
	`, asid, status.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	found := false
	var sid int64
	for rows.Next() {
		if found {
			return fmt.Errorf("multiple rows found for asid=%v, id=%v", asid, status.ID)
		}
		found = true
		if err := rows.Scan(&sid); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if !found {
		return fmt.Errorf("no row found for asid=%v, id=%v", asid, status.ID)
	}

	// We've found the row, now update it.

	// TODO: make it only update the StatusState, not replace
	statusState := computeState(status, filters)

	stmt := `
		UPDATE statuses SET status = ?, statusstate = ? WHERE sid = ?;	`
	_, err = txn.Exec(ctx, "update-status", stmt, &sqlStatus{*status}, &statusState, sid)
	return err
}

// computeState calculate whether a status matches filters or not.

func computeState(status *mastodon.Status, filters []*mastodon.Filter) StatusState {
	var content string
	var tags []mastodon.Tag
	if status.Reblog != nil {
		content = strings.ToLower(status.Reblog.Content)
		tags = status.Reblog.Tags
	} else {
		content = strings.ToLower(status.Content)
		tags = status.Tags
	}

	state := StatusState{}

	// Note: we lower-case ALL THE THINGS (oh the irony) to normalize
	for _, filter := range filters {
		var phrase = strings.ToLower(filter.Phrase)
		// TODO filters are actually fancier than that. but let's try this first!
		// first we check if the phrase is, case-insensitively, in the content (if it is, we're done)
		matched := strings.Contains(content, phrase)

		// otherwise we check tags; tags are formatted in the post (to add links and whatnot), which trips the
		// stupid "let's just check for strings". if we're actually looking for a tag, we could either drop the # to
		// look through content (meh) or do things a bit more As Intended and check against the list of tags of the post
		// (that also drop the # in the tag name)
		if !matched && phrase[0] == '#' {
			for _, tag := range tags {
				// tags are stored in the post as strings without the #
				if strings.ToLower(tag.Name) == phrase[1:] {
					matched = true
					break
				}
			}
		}
		state.Filters = append(state.Filters, FilterStateMatch{string(filter.ID), matched})
	}
	return state
}

func (st *Storage) SearchByStatusID(ctx context.Context, txn SQLReadOnly, uid UID, statusID mastodon.ID) (_ []*Item, retErr error) {
	defer recordAction("search-by-status-id")(retErr)
	accountState, err := st.FirstAccountStateByUID(ctx, txn, uid)
	if err != nil {
		return nil, err
	}

	rows, err := txn.Query(ctx, "search-by-status-id", `
		SELECT
			status
		FROM
			statuses
		WHERE
			json_extract(status, "$.id") = ?
			AND asid = ?
		;
	`, statusID, accountState.ASID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*Item
	for rows.Next() {
		var status sqlStatus
		if err := rows.Scan(&status); err != nil {
			return nil, err
		}

		results = append(results, &Item{
			Position: int64(len(results)),
			Status:   status.Status,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
