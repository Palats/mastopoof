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
	DB              *sql.DB
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
		DB:     db,
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
	return prepareDB(ctx, st.DB, targetVersion)
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

type ListUserEntry struct {
	UserState    *UserState
	AccountState *AccountState
}

func (st *Storage) ListUsers(ctx context.Context) ([]*ListUserEntry, error) {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	rows, err := txn.QueryContext(ctx, `
		SELECT
			uid
		FROM
			userstate
		;
	`)
	if err != nil {
		return nil, err
	}

	resp := []*ListUserEntry{}
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}

		userState, err := st.UserState(ctx, txn, uid)
		if err != nil {
			return nil, err
		}

		accountState, err := st.AccountStateByUID(ctx, txn, uid)
		if err != nil {
			return nil, err
		}

		resp = append(resp, &ListUserEntry{
			UserState:    userState,
			AccountState: accountState,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return resp, txn.Commit()
}

// CreateUser creates a new mastopoof user, with all the necessary bit and pieces.
// Returns the UID.
func (st *Storage) CreateUser(ctx context.Context, txn SQLQueryable, serverAddr string, accountID mastodon.ID, username string) (*UserState, error) {
	if txn == nil {
		txn = st.DB
	}

	// Create the local user.
	userState, err := st.CreateUserState(ctx, txn)
	if err != nil {
		return nil, err
	}
	// Create the mastodon account state.
	_, err = st.CreateAccountState(ctx, txn, userState.UID, serverAddr, string(accountID), username)
	if err != nil {
		return nil, err
	}

	// Create a stream.
	stID, err := st.CreateStreamState(ctx, txn, userState.UID)
	if err != nil {
		return nil, err
	}
	userState.DefaultStID = stID
	if err := st.SetUserState(ctx, txn, userState); err != nil {
		return nil, err
	}

	return userState, nil
}

func (st *Storage) serverStateKey(serverAddr string) string {
	return serverAddr + "--" + st.redirectURI(serverAddr) + "--" + st.scopes
}

// CreateServerState creates a server with the given address.
func (st *Storage) CreateServerState(ctx context.Context, txn SQLQueryable, serverAddr string) (*ServerState, error) {
	if txn == nil {
		txn = st.DB
	}
	key := st.serverStateKey(serverAddr)
	ss := &ServerState{
		Key:         key,
		ServerAddr:  serverAddr,
		Scopes:      st.scopes,
		RedirectURI: st.redirectURI(serverAddr),
	}

	// Do not use SetServerState(), as it will not fail if that already exists.
	state, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}

	stmt := `INSERT INTO serverstate(key, state) VALUES(?, ?)`
	_, err = txn.ExecContext(ctx, stmt, ss.Key, state)
	if err != nil {
		return nil, err
	}
	return ss, nil
}

// ServerState returns the current ServerState for a given, well, server.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) ServerState(ctx context.Context, txn SQLQueryable, serverAddr string) (*ServerState, error) {
	if txn == nil {
		txn = st.DB
	}
	var state string
	key := st.serverStateKey(serverAddr)
	err := txn.QueryRowContext(ctx,
		"SELECT state FROM serverstate WHERE key=?", key).Scan(&state)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no state for server_addr=%s, key=%s: %w", serverAddr, key, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	as := &ServerState{}
	if err := json.Unmarshal([]byte(state), as); err != nil {
		return nil, fmt.Errorf("unable to decode serverstate state: %v", err)
	}
	return as, nil
}

func (st *Storage) SetServerState(ctx context.Context, db SQLQueryable, ss *ServerState) error {
	state, err := json.Marshal(ss)
	if err != nil {
		return err
	}

	stmt := `UPDATE serverstate SET state = ? WHERE key = ?`
	_, err = db.ExecContext(ctx, stmt, state, ss.Key)
	if err != nil {
		return err
	}
	return nil
}

// CreateAccountState creates a new account for the given UID and assign it an ASID.
func (st *Storage) CreateAccountState(ctx context.Context, db SQLQueryable, uid int64, serverAddr string, accountID string, username string) (*AccountState, error) {
	var asid sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT MAX(asid) FROM accountstate").Scan(&asid)
	if err != nil {
		return nil, err
	}

	as := &AccountState{
		// DB is empty, consider previous asid is zero, to get first real entry at 1.
		ASID:       asid.Int64 + 1,
		UID:        uid,
		ServerAddr: serverAddr,
		AccountID:  accountID,
		Username:   username,
	}
	return as, st.SetAccountState(ctx, db, as)
}

