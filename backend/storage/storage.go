package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"

	_ "github.com/mattn/go-sqlite3"
)

// ServerState contains information about a Mastodon server - most notably, its app registration.
type ServerState struct {
	ServerAddr string `json:"server_addr"`

	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
	RedirectURI  string `json:"redirect_uri"`
}

// AccountState represents information about a mastodon account in the DB.
type AccountState struct {
	// AccountState ASID within storage. Just an arbitrary number for primary key.
	ASID int64 `json:"asid"`

	// The Mastodon server this account is part of.
	ServerAddr string `json:"server_addr"`
	// The Mastodon account ID on the server.
	AccountID string `json:"account_id"`
	// The Mastodon username
	Username string `json:"username"`

	AccessToken string `json:"access_token"`

	// The user using this mastodon account.
	UID int64 `json:"uid"`
	// Last home status ID fetched.
	LastHomeStatusID mastodon.ID `json:"last_home_status_id"`
}

// UserState is the state of a Mastopoof user, stored as JSON in the DB.
type UserState struct {
	// User ID.
	UID int64 `json:"uid"`

	// Default stream of that user.
	DefaultStID int64 `json:"default_stid"`
}

// StreamState is the state of a single stream, stored as JSON.
type StreamState struct {
	// Stream ID.
	StID int64 `json:"stid"`
	// User ID this stream belongs to.
	UID int64 `json:"uid"`
	// Position of the latest read status in this stream.
	LastRead int64 `json:"last_read"`
	// Position of the first status, if any. Usually == 1.
	FirstPosition int64 `json:"first_position"`
	// Position of the last status, if any.
	LastPosition int64 `json:"last_position"`
	// Remaining statuses in the pool which are not yet added in the stream.
	Remaining int64 `json:"remaining"`
}

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
	if len(id1) > len(id2) {
		return true
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
	DB *sql.DB
}

func NewStorage(db *sql.DB) *Storage {
	return &Storage{DB: db}
}

const schemaVersion = 10

