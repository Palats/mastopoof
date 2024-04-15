package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/Palats/mastopoof/backend/mastodon"
	"github.com/golang/glog"

	_ "github.com/mattn/go-sqlite3"
)

type SQLQueryable interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
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
	db              *sql.DB
	baseRedirectURL *url.URL
	scopes          string
}

// NewStorage creates a new Mastopoof abstraction layer.
// Parameters:
//   - `db`: the storage.
//   - `selfURL`: the address under which the web UI will be known. Needed for
//     Mastodon app registration purposes.
//   - `scopes`: List of scopes that will be used, used for Mastodon App registration.
func NewStorage(db *sql.DB, selfURL string, scopes string) (*Storage, error) {
	s := &Storage{
		db:     db,
		scopes: scopes,
	}

	if selfURL != "" {
		baseRedirectURL, err := url.Parse(selfURL)
		if err != nil {
			return nil, fmt.Errorf("unable to parse self URL %q: %w", selfURL, err)
		}
		baseRedirectURL = baseRedirectURL.JoinPath("_redirect")
		glog.Infof("Using redirect URI %s", baseRedirectURL)
		s.baseRedirectURL = baseRedirectURL
	}

	return s, nil
}

func (st *Storage) Init(ctx context.Context) error {
	return st.initVersion(ctx, maxSchemaVersion)
}

func (st *Storage) initVersion(ctx context.Context, targetVersion int) error {
	return prepareDB(ctx, st.db, targetVersion)
}

func (st *Storage) redirectURI(serverAddr string) string {
	if st.baseRedirectURL == nil {
		return "urn:ietf:wg:oauth:2.0:oob"
	}
	// RedirectURI for auth must contain information about the mastodon server
	// it is about. Otherwise, when getting a code back after auth, the server
	// cannot know what it is about.
	u := *st.baseRedirectURL // Make a copy to not modify the base URL.
	q := u.Query()
	q.Set("host", serverAddr)
	u.RawQuery = q.Encode()
	return u.String()
}

// CleanAbortTxn signals that the transaction must not be committed, but that it
// is not an error.
var CleanAbortTxn = errors.New("transaction cancellation requested")

// InTxn runs the provided code in a DB transaction.
// If `f` returns an error matching `CleanAbortTxn` (using `errors.Is`), the transaction is rolledback, but InTxn return nil
// If `f` returns another non-nil error, the transaction will be aborted and the error is returned.
// If the function returns nil, the transaction is committed.
func (st *Storage) InTxn(ctx context.Context, f func(ctx context.Context, txn SQLQueryable) error) error {
	return st.inTxn(ctx, nil, f)
}

