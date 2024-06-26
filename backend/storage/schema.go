package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"

	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"

	_ "embed"
)

// maxSchemaVersion indicates up to which version the database schema was configured.
// It is incremented everytime a change is made.
const maxSchemaVersion = 20

// refSchema is the database schema - see file comment for caveats.
//
//go:embed schema.sql
var refSchema string

// SID is the type of status IDs in `statuses` and `streamcontent` databases.
type SID int64

type UID int64

// UserState is the state of a Mastopoof user, stored as JSON in the DB.
type UserState struct {
	// User ID.
	UID UID `json:"uid"`

	// Default stream of that user.
	DefaultStID StID `json:"default_stid"`
}

// Scan implements the [Scanner] interface.
func (u *UserState) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for UserState json, got %T", src)
	}
	return json.Unmarshal([]byte(s), u)
}

// Value implements the [driver.Valuer] interface.
func (u *UserState) Value() (driver.Value, error) {
	data, err := json.Marshal(u)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

type ASID int64

// AccountState represents information about a mastodon account in the DB.
type AccountState struct {
	// AccountState ASID within storage. Just an arbitrary number for primary key.
	ASID ASID `json:"asid"`

	// The Mastodon server this account is part of.
	// E.g., `https://mastodon.social`
	ServerAddr string `json:"server_addr"`
	// The Mastodon account ID on the server.
	// E.g., `123456789765432132`
	AccountID mastodon.ID `json:"account_id"`
	// The Mastodon username
	// E.g., `foobar`
	Username string `json:"username"`

	AccessToken string `json:"access_token"`

	// The user using this mastodon account.
	UID UID `json:"uid"`
	// Last home status ID fetched.
	LastHomeStatusID mastodon.ID `json:"last_home_status_id"`
}

// Scan implements the [Scanner] interface.
func (a *AccountState) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for AccountState json, got %T", src)
	}
	return json.Unmarshal([]byte(s), a)
}

// Value implements the [driver.Valuer] interface.
func (a *AccountState) Value() (driver.Value, error) {
	data, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

func (accountState *AccountState) ToAccountProto() *pb.Account {
	return &pb.Account{
		ServerAddr: accountState.ServerAddr,
		AccountId:  string(accountState.AccountID),
		Username:   accountState.Username,
	}
}

// StatusState represent metadata about a status - for now only filter state
type StatusState struct {
	Filters []FilterStateMatch `json:"filters"`
}

// FilterStateMatch represents whether a filter matches a given status at the time it is fetched
type FilterStateMatch struct {
	// ID of the filter
	ID string `json:"id"`
	// Whether the filter matched the status
	Matched bool `json:"matched"`
}

// Scan implements the [Scanner] interface.
func (ss *StatusState) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for AppRegState json, got %T", src)
	}
	return json.Unmarshal([]byte(s), ss)
}

// Value implements the [driver.Valuer] interface.
func (ss *StatusState) Value() (driver.Value, error) {
	data, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

func (ss *StatusState) ToStatusStateProto() *pb.StatusState {
	var filters []*pb.FilterStateMatch
	for _, filter := range ss.Filters {
		filters = append(filters, &pb.FilterStateMatch{
			Desc:    filter.ID,
			Matched: filter.Matched,
		})
	}
	return &pb.StatusState{
		Filterstate: filters,
	}
}

// AppRegInfo is what identify a given app registration on a Mastodon server.
// It is used to identify which app registration is needed when interacting with a Mastodon server.
// It is not serialized - see AppRegState for that. AppRegState is a strict superset.
type AppRegInfo struct {
	// Mastodon server address.
	ServerAddr string `json:"server_addr"`
	// Scopes used when registering the app.
	Scopes string `json:"scopes"`
	// Where the oauth should redirect - incl. /_redirect.
	RedirectURI string `json:"redirect_uri"`
}

// Key computes a string key for that entry, for indexing.
// It is unique for a given AppRegKey content.
func (nfo *AppRegInfo) Key() string {
	return nfo.ServerAddr + "--" + nfo.RedirectURI + "--" + nfo.Scopes
}

// AppRegState contains information about an app registration on a Mastodon server.
// This state is kept in DB.
type AppRegState struct {
	// The storage key for this app registration.
	// Redundant in storage, but convenient when manipulating the data around.
	Key string `json:"key"`

	// Following fields are an AppRegKey.
	// Mastodon server address.
	ServerAddr string `json:"server_addr"`
	// Scopes used when registering the app.
	Scopes string `json:"scopes"`
	// Where the oauth should redirect - incl. /_redirect.
	RedirectURI string `json:"redirect_uri"`

	// App registration info not part of the key.
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
}

// Scan implements the [Scanner] interface.
func (a *AppRegState) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for AppRegState json, got %T", src)
	}
	return json.Unmarshal([]byte(s), a)
}

