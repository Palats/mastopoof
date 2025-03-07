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

	for {
		version, err := getCurrentVersion(ctx, db)
		if err != nil {
			return err
		}

		if version > targetVersion {
			return fmt.Errorf("user_version of DB (%v) is higher than target schema version (%v)", version, targetVersion)
		}
		if version == targetVersion {
			break
		}

		step := allSteps[version]
		if err := prepareDBSingleStep(ctx, db, step, version); err != nil {
			return err
		}
	}

	// Some update can create a lot of old table, recover space.
	if _, err := db.ExecContext(ctx, `VACUUM;`); err != nil {
		return fmt.Errorf("unable to vacuum db: %w", err)
	}
	// And optimize.
	if _, err := db.ExecContext(ctx, `PRAGMA optimize;`); err != nil {
		return fmt.Errorf("unable to vacuum db: %w", err)
	}

	glog.Infof("update done.")
	return nil
}

// prepareDBSingleStep applies the next change on the DB.
// Return true if there is no more changes needed.
func prepareDBSingleStep(ctx context.Context, db *sql.DB, step UpdateStep, expectedVersion int) (returnedErr error) {
	if step.DisableForeignKeys {
		// Prepare update of the database schema.
		// See https://www.sqlite.org/lang_altertable.html,
		//   "Making Other Kinds Of Table Schema Changes"
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF;`); err != nil {
			return fmt.Errorf("unable to deactivate foreign_keys: %w", err)
		}

		defer func() {
			if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = true;`); err != nil {
				glog.Errorf("unable to re-enable foreign keys: %v", err)
				if returnedErr == nil {
					returnedErr = err
				}
			}
		}()
	}

	txn, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("unable to update DB schema: %w", err)
	}
	defer txn.Rollback()

	// Get version of the storage.
	version, err := getCurrentVersion(ctx, txn)
	if err != nil {
		return err
	}
	glog.Infof("PRAGMA user_version is %d", version)

	if version != expectedVersion {
		return fmt.Errorf("concurrency error; expected version %d, got %d", expectedVersion, version)
	}

	stepVersion := version + 1

	glog.Infof("updating database schema from %d to %d...", version, stepVersion)

	// Apply the actual changes.
	if err := step.Apply(ctx, txn); err != nil {
		return fmt.Errorf("unable to update from version %d to version %d: %w", version, stepVersion, err)
	}

	if _, err := txn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d;`, stepVersion)); err != nil {
		return fmt.Errorf("unable to set user_version: %w", err)
	}

	// Verify integrity.
	if _, err := txn.ExecContext(ctx, `PRAGMA foreign_key_check;`); err != nil {
		return fmt.Errorf("unable to check foreign keys db: %w", err)
	}

	// And commit the change.
	if err := txn.Commit(); err != nil {
		return fmt.Errorf("unable to update DB schema: %w", err)
	}

	return nil
}

func getCurrentVersion(ctx context.Context, txn txnInterface) (int, error) {
	row := txn.QueryRowContext(ctx, "PRAGMA user_version")
	if row == nil {
		return -1, fmt.Errorf("unable to find user_version")

	}
	var version int
	if err := row.Scan(&version); err != nil {
		return -1, fmt.Errorf("error parsing user_version: %w", err)
	}
	return version, nil
}

// UpdateStep represents a specific update of the DB schema.
type UpdateStep struct {
	Apply              updateFunc
	DisableForeignKeys bool
}

var allSteps []UpdateStep

// If adding anything, do not forget to increment the schema version.
func RegisterStep(step UpdateStep) UpdateStep {
	allSteps = append(allSteps, step)
	return step
}

type updateFunc func(context.Context, txnInterface) error

// maxSchemaVersion indicates up to which version the database schema was configured.
// It is incremented everytime a change is made.
const maxSchemaVersion = 31

func init() {
	if len(allSteps) != maxSchemaVersion {
		panic(fmt.Sprintf("Got %d update functions for schema version %d", len(allSteps), maxSchemaVersion))
	}
}

var _ = RegisterStep(UpdateStep{
	Apply: v0Tov1,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v1Tov2,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v2Tov3,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v3Tov4,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v4Tov5,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v5Tov6,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v6Tov7,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v7Tov8,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v8Tov9,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v9Tov10,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v10Tov11,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v11Tov12,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v12Tov13,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v13Tov14,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v14Tov15,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v15Tov16,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v16Tov17,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v17Tov18,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v18Tov19,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v19Tov20,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v20Tov21,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v21Tov22,
})

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

var _ = RegisterStep(UpdateStep{
	Apply: v22Tov23,
})

func v22Tov23(ctx context.Context, txn txnInterface) error {
	// Add virtual columns on statuses to hold status IDs.

	// Foreign keys:
	//  streamcontent -> statuses
	//  streamcontent -> streamstate
	//  statuses -> accountstate
	//  accountstate -> userstate
	sqlStmt := `
		ALTER TABLE statuses RENAME TO statusesold;
		ALTER TABLE streamcontent RENAME TO streamcontentold;

		DROP INDEX streamcontent_sid;
		DROP INDEX streamcontent_stid;

		-- Statuses which were obtained from Mastodon.
		CREATE TABLE statuses (
			-- A unique ID.
			sid INTEGER PRIMARY KEY AUTOINCREMENT,
			-- The Mastodon account that got that status.
			asid INTEGER NOT NULL,
			-- The status, serialized as JSON.
			status TEXT NOT NULL,
			-- metadata/state about a status (e.g.: filters applied to it)
			statusstate TEXT NOT NULL DEFAULT "{}",

			-- Keep the status ID readily available to find the status again easily.
			status_id TEXT NOT NULL GENERATED ALWAYS AS (json_extract(status, '$.id')) STORED,
			status_reblog_id TEXT GENERATED ALWAYS AS (json_extract(status, '$.reblog.id')) STORED,

			FOREIGN KEY(asid) REFERENCES accountstate(asid)
		) STRICT;

		-- The actual content of a stream. In practice, this links position in the stream to a specific status.
		CREATE TABLE "streamcontent" (
			stid INTEGER NOT NULL,
			sid INTEGER NOT NULL,
			position INTEGER,

			PRIMARY KEY (stid, sid),
			FOREIGN KEY(stid) REFERENCES streamstate(stid),
			FOREIGN KEY(sid) REFERENCES statuses(sid)
		) STRICT;

		CREATE INDEX streamcontent_stid ON streamcontent(stid);
		CREATE INDEX streamcontent_sid ON streamcontent(sid);


		INSERT INTO statuses (sid, asid, status, statusstate)
			SELECT sid, asid, status, statusstate FROM statusesold;

		INSERT INTO streamcontent (stid, sid, position)
			SELECT stid, sid, position FROM streamcontentold;

		DROP TABLE streamcontentold;
		DROP TABLE statusesold;
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