func (st *Storage) Init(ctx context.Context) error {
	// Get version of the storage.
	row := st.DB.QueryRow("PRAGMA user_version")
	if row == nil {
		return fmt.Errorf("unable to find user_version")

	}
	var version int
	if err := row.Scan(&version); err != nil {
		return fmt.Errorf("error parsing user_version: %w", err)
	}
	glog.Infof("PRAGMA user_version is %d (target=%v)", version, schemaVersion)
	if version > schemaVersion {
		return fmt.Errorf("user_version of DB (%v) is higher than supported schema version (%v)", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}

	glog.Infof("updating database schema")

	// Prepare update of the database schema.
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("unable to update DB schema: %w", err)
	}
	defer txn.Rollback()

	if version < 1 {
		sqlStmt := `
			CREATE TABLE IF NOT EXISTS authinfo (
				-- User ID, starts at 1
				uid INTEGER PRIMARY KEY,
				-- JSON marshalled AuthInfo
				content TEXT NOT NULL
			);
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}
	}
	if version < 2 {
		sqlStmt := `
			CREATE TABLE IF NOT EXISTS userstate (
				-- User ID
				uid INTEGER PRIMARY KEY,
				-- JSON marshalled UserState
				state TEXT NOT NULL
			);

			CREATE TABLE IF NOT EXISTS statuses (
				-- Arbitrary integer
				sid INTEGER PRIMARY KEY AUTOINCREMENT,
				-- The account is belongs to
				uid INTEGER NOT NULL,
				-- The full status obtained from the server, as json.
				status TEXT NOT NULL
			);
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 3 {
		// Do backfill of status key
		sqlStmt := `
			ALTER TABLE statuses ADD COLUMN uri TEXT
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}

		rows, err := st.DB.QueryContext(ctx, `SELECT sid, status FROM statuses`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var jsonString string
			var sid int64
			if err := rows.Scan(&sid, &jsonString); err != nil {
				return err
			}
			var status mastodon.Status
			if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
				return err
			}

			stmt := `
				UPDATE statuses SET uri = ? WHERE sid = ?;
			`
			if _, err := txn.ExecContext(ctx, stmt, status.URI, sid); err != nil {
				return fmt.Errorf("unable to backfil URI for sid %v: %v", sid, err)
			}
		}
	}

	if version < 4 {
		sqlStmt := `
			CREATE TABLE listingstate (
				-- Listing ID. Starts at 1.
				lid INTEGER PRIMARY KEY,
				-- JSON marshalled ListingState
				state TEXT NOT NULL
			);

			CREATE TABLE listingcontent (
				-- Listing ID
				lid INTEGER NOT NULL,
				-- Status ID
				sid INTEGER NOT NULL,
				-- Position of the status in the listing.
				position INTEGER NOT NULL
			);
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 5 {
		sqlStmt := `
			ALTER TABLE listingstate RENAME TO streamstate;
			ALTER TABLE listingcontent RENAME TO streamcontent;

			ALTER TABLE streamstate RENAME COLUMN lid TO stid;
			ALTER TABLE streamcontent RENAME COLUMN lid TO stid;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 6 {
		// Rename field 'lid' in JSON to 'stid'.
		sqlStmt := `
			UPDATE streamstate SET state = json_set(
				streamstate.state,
				"$.stid",
				json_extract(streamstate.state, "$.lid")
			);
			UPDATE streamstate SET state = json_remove(
				streamstate.state,
				"$.lid"
			);
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 7 {
		// Rename 'authinfo' to 'accountstate'.
		// Change key of accountstate to be an arbitrary key and backfill it.
		sqlStmt := `
			ALTER TABLE authinfo RENAME TO accountstate;
			ALTER TABLE accountstate RENAME COLUMN uid TO asid;
			ALTER TABLE accountstate ADD COLUMN uid TEXT;

			UPDATE accountstate SET uid = 1;

			UPDATE accountstate SET content = json_set(
				accountstate.content,
				"$.asid",
				json_extract(accountstate.content, "$.uid")
			);
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 8 {
		// Move last_home_status_id from userstate to accountstate;
		sqlStmt := `
			UPDATE accountstate SET content = json_set(
				accountstate.content,
				"$.last_home_status_id",
				(SELECT json_extract(userstate.state, "$.last_home_status_id") FROM userstate WHERE userstate.uid = accountstate.uid)
			);

			UPDATE userstate SET state = json_remove(
				userstate.state,
				"$.last_home_status_id"
			);
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 9 {
		// Split server info.
		//  Add  serverstate (server_addr, {server_addr, client_id, client_secret, auth_uri, redirect_uri})
		//  Delete accountstate  {client_id, client_secret, auth_uri, redirect_uri}
		sqlStmt := `
			CREATE TABLE serverstate (
				-- server address
				server_addr STRING NOT NULL,
				-- JSON marshalled ServerState
				state TEXT NOT NULL
			);

			INSERT INTO serverstate (server_addr, state)
				SELECT
					json_extract(accountstate.content, "$.server_addr"),
					json_object(
						"server_addr", json_extract(accountstate.content, "$.server_addr"),
						"client_id", json_extract(accountstate.content, "$.client_id"),
						"client_secret", json_extract(accountstate.content, "$.client_secret"),
						"auth_uri", json_extract(accountstate.content, "$.auth_uri"),
						"redirect_uri", json_extract(accountstate.content, "$.redirect_uri")
					)
				FROM
					accountstate;

			UPDATE accountstate SET content = json_remove(
				accountstate.content,
				"$.client_id",
				"$.client_secret",
				"$.auth_uri",
				"$.redirect_uri"
			);
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 10 {
		// Add session persistence
		sqlStmt := `
			CREATE TABLE sessions (
				token TEXT PRIMARY KEY,
				data BLOB NOT NULL,
				expiry REAL NOT NULL
			);

			CREATE INDEX sessions_expiry_idx ON sessions(expiry);
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
		}
	}

	if _, err := txn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d;`, schemaVersion)); err != nil {
		return fmt.Errorf("unable to set user_version: %w", err)
	}

	// And commit the change.
	if err := txn.Commit(); err != nil {
		return fmt.Errorf("unable to update DB schema: %w", err)
	}
	return nil
}

type ListUserEntry struct {
	UserState    *UserState
	AccountState *AccountState
}

func (st *Storage) ListUsers(ctx context.Context, db SQLQueryable) ([]*ListUserEntry, error) {
	rows, err := db.QueryContext(ctx, `
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

		userState, err := st.UserState(ctx, db, uid)
		if err != nil {
			return nil, err
		}

		accountState, err := st.AccountStateByUID(ctx, db, uid)
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
	return resp, nil
}

// CreateServerState creates a server with the given address.
func (st *Storage) CreateServerState(ctx context.Context, db SQLQueryable, serverAddr string, redirectURI string) (*ServerState, error) {
	ss := &ServerState{
		ServerAddr:  serverAddr,
		RedirectURI: redirectURI,
	}

	// Do not use SetServerState(), as it will not fail if that already exists.
	state, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}

	stmt := `INSERT INTO serverstate(server_addr, state) VALUES(?, ?)`
	_, err = db.ExecContext(ctx, stmt, ss.ServerAddr, state)
	if err != nil {
		return nil, err
	}
	return ss, nil
}

// ServerState returns the current ServerState for a given, well, server.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) ServerState(ctx context.Context, db SQLQueryable, serverAddr string, redirectURI string) (*ServerState, error) {
	var state string
	err := db.QueryRowContext(ctx,
		"SELECT state FROM serverstate WHERE server_addr=? AND json_extract(state, '$.redirect_uri')=?",
		serverAddr, redirectURI).Scan(&state)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no state for server_addr=%s, redirect_uri=%s: %w", serverAddr, redirectURI, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	as := &ServerState{}
	if err := json.Unmarshal([]byte(state), as); err != nil {
		return nil, fmt.Errorf("unable to decode serverstate content: %v", err)
	}
	return as, nil
}

func (st *Storage) SetServerState(ctx context.Context, db SQLQueryable, ss *ServerState) error {
	state, err := json.Marshal(ss)
	if err != nil {
		return err
	}

	// TODO: Fix schema to have a primary key / unique on `server_addr` maybe.
	stmt := `UPDATE serverstate SET state = ? WHERE server_addr = ? AND json_extract(state, '$.redirect_uri')=?`
	_, err = db.ExecContext(ctx, stmt, state, ss.ServerAddr, ss.RedirectURI)
	if err != nil {
		return err
	}
	return nil
}

// CreateAccountState creates a new account for the given UID and assign it an ASID.
func (st *Storage) CreateAccountState(ctx context.Context, db SQLQueryable, uid int64, serverAddr string, accountID string, username string) (*AccountState, error) {
	var asid int64
	err := db.QueryRowContext(ctx, "SELECT MAX(asid) FROM accountstate").Scan(&asid)
	if err == sql.ErrNoRows {
		// DB is empty, consider previous asid is zero, to get first real entry at 1.
		asid = 0
	} else if err != nil {
		return nil, err
	}

	as := &AccountState{
		ASID:       asid + 1,
		UID:        uid,
		ServerAddr: serverAddr,
		AccountID:  accountID,
		Username:   username,
	}
	return as, st.SetAccountState(ctx, db, as)
}

// AccountStateByUID gets a the mastodon account of a mastopoof user identified by its UID.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) AccountStateByUID(ctx context.Context, db SQLQueryable, uid int64) (*AccountState, error) {
	var content string
	err := db.QueryRowContext(ctx, "SELECT content FROM accountstate WHERE uid=?", uid).Scan(&content)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no mastodon account for uid=%v: %w", uid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	as := &AccountState{}
	if err := json.Unmarshal([]byte(content), as); err != nil {
		return nil, fmt.Errorf("unable to decode accountstate content: %v", err)
	}
	return as, nil
}