// inTxn is the implementation of InTxn.
// If the provided `txn` is nil, it will create a transaction.
// If the provided `txn` is not nil, it will use that transaction, and let the
// parent take care of commiting/rolling it back.
func (st *Storage) inTxn(ctx context.Context, txn SQLQueryable, f func(ctx context.Context, txn SQLQueryable) error) error {
	if txn != nil {
		// TODO: semantics of `CleanAbortTxn` is very dubious with those
		// pseudo nested transactions.
		return f(ctx, txn)
	}

	localTxn, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer localTxn.Rollback()

	err = f(ctx, localTxn)
	if errors.Is(err, CleanAbortTxn) {
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

func (st *Storage) ListUsers(ctx context.Context) ([]*ListUserEntry, error) {
	resp := []*ListUserEntry{}
	err := st.InTxn(ctx, func(ctx context.Context, txn SQLQueryable) error {
		rows, err := txn.QueryContext(ctx, `
			SELECT
				uid
			FROM
				userstate
			;
		`)
		if err != nil {
			return err
		}

		for rows.Next() {
			var uid UID
			if err := rows.Scan(&uid); err != nil {
				return err
			}

			userState, err := st.UserState(ctx, txn, uid)
			if err != nil {
				return err
			}

			accountState, err := st.AccountStateByUID(ctx, txn, uid)
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
func (st *Storage) CreateUser(ctx context.Context, txn SQLQueryable, serverAddr string, accountID mastodon.ID, username string) (*UserState, *AccountState, *StreamState, error) {
	var userState *UserState
	var accountState *AccountState
	var streamState *StreamState
	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		// Create the local user.
		var err error
		userState, err = st.CreateUserState(ctx, txn)
		if err != nil {
			return err
		}
		// Create the mastodon account state.
		accountState, err = st.CreateAccountState(ctx, txn, userState.UID, serverAddr, string(accountID), username)
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

func (st *Storage) serverStateKey(serverAddr string) string {
	return serverAddr + "--" + st.redirectURI(serverAddr) + "--" + st.scopes
}

// CreateServerState creates a server with the given address.
func (st *Storage) CreateServerState(ctx context.Context, txn SQLQueryable, serverAddr string) (*ServerState, error) {
	key := st.serverStateKey(serverAddr)
	ss := &ServerState{
		Key:         key,
		ServerAddr:  serverAddr,
		Scopes:      st.scopes,
		RedirectURI: st.redirectURI(serverAddr),
	}

	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		// Do not use SetServerState(), as it will not fail if that already exists.
		state, err := json.Marshal(ss)
		if err != nil {
			return err
		}
		stmt := `INSERT INTO serverstate(key, state) VALUES(?, ?)`
		_, err = txn.ExecContext(ctx, stmt, ss.Key, state)
		return err
	})
	if err != nil {
		return nil, err
	}
	return ss, nil
}

// ServerState returns the current ServerState for a given, well, server.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) ServerState(ctx context.Context, txn SQLQueryable, serverAddr string) (*ServerState, error) {
	as := &ServerState{}
	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		var state string
		key := st.serverStateKey(serverAddr)
		err := txn.QueryRowContext(ctx,
			"SELECT state FROM serverstate WHERE key=?", key).Scan(&state)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no state for server_addr=%s, key=%s: %w", serverAddr, key, ErrNotFound)
		}
		if err != nil {
			return err
		}

		if err := json.Unmarshal([]byte(state), as); err != nil {
			return fmt.Errorf("unable to decode serverstate state: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return as, nil
}

func (st *Storage) SetServerState(ctx context.Context, txn SQLQueryable, ss *ServerState) error {
	return st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		state, err := json.Marshal(ss)
		if err != nil {
			return err
		}
		stmt := `UPDATE serverstate SET state = ? WHERE key = ?`
		_, err = txn.ExecContext(ctx, stmt, state, ss.Key)
		return err
	})
}

// CreateAccountState creates a new account for the given UID and assign it an ASID.
func (st *Storage) CreateAccountState(ctx context.Context, txn SQLQueryable, uid UID, serverAddr string, accountID string, username string) (*AccountState, error) {
	var as *AccountState
	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		var asid sql.NullInt64
		err := txn.QueryRowContext(ctx, "SELECT MAX(asid) FROM accountstate").Scan(&asid)
		if err != nil {
			return err
		}

		as = &AccountState{
			// DB is empty, consider previous asid is zero, to get first real entry at 1.
			ASID:       ASID(asid.Int64) + 1,
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

// AccountStateByUID gets a the mastodon account of a mastopoof user identified by its UID.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) AccountStateByUID(ctx context.Context, txn SQLQueryable, uid UID) (*AccountState, error) {
	as := &AccountState{}
	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		var state string
		err := txn.QueryRowContext(ctx, "SELECT state FROM accountstate WHERE uid=?", uid).Scan(&state)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no mastodon account for uid=%v: %w", uid, ErrNotFound)
		}
		if err != nil {
			return err
		}

		if err := json.Unmarshal([]byte(state), as); err != nil {
			return fmt.Errorf("unable to decode accountstate state: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return as, nil
}

// AccountStateByAccountID gets a the mastodon account based on server address and account ID on that server.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) AccountStateByAccountID(ctx context.Context, txn SQLQueryable, serverAddr string, accountID string) (*AccountState, error) {
	as := &AccountState{}
	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		var state string
		err := txn.QueryRowContext(ctx, `
			SELECT state
			FROM accountstate
			WHERE
				json_extract(state, "$.server_addr") = ?
				AND json_extract(state, "$.account_id") = ?
		`, serverAddr, accountID).Scan(&state)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no mastodon account for server=%q, account id=%v: %w", serverAddr, accountID, ErrNotFound)
		}
		if err != nil {
			return err
		}

		if err := json.Unmarshal([]byte(state), as); err != nil {
			return fmt.Errorf("unable to decode accountstate state: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return as, nil
}

func (st *Storage) SetAccountState(ctx context.Context, txn SQLQueryable, as *AccountState) error {
	return st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		state, err := json.Marshal(as)
		if err != nil {
			return err
		}

		// TODO: make SetAccountState support only update and verify primary key existin for ON CONFLICT.
		stmt := `INSERT INTO accountstate(asid, state, uid) VALUES(?, ?, ?) ON CONFLICT(asid) DO UPDATE SET state = ?`
		_, err = txn.ExecContext(ctx, stmt, as.ASID, string(state), as.UID, string(state))
		return err
	})
}

// CreateUserState creates a new account and assign it a UID.
func (st *Storage) CreateUserState(ctx context.Context, txn SQLQueryable) (*UserState, error) {
	userState := &UserState{}
	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		var uid sql.NullInt64
		// If there is no entry, a row is still returned, but with a NULL value.
		err := txn.QueryRowContext(ctx, "SELECT MAX(uid) FROM userstate").Scan(&uid)
		if err != nil {
			return fmt.Errorf("unable to create new user: %w", err)
		}
		// If DB is empty, consider previous uid is zero, to get first real entry at 1.
		userState.UID = UID(uid.Int64) + 1
		return st.SetUserState(ctx, txn, userState)
	})
	if err != nil {
		return nil, err
	}
	return userState, nil
}

// UserState returns information about a given mastopoof user.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) UserState(ctx context.Context, txn SQLQueryable, uid UID) (*UserState, error) {
	userState := &UserState{}
	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		var jsonString string
		err := txn.QueryRowContext(ctx, "SELECT state FROM userstate WHERE uid = ?", uid).Scan(&jsonString)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no user for uid=%v: %w", uid, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(jsonString), userState); err != nil {
			return fmt.Errorf("unable to decode userstate state: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return userState, nil
}

func (st *Storage) SetUserState(ctx context.Context, txn SQLQueryable, userState *UserState) error {
	jsonString, err := json.Marshal(userState)
	if err != nil {
		return err
	}

	return st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		stmt := `INSERT INTO userstate(uid, state) VALUES(?, ?) ON CONFLICT(uid) DO UPDATE SET state = ?`
		_, err = txn.ExecContext(ctx, stmt, userState.UID, string(jsonString), string(jsonString))
		return err
	})
}