var _ = RegisterStep(UpdateStep{
	Apply: v23Tov24,
})

func v23Tov24(ctx context.Context, txn txnInterface) error {
	// Add index on statuses.
	sqlStmt := `
		CREATE INDEX statuses_asid_status_id ON statuses(asid, status_id);
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

var _ = RegisterStep(UpdateStep{
	Apply: v24Tov25,
})

func v24Tov25(ctx context.Context, txn txnInterface) error {
	// Change streamcontent indices.
	sqlStmt := `
		DROP INDEX streamcontent_stid;
		CREATE INDEX streamcontent_position ON streamcontent(position);
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

var _ = RegisterStep(UpdateStep{
	Apply: v25Tov26,
})

func v25Tov26(ctx context.Context, txn txnInterface) error {
	// Add more indices to find existing statuses.
	sqlStmt := `
		-- Index to help find statuses which have been seen before.
		CREATE INDEX statuses_status_id ON statuses(status_id);
		CREATE INDEX statuses_status_reblog_id ON statuses(status_reblog_id);
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

var _ = RegisterStep(UpdateStep{
	Apply: v26Tov27,
})

func v26Tov27(ctx context.Context, txn txnInterface) error {
	// Rename statusstate to status_meta
	sqlStmt := `
		ALTER TABLE statuses RENAME COLUMN statusstate TO status_meta;
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

var _ = RegisterStep(UpdateStep{
	Apply: v27Tov28,
})

func v27Tov28(ctx context.Context, txn txnInterface) error {
	// Add statuses IDs in the stream table.
	sqlStmt := `
		ALTER TABLE streamcontent RENAME TO streamcontentold;

		DROP INDEX streamcontent_sid;
		DROP INDEX streamcontent_position;

		---- Actual definition

		-- The actual content of a stream. In practice, this links position in the stream to a specific status.
		-- This is kept separate from table statuses. There are 2 reasons:
		--   - statuses table is more of a cache of status info from Mastodon than a Mastopoof user state.
		--   - Eventually, it should be possible to have multiple stream even with a single Mastodon account.
		-- While "statuses as cache" is not possible right now (e.g., status ID management), the hope
		-- of not having a 1:1 mapping between account and stream is important - thus not merging
		-- both tables (statuses & streamcontent).
		CREATE TABLE "streamcontent" (
			stid INTEGER NOT NULL,
			sid INTEGER NOT NULL,

			-- When a new status is fetched from Mastodon, it is inserted in both statuses
			-- and in streamcontent. As long as the status is not triaged, position is NULL.
			position INTEGER,

			-- A copy of some of the status information to facilitate
			-- sorting and operation on the stream.
			status_id TEXT NOT NULL,
			status_reblog_id TEXT,
			status_in_reply_to_id TEXT,

			PRIMARY KEY (stid, sid),
			FOREIGN KEY(stid) REFERENCES streamstate(stid),
			FOREIGN KEY(sid) REFERENCES statuses(sid)
		) STRICT;

		-- No index on stid, as there are many entry for a single stid value.
		CREATE INDEX streamcontent_sid ON streamcontent(sid);
		CREATE INDEX streamcontent_position ON streamcontent(position);

		---- And recreating the data

		INSERT INTO streamcontent (stid, sid, position, status_id, status_reblog_id, status_in_reply_to_id)
			SELECT
				old.stid,
				old.sid,
				old.position,
				json_extract(st.status, "$.id"),
				json_extract(st.status, "$.reblog.id"),
				json_extract(st.status, "$.in_reply_to_id")
			FROM
				streamcontentold AS old
				JOIN statuses AS st USING (sid)
			;

		DROP TABLE streamcontentold;
	`

	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

var _ = RegisterStep(UpdateStep{
	Apply: v28Tov29,
})

func v28Tov29(ctx context.Context, txn txnInterface) error {
	// Add triage info
	sqlStmt := `
		ALTER TABLE streamcontent ADD COLUMN stream_status_state TEXT NOT NULL DEFAULT "{}";
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

var _ = RegisterStep(UpdateStep{
	Apply: v29Tov30,
})

func v29Tov30(ctx context.Context, txn txnInterface) error {
	// Add some indexes.
	sqlStmt := `
		CREATE INDEX streamcontent_status_id ON streamcontent(status_id);
		CREATE INDEX streamcontent_status_reblog_id ON streamcontent(status_reblog_id);
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}

var _ = RegisterStep(UpdateStep{
	Apply:              v30Tov31,
	DisableForeignKeys: true,
})

func v30Tov31(ctx context.Context, txn txnInterface) error {
	// Fix type of uid column.
	sqlStmt := `
		-- State of a Mastodon account.
		CREATE TABLE accountstate_new (
			-- Unique id.
			asid INTEGER PRIMARY KEY,
			-- Protobuf mastopoof.storage.AccountState as JSON
			state TEXT NOT NULL,
			-- The user which owns this account.
			-- Immutable - even if another user ends up configuring that account,
			-- a new account state would be created.
			uid INTEGER NOT NULL,

			FOREIGN KEY(uid) REFERENCES userstate(uid)
		) STRICT;

		INSERT INTO accountstate_new (asid, state, uid)
			SELECT asid, state, uid FROM accountstate;

		DROP TABLE accountstate;

		ALTER TABLE accountstate_new RENAME TO accountstate;
	`
	if _, err := txn.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
	}
	return nil
}
