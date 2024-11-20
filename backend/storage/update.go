// Package storage manages Mastopoof persistence.
// This file is deals with creating/updating existing databases.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"

	_ "embed"
)

// refSchema is the database schema - see file comment for caveats.
//
//go:embed schema.sql
var refSchema string

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

	for i := version; i < targetVersion; i++ {
		err := updateFunctions[i](ctx, txn)
		if err != nil {
			return fmt.Errorf("unable to update from version %d to version %d: %w", version, version+1, err)
		}
	}

	if _, err := txn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d;`, targetVersion)); err != nil {
		return fmt.Errorf("unable to set user_version: %w", err)
	}

	// And commit the change.
	if err := txn.Commit(); err != nil {
		return fmt.Errorf("unable to update DB schema: %w", err)
	}

	// Some update can create a lot of old table, recover space.
	if _, err := db.ExecContext(ctx, `VACUUM;`); err != nil {
		return fmt.Errorf("unable to vacuum db: %w", err)
	}
	return nil
}

type updateFunc func(context.Context, txnInterface) error

// If adding anything, do not forget to increment the schema version.
var updateFunctions = []updateFunc{
	v0Tov1,
	v1Tov2,
	v2Tov3,
	v3Tov4,
	v4Tov5,
	v5Tov6,
	v6Tov7,
	v7Tov8,
	v8Tov9,
	v9Tov10,
	v10Tov11,
	v11Tov12,
	v12Tov13,
	v13Tov14,
	v14Tov15,
	v15Tov16,
	v16Tov17,
	v17Tov18,
	v18Tov19,
	v19Tov20,
	v20Tov21,
	v21Tov22,
}

// maxSchemaVersion indicates up to which version the database schema was configured.
// It is incremented everytime a change is made.
const maxSchemaVersion = 22

func init() {
	if len(updateFunctions) != maxSchemaVersion {
		panic(fmt.Sprintf("Got %d update functions for schema version %d", len(updateFunctions), maxSchemaVersion))
	}
}

func v0Tov1(ctx context.Context, txn txnInterface) error {
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
	return nil
}

func v1Tov2(ctx context.Context, txn txnInterface) error {
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
	return nil
}

func v2Tov3(ctx context.Context, txn txnInterface) error {
	// Do backfill of status key
	sqlStmt := `
			ALTER TABLE statuses ADD COLUMN uri TEXT
		`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}

	rows, err := txn.QueryContext(ctx, `SELECT sid, status FROM statuses`)
	if err != nil {
		return fmt.Errorf("unable to query status keys: %w", err)
	}
	for rows.Next() {
		var jsonString string
		var sid int64
		if err := rows.Scan(&sid, &jsonString); err != nil {
			return fmt.Errorf("unable to scan status: %w", err)
		}
		var status mastodon.Status
		if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
			return fmt.Errorf("unable to unmarshal status: %w", err)
		}

		stmt := `
				UPDATE statuses SET uri = ? WHERE sid = ?;
			`
		if _, err := txn.ExecContext(ctx, stmt, status.URI, sid); err != nil {
			return fmt.Errorf("unable to backfil URI for sid %v: %v", sid, err)
		}
	}
	return nil
}

func v3Tov4(ctx context.Context, txn txnInterface) error {
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
	return nil
}

func v4Tov5(ctx context.Context, txn txnInterface) error {
	sqlStmt := `
			ALTER TABLE listingstate RENAME TO streamstate;
			ALTER TABLE listingcontent RENAME TO streamcontent;

			ALTER TABLE streamstate RENAME COLUMN lid TO stid;
			ALTER TABLE streamcontent RENAME COLUMN lid TO stid;
		`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v5Tov6(ctx context.Context, txn txnInterface) error {
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
	return nil
}

func v6Tov7(ctx context.Context, txn txnInterface) error {
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
	return nil
}

func v7Tov8(ctx context.Context, txn txnInterface) error {
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
	return nil
}

func v8Tov9(ctx context.Context, txn txnInterface) error {
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
	return nil
}

func v9Tov10(ctx context.Context, txn txnInterface) error {
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
	return nil
}

func v10Tov11(ctx context.Context, txn txnInterface) error {
	// Change key for server state.
	// Just drop all existing server registration - that will force a re-login.
	sqlStmt := `
			DELETE FROM serverstate;
			ALTER TABLE serverstate DROP COLUMN server_addr;
			ALTER TABLE serverstate ADD COLUMN key TEXT;
		`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v11Tov12(ctx context.Context, txn txnInterface) error {
	// Nuke session state - the update in serverstate warrants it.
	sqlStmt := `
			DELETE FROM sessions;
		`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v12Tov13(ctx context.Context, txn txnInterface) error {
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
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v13Tov14(ctx context.Context, txn txnInterface) error {
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
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v14Tov15(ctx context.Context, txn txnInterface) error {
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
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}

	var afterCount int64
	err = txn.QueryRowContext(ctx, "SELECT COUNT(*) FROM statuses").Scan(&afterCount)
	if err != nil {
		return err
	}
	if beforeCount != afterCount {
		return fmt.Errorf("got %d statuses after update, %d before", afterCount, beforeCount)
	}
	return nil
}

func v15Tov16(ctx context.Context, txn txnInterface) error {
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
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v16Tov17(ctx context.Context, txn txnInterface) error {
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
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v17Tov18(ctx context.Context, txn txnInterface) error {
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
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v18Tov19(ctx context.Context, txn txnInterface) error {
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
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v19Tov20(ctx context.Context, txn txnInterface) error {
	// add a "statusstate" column to status with default value {}
	sqlStmt := `
			ALTER TABLE statuses ADD COLUMN statusstate TEXT NOT NULL DEFAULT "{}";
		`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

func v20Tov21(ctx context.Context, txn txnInterface) error {
	// Set foreign key on accountstate.
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
				uid TEXT NOT NULL,

				FOREIGN KEY(uid) REFERENCES userstate(uid)
			) STRICT;

			INSERT INTO accountstate (asid, state, uid)
				SELECT asid, state, uid FROM accountstateold;

			DROP TABLE accountstateold;
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}

	// Set foreign key on statuses.
	sqlStmt = `
			ALTER TABLE statuses RENAME TO statusesold;

			-- Statuses which were obtained from Mastodon.
			CREATE TABLE statuses (
				-- A unique ID.
				sid INTEGER PRIMARY KEY AUTOINCREMENT,
				-- The Mastopoof account that got that status.
				asid INTEGER NOT NULL,
				-- The status, serialized as JSON.
				status TEXT NOT NULL,
				-- metadata/state about a status (e.g.: filters applied to it)
				statusstate TEXT NOT NULL DEFAULT "{}",

				FOREIGN KEY(asid) REFERENCES accountstate(asid)
			) STRICT;

			INSERT INTO statuses (sid, asid, status, statusstate)
				SELECT sid, asid, status, statusstate FROM statusesold;

			DROP TABLE statusesold;
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}

	// Set primary key and foreign keys on streamcontent.
	sqlStmt = `
			ALTER TABLE streamcontent RENAME TO streamcontentold;

			-- The actual content of a stream. In practice, this links position in the stream to a specific status.
			CREATE TABLE "streamcontent" (
				stid INTEGER NOT NULL,
				sid INTEGER NOT NULL,
				position INTEGER,

				PRIMARY KEY (stid, sid),
				FOREIGN KEY(stid) REFERENCES streamstate(stid),
				FOREIGN KEY(sid) REFERENCES statuses(sid)
			) STRICT;

			INSERT INTO streamcontent (stid, sid, position)
				SELECT stid, sid, position FROM streamcontentold;

			DROP TABLE streamcontentold;
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}

	return nil
}

func v21Tov22(ctx context.Context, txn txnInterface) error {
	// Add indices on streamcontent.
	sqlStmt := `
		CREATE INDEX streamcontent_stid ON streamcontent(stid);
		CREATE INDEX streamcontent_sid ON streamcontent(sid);
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}