// Value implements the [driver.Valuer] interface.
func (a *AppRegState) Value() (driver.Value, error) {
	data, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

type StID int64

// StreamState is the state of a single stream, stored as JSON.
type StreamState struct {
	// Stream ID.
	StID StID `json:"stid"`
	// User ID this stream belongs to.
	UID UID `json:"uid"`
	// Position of the latest read status in this stream.
	LastRead int64 `json:"last_read"`
	// Position of the first status, if any. Usually == 1.
	FirstPosition int64 `json:"first_position"`
	// Position of the last status, if any.
	LastPosition int64 `json:"last_position"`
	// Remaining statuses in the pool which are not yet added in the stream.
	Remaining int64 `json:"remaining"`

	// Last time a fetch from mastodon finished, as unix timestamp in seconds.
	LastFetchSecs int64 `json:"last_fetch_secs"`

	NotificationsState pb.StreamInfo_NotificationsState `json:"notifications_state"`
	// Number of unread notifications
	NotificationsCount int64 `json:"notifications_count"`
}

// Scan implements the [Scanner] interface.
func (ss *StreamState) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for StreamState json, got %T", src)
	}
	return json.Unmarshal([]byte(s), ss)
}

// Value implements the [driver.Valuer] interface.
func (ss *StreamState) Value() (driver.Value, error) {
	data, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

func (ss *StreamState) ToStreamInfo() *pb.StreamInfo {
	return &pb.StreamInfo{
		Stid:               int64(ss.StID),
		LastRead:           ss.LastRead,
		FirstPosition:      ss.FirstPosition,
		LastPosition:       ss.LastPosition,
		RemainingPool:      ss.Remaining,
		LastFetchSecs:      ss.LastFetchSecs,
		NotificationState:  ss.NotificationsState,
		NotificationsCount: ss.NotificationsCount,
	}
}

// sqlStatus encapsulate a mastodon status to allow for easier SQL
// serialization, as it is not possible to add it on the original type
// on the Mastodon library.
type sqlStatus struct {
	mastodon.Status
}

// Scan implements the [Scanner] interface.
func (ss *sqlStatus) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for Status json, got %T", src)
	}
	return json.Unmarshal([]byte(s), ss)
}