// AccountStateByAccountID gets a the mastodon account based on server address and account ID on that server.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) AccountStateByAccountID(ctx context.Context, db SQLQueryable, serverAddr string, accountID string) (*AccountState, error) {
	var content string
	err := db.QueryRowContext(ctx, `
		SELECT content
		FROM accountstate
		WHERE
			json_extract(content, "$.server_addr") = ?
			AND json_extract(content, "$.account_id") = ?
	`, serverAddr, accountID).Scan(&content)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no mastodon account for server=%q, account id=%v: %w", serverAddr, accountID, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	as := &AccountState{}
	if err := json.Unmarshal([]byte(content), as); err != nil {
		return nil, fmt.Errorf("unable to decode accountstate content: %v", err)
	}
	return as, nil
}

func (st *Storage) SetAccountState(ctx context.Context, db SQLQueryable, as *AccountState) error {
	content, err := json.Marshal(as)
	if err != nil {
		return err
	}

	// TODO: make SetAccountState support only update and verify primary key existin for ON CONFLICT.
	stmt := `INSERT INTO accountstate(asid, content) VALUES(?, ?) ON CONFLICT(asid) DO UPDATE SET content = ?`
	_, err = db.ExecContext(ctx, stmt, as.ASID, content, content)
	if err != nil {
		return err
	}
	return nil
}

// CreateUserState creates a new account and assign it a UID.
func (st *Storage) CreateUserState(ctx context.Context, db SQLQueryable) (*UserState, error) {
	var uid int64
	err := db.QueryRowContext(ctx, "SELECT MAX(uid) FROM userstate").Scan(&uid)
	if err == sql.ErrNoRows {
		// DB is empty, consider previous uid is zero, to get first real entry at 1.
		uid = 0
	} else if err != nil {
		return nil, err
	}

	userState := &UserState{
		UID: uid + 1,
	}
	return userState, st.SetUserState(ctx, db, userState)
}

