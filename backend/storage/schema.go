package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"
)

// schemaVersion indicates up to which version the database schema was configured.
// It is incremented everytime a change is made.
const schemaVersion = 10

// refSchema is the database schema as if it was created from scratch. This is
// used only for comparison with an actual schema, for consistency checking. In
// practice, the database is setup using prepareDB, which set things up
// progressively, reflecting the evolution of the DB schema - and this refSchema
// is ignored for that purpose.
const refSchema = `
	CREATE TABLE accountstate (
		asid INTEGER PRIMARY KEY,
		content TEXT NOT NULL,
		uid TEXT
	);

	CREATE TABLE serverstate (
		server_addr STRING NOT NULL,
		state TEXT NOT NULL
	);

	CREATE TABLE statuses (
		sid INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL,
		status TEXT NOT NULL,
		uri TEXT
	);

	CREATE TABLE "streamcontent" (
		stid INTEGER NOT NULL,
		sid INTEGER NOT NULL,
		position INTEGER NOT NULL
	);

	CREATE TABLE "streamstate" (
		stid INTEGER PRIMARY KEY,
		state TEXT NOT NULL
	);

	CREATE TABLE userstate (
		uid INTEGER PRIMARY KEY,
		state TEXT NOT NULL
	);
`

// prepareDB creates the schema for the database or update
// it if needed.
func prepareDB(ctx context.Context, db *sql.DB) error {
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
	glog.Infof("PRAGMA user_version is %d (target=%v)", version, schemaVersion)
	if version > schemaVersion {
		return fmt.Errorf("user_version of DB (%v) is higher than supported schema version (%v)", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}

	glog.Infof("updating database schema")

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
			return fmt.Errorf("schema v1: unable to run %q: %w", sqlStmt, err)
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
			return fmt.Errorf("schema v2: unable to run %q: %w", sqlStmt, err)
		}
	}

	if version < 3 {
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
			return fmt.Errorf("schema v4: unable to run %q: %w", sqlStmt, err)
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
			return fmt.Errorf("schema v5: unable to run %q: %w", sqlStmt, err)
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
			return fmt.Errorf("schema v6: unable to run %q: %w", sqlStmt, err)
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
			return fmt.Errorf("schema v7: unable to run %q: %w", sqlStmt, err)
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
			return fmt.Errorf("schema v8: unable to run %q: %w", sqlStmt, err)
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
			return fmt.Errorf("schema v9: unable to run %q: %w", sqlStmt, err)
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
			return fmt.Errorf("schema v10: unable to run %q: %w", sqlStmt, err)
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