// Value implements the [driver.Valuer] interface.
func (ss *sqlStatus) Value() (driver.Value, error) {
	data, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

// prepareDB creates the schema for the database or update
// it if needed.
func prepareDB(ctx context.Context, db *sql.DB, targetVersion int) error {
	if targetVersion > maxSchemaVersion {
		return fmt.Errorf("target version (%d) is higher than max known version (%d)", targetVersion, maxSchemaVersion)
	}
	// Prepare update of the database schema.
	txn, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("unable to update DB schema: %w", err)
	}
	defer txn.Rollback()

	// Get version of the storage.
	row := txn.QueryRow("PRAGMA user_version")
	if row == nil {
		return fmt.Errorf("unable to find user_version")

	}
	var version int
	if err := row.Scan(&version); err != nil {
		return fmt.Errorf("error parsing user_version: %w", err)
	}
	glog.Infof("PRAGMA user_version is %d (target=%v)", version, targetVersion)
	if version > targetVersion {
		return fmt.Errorf("user_version of DB (%v) is higher than target schema version (%v)", version, targetVersion)
	}
	if version == targetVersion {
		return nil
	}

	glog.Infof("updating database schema")

	if version < 1 && targetVersion >= 1 {
		sqlStmt := `
			CREATE TABLE IF NOT EXISTS authinfo (
				-- User ID, starts at 1
				uid INTEGER PRIMARY KEY,
				-- JSON marshalled AuthInfo
				content TEXT NOT NULL
			);
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v1: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 2 && targetVersion >= 2 {
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
			return fmt.Errorf("schema v2: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 3 && targetVersion >= 3 {
		// Do backfill of status key
		sqlStmt := `
			ALTER TABLE statuses ADD COLUMN uri TEXT
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v3: unable to run %q: %w", sqlStmt, err)
		}

		rows, err := txn.QueryContext(ctx, `SELECT sid, status FROM statuses`)
		if err != nil {
			return fmt.Errorf("schema v3: unable to query status keys: %w", err)
		}
		for rows.Next() {
			var jsonString string
			var sid int64
			if err := rows.Scan(&sid, &jsonString); err != nil {
				return fmt.Errorf("schema v3: unable to scan status: %w", err)
			}
			var status mastodon.Status
			if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
				return fmt.Errorf("schema v3: unable to unmarshal status: %w", err)
			}

			stmt := `
				UPDATE statuses SET uri = ? WHERE sid = ?;
			`
			if _, err := txn.ExecContext(ctx, stmt, status.URI, sid); err != nil {
				return fmt.Errorf("schema v3: unable to backfil URI for sid %v: %v", sid, err)
			}
		}
	}

	if version < 4 && targetVersion >= 4 {
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
			return fmt.Errorf("schema v4: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 5 && targetVersion >= 5 {
		sqlStmt := `
			ALTER TABLE listingstate RENAME TO streamstate;
			ALTER TABLE listingcontent RENAME TO streamcontent;

			ALTER TABLE streamstate RENAME COLUMN lid TO stid;
			ALTER TABLE streamcontent RENAME COLUMN lid TO stid;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v5: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 6 && targetVersion >= 6 {
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
			return fmt.Errorf("schema v6: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 7 && targetVersion >= 7 {
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
			return fmt.Errorf("schema v7: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 8 && targetVersion >= 8 {
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
			return fmt.Errorf("schema v8: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 9 && targetVersion >= 9 {
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
			return fmt.Errorf("schema v9: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 10 && targetVersion >= 10 {
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
			return fmt.Errorf("schema v10: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 11 && targetVersion >= 11 {
		// Change key for server state.
		// Just drop all existing server registration - that will force a re-login.
		sqlStmt := `
			DELETE FROM serverstate;
			ALTER TABLE serverstate DROP COLUMN server_addr;
			ALTER TABLE serverstate ADD COLUMN key TEXT;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v11: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 12 && targetVersion >= 12 {
		// Nuke session state - the update in serverstate warrants it.
		sqlStmt := `
			DELETE FROM sessions;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v12: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 13 && targetVersion >= 13 {
		// Recreate the accountstate table to:
		//  - Rename content -> state
		//  - Add "NOT NULL" on uid field
		//  - Add STRICT
		sqlStmt := `
			ALTER TABLE accountstate RENAME TO accountstateold;

			-- State of a Mastodon account.
			CREATE TABLE accountstate (
				-- Unique id.
				asid INTEGER PRIMARY KEY,
				-- Serialized JSON AccountState
				state TEXT NOT NULL,
				-- The user which owns this account.
				-- Immutable - even if another user ends up configuring that account,
				-- a new account state would be created.
				uid TEXT NOT NULL
			) STRICT;

			INSERT INTO accountstate (asid, state, uid)
			SELECT asid, CAST(content as TEXT), uid FROM accountstateold;

			DROP TABLE accountstateold;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v13: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 14 && targetVersion >= 14 {
		// Convert userstate to STRICT.
		sqlStmt := `
			ALTER TABLE userstate RENAME TO userstateold;

			-- Mastopoof user information.
			CREATE TABLE userstate (
				-- A unique idea for that user.
				uid INTEGER PRIMARY KEY,
				-- Serialized JSON UserState
				state TEXT NOT NULL
			) STRICT;

			INSERT INTO userstate (uid, state)
			SELECT uid, CAST(state as TEXT) FROM userstateold;

			DROP TABLE userstateold;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v14: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 15 && targetVersion >= 15 {
		// Convert statuses to STRICT.
		// Change uid -> asid.
		// Remove uri.

		// Verify that we're not losing statuses.
		var beforeCount int64
		err := txn.QueryRowContext(ctx, "SELECT COUNT(*) FROM statuses").Scan(&beforeCount)
		if err != nil {
			return err
		}

		sqlStmt := `
			ALTER TABLE statuses RENAME TO statusesold;

			CREATE TABLE statuses (
				-- A unique ID.
				sid INTEGER PRIMARY KEY AUTOINCREMENT,
				-- The Mastopoof account that got that status.
				asid INTEGER NOT NULL,
				-- The status, serialized as JSON.
				status TEXT NOT NULL
			) STRICT;

			INSERT INTO statuses (sid, asid, status)
				SELECT
			 		statusesold.sid,
					accountstate.asid,
					CAST(statusesold.status as TEXT)
				FROM statusesold
				JOIN accountstate USING (uid)
			;

			DROP TABLE statusesold;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v15: unable to run %q: %w", sqlStmt, err)
		}

		var afterCount int64
		err = txn.QueryRowContext(ctx, "SELECT COUNT(*) FROM statuses").Scan(&afterCount)
		if err != nil {
			return err
		}
		if beforeCount != afterCount {
			return fmt.Errorf("got %d statuses after update, %d before", afterCount, beforeCount)
		}
	}

	if version < 16 && targetVersion >= 16 {
		// Convert streamcontent to STRICT and remove NOT NULL on `position`.

		sqlStmt := `
			ALTER TABLE streamcontent RENAME TO streamcontentold;

			-- The actual content of a stream. In practice, this links position in the stream to a specific status.
			CREATE TABLE "streamcontent" (
				stid INTEGER NOT NULL,
				sid INTEGER NOT NULL,
				position INTEGER
			) STRICT;

			INSERT INTO streamcontent (stid, sid, position)
				SELECT stid, sid, position FROM streamcontentold;

			DROP TABLE streamcontentold;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v16: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 17 && targetVersion >= 17 {
		// Change stream management - directly insert in streamcontent, but without position.
		sqlStmt := `
			INSERT INTO streamcontent (stid, sid)
				SELECT
					json_extract(userstate.state, "$.default_stid"),
					statuses.sid
				FROM
					statuses
					LEFT OUTER JOIN streamcontent USING (sid)
					JOIN accountstate USING (asid)
					JOIN userstate USING (uid)
				WHERE
					streamcontent.sid IS NULL
			;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v17: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 18 && targetVersion >= 18 {
		// Convert streamstate to STRICT.
		sqlStmt := `
			ALTER TABLE streamstate RENAME TO streamstateold;

			-- Information about a stream.
			-- A stream is a series of statuses, attached to a mastopoof user.
			-- This table contains info about the stream, not the statuses
			-- themselves, nor the ordering.
			CREATE TABLE "streamstate" (
				-- Unique id for this stream.
				stid INTEGER PRIMARY KEY,
				-- Serialized StreamState JSON.
				state TEXT NOT NULL
			) STRICT;

			INSERT INTO streamstate (stid, state)
				SELECT stid, CAST(state as TEXT) FROM streamstateold;

			DROP TABLE streamstateold;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v18: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 19 && targetVersion >= 19 {
		// Convert serverstate to STRICT.
		// Rename it to appregstate
		// Make key NOT NULL.
		sqlStmt := `
			-- Info about app registration on Mastodon servers.
			CREATE TABLE appregstate (
				-- A unique key for the appregstate.
				-- Made of hash of redirect URI & scopes requested, as each of those
				-- require a different Mastodon app registration.
				key TEXT NOT NULL,
				-- Serialized AppRegState
				state TEXT NOT NULL
			) STRICT;

			INSERT INTO appregstate (key, state)
				SELECT key, CAST(state as TEXT) FROM serverstate;

			DROP TABLE serverstate;
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v19: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 20 && targetVersion >= 20 {
		// add a "statusstate" column to status with default value {}
		sqlStmt := `
			ALTER TABLE statuses ADD COLUMN statusstate TEXT NOT NULL DEFAULT "{}";
		`
		if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("schema v20: unable to run %q: %w", sqlStmt, err)
		}
	}

	// If adding anything, do not forget to increment the schema version.

	if _, err := txn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d;`, targetVersion)); err != nil {
		return fmt.Errorf("unable to set user_version: %w", err)
	}

	// And commit the change.
	if err := txn.Commit(); err != nil {
		return fmt.Errorf("unable to update DB schema: %w", err)
	}
	return nil
}