// CreateStreamState creates a new stream for the given user and return the stream ID (stid).
func (st *Storage) CreateStreamState(ctx context.Context, txn SQLQueryable, uid UID) (*StreamState, error) {
	var streamState *StreamState

	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		var stid sql.NullInt64
		err := txn.QueryRowContext(ctx, "SELECT MAX(stid) FROM streamstate").Scan(&stid)
		if err != nil {
			return err
		}

		streamState = &StreamState{
			// Pick the largest existing (or 0) stream ID and just add one to create a new one.
			StID: StID(stid.Int64 + 1),
			UID:  uid,
		}
		return st.SetStreamState(ctx, txn, streamState)
	})
	if err != nil {
		return nil, err
	}
	return streamState, nil
}

func (st *Storage) StreamState(ctx context.Context, txn SQLQueryable, stid StID) (*StreamState, error) {
	streamState := &StreamState{}
	err := st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		var jsonString string
		err := txn.QueryRowContext(ctx, "SELECT state FROM streamstate WHERE stid = ?", stid).Scan(&jsonString)
		if err == sql.ErrNoRows {
			return fmt.Errorf("stream with stid=%d not found", stid)
		}
		if err != nil {
			return err
		}

		if err := json.Unmarshal([]byte(jsonString), streamState); err != nil {
			return fmt.Errorf("unable to decode streamstate state: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return streamState, nil
}

func (st *Storage) SetStreamState(ctx context.Context, txn SQLQueryable, streamState *StreamState) error {
	return st.inTxn(ctx, txn, func(ctx context.Context, txn SQLQueryable) error {
		jsonString, err := json.Marshal(streamState)
		if err != nil {
			return err
		}
		stmt := `INSERT INTO streamstate(stid, state) VALUES(?, ?) ON CONFLICT(stid) DO UPDATE SET state = ?`
		_, err = txn.ExecContext(ctx, stmt, streamState.StID, jsonString, jsonString)
		return err
	})
}

// RecomputeStreamState recreates what it can about StreamState from
// the state of the DB.
func (st *Storage) RecomputeStreamState(ctx context.Context, txn SQLQueryable, stid StID) (*StreamState, error) {
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
	err = txn.QueryRowContext(ctx, "SELECT min(position) FROM streamcontent WHERE stid = ?", stid).Scan(&position)
	if err != nil {
		return nil, err
	}
	streamState.FirstPosition = 0
	if position.Valid {
		streamState.FirstPosition = position.Int64
	}

	// LastPosition
	err = txn.QueryRowContext(ctx, "SELECT max(position) FROM streamcontent WHERE stid = ?", stid).Scan(&position)
	if err != nil {
		return nil, err
	}
	streamState.LastPosition = 0
	if position.Valid {
		streamState.LastPosition = position.Int64
	}

	// Remaining
	accountState, err := st.AccountStateByUID(ctx, txn, streamState.UID)
	if err != nil {
		return nil, err
	}
	err = txn.QueryRowContext(ctx, `
		SELECT
			COUNT(*)
		FROM
			statuses
			LEFT JOIN streamcontent
			USING (sid)
		WHERE
			statuses.asid = ?
			-- That match for entries in statuses which have no corresponding stream content.
			AND streamcontent.stid IS NULL
		;
	`, accountState.ASID).Scan(&streamState.Remaining)
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
func (st *Storage) FixDuplicateStatuses(ctx context.Context, txn SQLQueryable, stid StID) error {
	if txn == nil {
		return errors.New("missing transaction")
	}

	rows, err := txn.QueryContext(ctx, `
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

	for rows.Next() {
		var sid int64
		var minPosition int64
		var count int64
		if err := rows.Scan(&sid, &minPosition, &count); err != nil {
			return err
		}
		fmt.Printf("Status sid=%d: %d dups\n", sid, count)

		result, err := txn.ExecContext(ctx, `
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
func (st *Storage) FixCrossStatuses(ctx context.Context, txn SQLQueryable, stid StID) error {
	if txn == nil {
		return errors.New("missing transaction")
	}
	streamState, err := st.StreamState(ctx, txn, stid)
	if err != nil {
		return fmt.Errorf("unable to get streamstate from DB: %w", err)
	}
	accountState, err := st.AccountStateByUID(ctx, txn, streamState.UID)
	if err != nil {
		return err
	}

	rows, err := txn.QueryContext(ctx, `
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

	for rows.Next() {
		var sid int64
		if err := rows.Scan(&sid); err != nil {
			return err
		}
		fmt.Printf("Status sid=%d is coming from another user\n", sid)

		result, err := txn.ExecContext(ctx, `
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

func (st *Storage) ClearApp(ctx context.Context) error {
	return st.InTxn(ctx, func(ctx context.Context, txn SQLQueryable) error {
		// Remove everything from the stream.
		_, err := txn.ExecContext(ctx, `DELETE FROM serverstate`)
		return err
	})
}

func (st *Storage) ClearStream(ctx context.Context, stid StID) error {
	return st.InTxn(ctx, func(ctx context.Context, txn SQLQueryable) error {
		// Remove everything from the stream.
		if _, err := txn.ExecContext(ctx, `DELETE FROM streamcontent WHERE stid = ?`, stid); err != nil {
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

func (st *Storage) ClearPoolAndStream(ctx context.Context, uid UID) error {
	return st.InTxn(ctx, func(ctx context.Context, txn SQLQueryable) error {
		userState, err := st.UserState(ctx, txn, uid)
		if err != nil {
			return err
		}

		// Reset the fetch-from-server state.
		accountState, err := st.AccountStateByUID(ctx, txn, uid)
		if err != nil {
			return err
		}
		accountState.LastHomeStatusID = ""
		if err := st.SetAccountState(ctx, txn, accountState); err != nil {
			return err
		}

		// Remove all statuses.
		if _, err := txn.ExecContext(ctx, `DELETE FROM statuses WHERE asid = ?`, accountState.ASID); err != nil {
			return err
		}
		// Remove everything from the stream.
		stid := userState.DefaultStID
		if _, err := txn.ExecContext(ctx, `DELETE FROM streamcontent WHERE stid = ?`, stid); err != nil {
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
	Position int64           `json:"position"`
	Status   mastodon.Status `json:"status"`
}

// PickNext
// Return (nil, nil) if there is no next status.
func (st *Storage) PickNext(ctx context.Context, stid StID) (*Item, error) {
	var item *Item
	err := st.InTxn(ctx, func(ctx context.Context, txn SQLQueryable) error {
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
func (st *Storage) pickNextInTxn(ctx context.Context, txn SQLQueryable, streamState *StreamState) (*Item, error) {
	// TODO: remove that, as it looks it up for every pick. Should disappear when
	// moving to inserting into the stream immediately on fetch.
	accountState, err := st.AccountStateByUID(ctx, txn, streamState.UID)
	if err != nil {
		return nil, err
	}

	// List all statuses which are not listed yet in "streamcontent".
	rows, err := txn.QueryContext(ctx, `
		SELECT
			statuses.sid,
			statuses.status
		FROM
			statuses
			LEFT OUTER JOIN streamcontent
			USING (sid)
		WHERE
			statuses.asid = ?
			AND streamcontent.sid IS NULL
		;
	`, accountState.ASID)
	if err != nil {
		return nil, err
	}

	var selectedID int64
	var selected *mastodon.Status
	var found int64
	for rows.Next() {
		found++
		var sid int64
		var jsonString string
		if err := rows.Scan(&sid, &jsonString); err != nil {
			return nil, err
		}
		var status mastodon.Status
		if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
			return nil, err
		}

		// Apply the rules here - is this status better than the currently selected one?
		match := false
		if selected == nil {
			match = true
			selected = &status
		} else {
			// For now, just pick the oldest one.
			if status.CreatedAt.Before(selected.CreatedAt) {
				match = true
			}
		}

		if match {
			selectedID = sid
			selected = &status
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

	// Insert the newly selected status in the stream.
	stmt := `INSERT INTO streamcontent(stid, sid, position) VALUES(?, ?, ?);`
	_, err = txn.ExecContext(ctx, stmt, streamState.StID, selectedID, position)
	if err != nil {
		return nil, err
	}

	return &Item{
		Position: position,
		Status:   *selected,
	}, nil
}

type ListResult struct {
	Items       []*Item
	StreamState *StreamState
}

// ListBackward get statuses before the provided position.
// refPosition must be strictly positive - i.e., refer to an actual position.
func (st *Storage) ListBackward(ctx context.Context, stid StID, refPosition int64) (*ListResult, error) {
	if refPosition < 1 {
		return nil, fmt.Errorf("invalid position %d", refPosition)
	}

	result := &ListResult{}

	err := st.InTxn(ctx, func(ctx context.Context, txn SQLQueryable) error {
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
		rows, err := txn.QueryContext(ctx, `
			SELECT
				streamcontent.position,
				statuses.status
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

		// Result is descending by position, so reversed compared to what we want.
		var reverseItems []*Item
		for rows.Next() {
			var position int64
			var jsonString string
			if err := rows.Scan(&position, &jsonString); err != nil {
				return err
			}
			var status mastodon.Status
			if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
				return err
			}
			reverseItems = append(reverseItems, &Item{
				Position: position,
				Status:   status,
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
func (st *Storage) ListForward(ctx context.Context, stid StID, refPosition int64) (*ListResult, error) {
	if refPosition < 0 {
		return nil, fmt.Errorf("invalid position %d", refPosition)
	}

	result := &ListResult{}

	err := st.InTxn(ctx, func(ctx context.Context, txn SQLQueryable) error {
		streamState, err := st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}
		result.StreamState = streamState

		if refPosition == 0 {
			// Also pick the one last read status, for context.
			refPosition = streamState.LastRead - 1
			if refPosition < streamState.FirstPosition {
				refPosition = streamState.FirstPosition
			}
		} else if streamState.FirstPosition == streamState.LastPosition {
			return fmt.Errorf("forward requests with non-null position on empty stream are not allowed")
		}
		if refPosition < streamState.FirstPosition || refPosition > streamState.LastPosition {
			return fmt.Errorf("position %d does not exists", refPosition)
		}

		maxCount := 10

		// Fetch what is currently available after refPosition
		rows, err := txn.QueryContext(ctx, `
			SELECT
				streamcontent.position,
				statuses.status
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

		for rows.Next() {
			var position int64
			var jsonString string
			if err := rows.Scan(&position, &jsonString); err != nil {
				return err
			}
			var status mastodon.Status
			if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
				return err
			}
			result.Items = append(result.Items, &Item{
				Position: position,
				Status:   status,
			})
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// Do we want to triage more?
		for len(result.Items) < maxCount {
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
func (st *Storage) InsertStatuses(ctx context.Context, txn SQLQueryable, asid ASID, streamState *StreamState, statuses []*mastodon.Status) error {
	for _, status := range statuses {
		jsonBytes, err := json.Marshal(status)
		if err != nil {
			return err
		}
		// TODO: batching
		stmt := `INSERT INTO statuses(asid, status) VALUES(?, ?)`
		_, err = txn.ExecContext(ctx, stmt, asid, string(jsonBytes))
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