// UserState returns information about a given mastopoof user.
// Returns wrapped ErrNotFound if no entry exists.
func (st *Storage) UserState(ctx context.Context, db SQLQueryable, uid int64) (*UserState, error) {
	var jsonString string
	err := db.QueryRowContext(ctx, "SELECT state FROM userstate WHERE uid = ?", uid).Scan(&jsonString)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no user for uid=%v: %w", uid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	userState := &UserState{}
	if err := json.Unmarshal([]byte(jsonString), userState); err != nil {
		return nil, fmt.Errorf("unable to decode userstate content: %v", err)
	}
	return userState, nil
}

func (st *Storage) SetUserState(ctx context.Context, db SQLQueryable, userState *UserState) error {
	jsonString, err := json.Marshal(userState)
	if err != nil {
		return err
	}
	stmt := `INSERT INTO userstate(uid, state) VALUES(?, ?) ON CONFLICT(uid) DO UPDATE SET state = ?`
	_, err = db.ExecContext(ctx, stmt, userState.UID, jsonString, jsonString)
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

func (st *Storage) StreamState(ctx context.Context, db SQLQueryable, stid int64) (*StreamState, error) {
	var jsonString string
	err := db.QueryRowContext(ctx, "SELECT state FROM streamstate WHERE stid = ?", stid).Scan(&jsonString)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("stream with stid=%d not found", stid)
	}
	if err != nil {
		return nil, err
	}

	streamState := &StreamState{}
	if err := json.Unmarshal([]byte(jsonString), streamState); err != nil {
		return nil, fmt.Errorf("unable to decode streamstate content: %v", err)
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

type Item struct {
	// Position in the stream.
	Position int64           `json:"position"`
	Status   mastodon.Status `json:"status"`
}

// Opened returns the currently open statuses in the stream.
func (st *Storage) Opened(ctx context.Context, stid int64) ([]*Item, error) {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	streamState, err := st.StreamState(ctx, txn, stid)
	if err != nil {
		return nil, err
	}

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
		;
	`, stid, streamState.LastRead)
	if err != nil {
		return nil, err
	}

	results := []*Item{}

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

		results = append(results, &Item{
			Position: position,
			Status:   status,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, txn.Commit()
}

// StatusAt gets the status at the provided position in the stream.
// Returns nil, nil if there is no matching status at that position.
func (st *Storage) StatusAt(ctx context.Context, stid int64, position int64) (*Item, error) {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	var jsonString string

	row := txn.QueryRowContext(ctx, `
		SELECT
			statuses.status
		FROM
			statuses
			INNER JOIN streamcontent
			USING (sid)
		WHERE
			streamcontent.stid = ?
			AND streamcontent.position = ?
		;
	`, stid, position)

	err = row.Scan(&jsonString)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var status mastodon.Status
	if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
		return nil, err
	}

	return &Item{
		Position: position,
		Status:   status,
	}, txn.Commit()
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
	o, err := st.pickNextInTxn(ctx, stid, txn, streamState)
	if err != nil {
		return nil, err
	}
	return o, txn.Commit()
}

func (st *Storage) pickNextInTxn(ctx context.Context, stid int64, txn *sql.Tx, streamState *StreamState) (*Item, error) {
	// List all statuses which are not listed yet in "streamcontent" for that stream ID.
	rows, err := txn.QueryContext(ctx, `
		SELECT
			statuses.sid,
			statuses.status
		FROM
			statuses
			LEFT OUTER JOIN streamcontent
			USING (sid)
		WHERE
			(streamcontent.stid IS NULL OR streamcontent.stid != ?)
		;
	`, stid)
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
	_, err = txn.ExecContext(ctx, stmt, stid, selectedID, position)
	if err != nil {
		return nil, err
	}

	return &Item{
		Position: position,
		Status:   *selected,
	}, nil
}

type ListResult struct {
	HasFirst bool
	HasLast  bool
	Items    []*Item

	StreamState *StreamState
}

// ListBackward get statuses before the provided position.
// It can insert things in the stream if necessary.
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

	if len(result.Items) > 0 {
		result.HasFirst = streamState.FirstPosition == result.Items[0].Position
	}
	return result, txn.Commit()
}

// ListForward get statuses after the provided position.
// It can insert things in the stream if necessary.
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
		if refPosition < 0 {
			refPosition = 0
		}
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

	// Do we want to fetch more?
	for len(result.Items) < maxCount {
		// If we're here, it means we've reached the end of the current stream,
		// so we need to try to inject new items.
		ost, err := st.pickNextInTxn(ctx, stid, txn, streamState)
		if err != nil {
			return nil, err
		}
		if ost == nil {
			// Nothing is available anymore to insert at this point.
			result.HasLast = true
			break
		}
		result.Items = append(result.Items, ost)
	}

	if len(result.Items) > 0 {
		result.HasFirst = streamState.FirstPosition == result.Items[0].Position
	}

	return result, txn.Commit()
}