// AccountStateByUID gets a the mastodon account of a mastopoof user identified by its UID.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) AccountStateByUID(ctx context.Context, txn SQLQueryable, uid int64) (*AccountState, error) {
	if txn == nil {
		txn = st.DB
	}
	var state string
	err := txn.QueryRowContext(ctx, "SELECT state FROM accountstate WHERE uid=?", uid).Scan(&state)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no mastodon account for uid=%v: %w", uid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	as := &AccountState{}
	if err := json.Unmarshal([]byte(state), as); err != nil {
		return nil, fmt.Errorf("unable to decode accountstate state: %v", err)
	}
	return as, nil
}

// AccountStateByAccountID gets a the mastodon account based on server address and account ID on that server.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) AccountStateByAccountID(ctx context.Context, db SQLQueryable, serverAddr string, accountID string) (*AccountState, error) {
	var state string
	err := db.QueryRowContext(ctx, `
		SELECT state
		FROM accountstate
		WHERE
			json_extract(state, "$.server_addr") = ?
			AND json_extract(state, "$.account_id") = ?
	`, serverAddr, accountID).Scan(&state)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no mastodon account for server=%q, account id=%v: %w", serverAddr, accountID, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	as := &AccountState{}
	if err := json.Unmarshal([]byte(state), as); err != nil {
		return nil, fmt.Errorf("unable to decode accountstate state: %v", err)
	}
	return as, nil
}

func (st *Storage) SetAccountState(ctx context.Context, db SQLQueryable, as *AccountState) error {
	state, err := json.Marshal(as)
	if err != nil {
		return err
	}

	// TODO: make SetAccountState support only update and verify primary key existin for ON CONFLICT.
	stmt := `INSERT INTO accountstate(asid, state, uid) VALUES(?, ?, ?) ON CONFLICT(asid) DO UPDATE SET state = ?`
	_, err = db.ExecContext(ctx, stmt, as.ASID, string(state), as.UID, string(state))
	if err != nil {
		return err
	}
	return nil
}

// CreateUserState creates a new account and assign it a UID.
func (st *Storage) CreateUserState(ctx context.Context, db SQLQueryable) (*UserState, error) {
	var uid sql.NullInt64
	// If there is no entry, a row is still returned, but with a NULL value.
	err := db.QueryRowContext(ctx, "SELECT MAX(uid) FROM userstate").Scan(&uid)
	if err != nil {
		return nil, fmt.Errorf("unable to create new user: %w", err)
	}

	userState := &UserState{
		// If DB is empty, consider previous uid is zero, to get first real entry at 1.
		UID: uid.Int64 + 1,
	}
	return userState, st.SetUserState(ctx, db, userState)
}

// UserState returns information about a given mastopoof user.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) UserState(ctx context.Context, txn SQLQueryable, uid int64) (*UserState, error) {
	if txn == nil {
		txn = st.DB
	}
	var jsonString string
	err := txn.QueryRowContext(ctx, "SELECT state FROM userstate WHERE uid = ?", uid).Scan(&jsonString)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no user for uid=%v: %w", uid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	userState := &UserState{}
	if err := json.Unmarshal([]byte(jsonString), userState); err != nil {
		return nil, fmt.Errorf("unable to decode userstate state: %v", err)
	}
	return userState, nil
}

func (st *Storage) SetUserState(ctx context.Context, db SQLQueryable, userState *UserState) error {
	jsonString, err := json.Marshal(userState)
	if err != nil {
		return err
	}
	stmt := `INSERT INTO userstate(uid, state) VALUES(?, ?) ON CONFLICT(uid) DO UPDATE SET state = ?`
	_, err = db.ExecContext(ctx, stmt, userState.UID, string(jsonString), string(jsonString))
	if err != nil {
		return err
	}
	return nil
}

// CreateStreamState creates a new stream for the given user and return the stream ID (stid).
// TODO: return the stream state object.
func (st *Storage) CreateStreamState(ctx context.Context, db SQLQueryable, userID int64) (int64, error) {
	var stid int64
	err := db.QueryRowContext(ctx, "SELECT stid FROM streamstate ORDER BY stid LIMIT 1").Scan(&stid)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}

	// Pick the largest existing (or 0) stream ID and just add one to create a new one.
	stid += 1

	stream := &StreamState{
		StID: stid,
		UID:  userID,
	}

	if err := st.SetStreamState(ctx, db, stream); err != nil {
		return 0, err
	}

	return stid, nil
}

func (st *Storage) StreamState(ctx context.Context, txn SQLQueryable, stid int64) (*StreamState, error) {
	if txn == nil {
		txn = st.DB
	}
	var jsonString string
	err := txn.QueryRowContext(ctx, "SELECT state FROM streamstate WHERE stid = ?", stid).Scan(&jsonString)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("stream with stid=%d not found", stid)
	}
	if err != nil {
		return nil, err
	}

	streamState := &StreamState{}
	if err := json.Unmarshal([]byte(jsonString), streamState); err != nil {
		return nil, fmt.Errorf("unable to decode streamstate state: %v", err)
	}
	return streamState, nil
}

func (st *Storage) SetStreamState(ctx context.Context, db SQLQueryable, streamState *StreamState) error {
	jsonString, err := json.Marshal(streamState)
	if err != nil {
		return err
	}
	stmt := `INSERT INTO streamstate(stid, state) VALUES(?, ?) ON CONFLICT(stid) DO UPDATE SET state = ?`
	_, err = db.ExecContext(ctx, stmt, streamState.StID, jsonString, jsonString)
	if err != nil {
		return err
	}
	return nil
}

// RecomputeStreamState recreates what it can about StreamState from
// the state of the DB.
func (st *Storage) RecomputeStreamState(ctx context.Context, txn SQLQueryable, stid int64) (*StreamState, error) {
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
	// The `streamcontent.stid != ?` is necessary to avoid interference from other streams.
	// TODO: document the `streamcontent.stid != ?`
	err = txn.QueryRowContext(ctx, `
		SELECT
			COUNT(*)
		FROM
			statuses
			LEFT JOIN streamcontent
			USING (sid)
		WHERE
			statuses.uid = ?
			AND streamcontent.stid IS NULL
		;
	`, streamState.UID).Scan(&streamState.Remaining)
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
func (st *Storage) FixDuplicateStatuses(ctx context.Context, txn SQLQueryable, stid int64) error {
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
func (st *Storage) FixCrossStatuses(ctx context.Context, txn SQLQueryable, stid int64) error {
	streamState, err := st.StreamState(ctx, txn, stid)
	if err != nil {
		return fmt.Errorf("unable to get streamstate from DB: %w", err)
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
			AND statuses.uid != ?
		GROUP BY
			sid
	`, stid, streamState.UID)
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
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	// Remove everything from the stream.
	if _, err := txn.ExecContext(ctx, `DELETE FROM serverstate`); err != nil {
		return err
	}
	return txn.Commit()
}

func (st *Storage) ClearStream(ctx context.Context, stid int64) error {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

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
	if err := st.SetStreamState(ctx, txn, streamState); err != nil {
		return err
	}

	return txn.Commit()
}

func (st *Storage) ClearPoolAndStream(ctx context.Context, uid int64) error {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	userState, err := st.UserState(ctx, txn, uid)
	if err != nil {
		return err
	}

	// Remove all statuses.
	if _, err := txn.ExecContext(ctx, `DELETE FROM statuses WHERE uid = ?`, uid); err != nil {
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
	if err := st.SetStreamState(ctx, txn, streamState); err != nil {
		return err
	}

	return txn.Commit()
}

type Item struct {
	// Position in the stream.
	Position int64           `json:"position"`
	Status   mastodon.Status `json:"status"`
}

// PickNext
// Return (nil, nil) if there is no next status.
func (st *Storage) PickNext(ctx context.Context, stid int64) (*Item, error) {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()
	streamState, err := st.StreamState(ctx, txn, stid)
	if err != nil {
		return nil, err
	}
	o, err := st.pickNextInTxn(ctx, streamState.UID, txn, streamState)
	if err != nil {
		return nil, err
	}
	return o, txn.Commit()
}

func (st *Storage) pickNextInTxn(ctx context.Context, uid int64, txn *sql.Tx, streamState *StreamState) (*Item, error) {
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
			statuses.uid = ?
			AND streamcontent.sid IS NULL
		;
	`, uid)
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
func (st *Storage) ListBackward(ctx context.Context, stid int64, refPosition int64) (*ListResult, error) {
	if refPosition < 1 {
		return nil, fmt.Errorf("invalid position %d", refPosition)
	}

	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	result := &ListResult{}

	streamState, err := st.StreamState(ctx, txn, stid)
	if err != nil {
		return nil, err
	}

	if streamState.FirstPosition == streamState.LastPosition {
		return nil, fmt.Errorf("backward requests on empty stream are not allowed")
	}
	if refPosition < streamState.FirstPosition || refPosition > streamState.LastPosition {
		return nil, fmt.Errorf("position %d does not exists", refPosition)
	}

	result.StreamState = streamState

	maxCount := 10

	// Fetch what is currently available after refPosition
	rows, err := st.DB.QueryContext(ctx, `
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
		return nil, err
	}

	// Result is descending by position, so reversed compared to what we want.
	var reverseItems []*Item
	for rows.Next() {
		var position int64
		var jsonString string
		if err := rows.Scan(&position, &jsonString); err != nil {
			return nil, err
		}
		var status mastodon.Status
		if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
			return nil, err
		}
		reverseItems = append(reverseItems, &Item{
			Position: position,
			Status:   status,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := len(reverseItems) - 1; i >= 0; i-- {
		result.Items = append(result.Items, reverseItems[i])
	}

	return result, txn.Commit()
}

// ListForward get statuses after the provided position.
// It can triage things in the stream if necessary.
// If refPosition is 0, gives data around the provided position.
func (st *Storage) ListForward(ctx context.Context, stid int64, refPosition int64) (*ListResult, error) {
	if refPosition < 0 {
		return nil, fmt.Errorf("invalid position %d", refPosition)
	}

	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	result := &ListResult{}

	streamState, err := st.StreamState(ctx, txn, stid)
	if err != nil {
		return nil, err
	}
	result.StreamState = streamState

	if refPosition == 0 {
		// Also pick the one last read status, for context.
		refPosition = streamState.LastRead - 1
		if refPosition < streamState.FirstPosition {
			refPosition = streamState.FirstPosition
		}
	} else if streamState.FirstPosition == streamState.LastPosition {
		return nil, fmt.Errorf("forward requests with non-null position on empty stream are not allowed")
	}
	if refPosition < streamState.FirstPosition || refPosition > streamState.LastPosition {
		return nil, fmt.Errorf("position %d does not exists", refPosition)
	}

	maxCount := 10

	// Fetch what is currently available after refPosition
	rows, err := st.DB.QueryContext(ctx, `
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
		return nil, err
	}

	for rows.Next() {
		var position int64
		var jsonString string
		if err := rows.Scan(&position, &jsonString); err != nil {
			return nil, err
		}
		var status mastodon.Status
		if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, &Item{
			Position: position,
			Status:   status,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Do we want to triage more?
	for len(result.Items) < maxCount {
		// If we're here, it means we've reached the end of the current stream,
		// so we need to try to inject new items.
		ost, err := st.pickNextInTxn(ctx, stid, txn, streamState)
		if err != nil {
			return nil, err
		}
		if ost == nil {
			// Nothing is available anymore to insert at this point.
			break
		}
		result.Items = append(result.Items, ost)
	}

	return result, txn.Commit()
}

// InsertStatuses add the given statuses to the user storage.
// It does not update other info.
func (st *Storage) InsertStatuses(ctx context.Context, txn SQLQueryable, uid int64, statuses []*mastodon.Status) (*StreamState, error) {
	for _, status := range statuses {
		jsonString, err := json.Marshal(status)
		if err != nil {
			return nil, err
		}
		// TODO: batching
		stmt := `INSERT INTO statuses(uid, uri, status) VALUES(?, ?, ?)`
		_, err = txn.ExecContext(ctx, stmt, uid, status.URI, jsonString)
		if err != nil {
			return nil, err
		}
	}

	// Keep stats up-to-date for the stream.
	userState, err := st.UserState(ctx, txn, uid)
	if err != nil {
		return nil, err
	}
	streamState, err := st.StreamState(ctx, txn, userState.DefaultStID)
	if err != nil {
		return nil, err
	}
	streamState.Remaining += int64(len(statuses))
	if err := st.SetStreamState(ctx, txn, streamState); err != nil {
		return nil, err
	}
	return streamState, nil
}
