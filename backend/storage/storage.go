package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"

	_ "github.com/mattn/go-sqlite3"
)

// AuthInfo represents information about a user authentification in the DB.
type AuthInfo struct {
	// Auth UID within storage.
	UID int64 `json:"uid"`

	ServerAddr   string `json:"server_addr"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
	RedirectURI  string `json:"redirect_uri"`

	AccessToken string `json:"access_token"`
}

// UserState is the state of a user, stored as JSON in the DB.
type UserState struct {
	// User ID.
	UID int64 `json:"uid"`
	// Last home status ID fetched.
	LastHomeStatusID mastodon.ID `json:"last_home_status_id"`
}

// ListingState is the state of a single listing, stored as JSON.
type ListingState struct {
	// Listing ID.
	LID int64 `json:"lid"`
	// User ID this listing belongs to.
	UID int64 `json:"uid"`
	// Position of the latest read status in this listing.
	LastRead int64 `json:"last_read"`
}

type SQLQueryable interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

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

const schemaVersion = 4

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

	if _, err := txn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d;`, schemaVersion)); err != nil {
		return fmt.Errorf("unable to set user_version: %w", err)
	}

	// And commit the change.
	if err := txn.Commit(); err != nil {
		return fmt.Errorf("unable to update DB schema: %w", err)
	}
	return nil
}

// ClearAll clears the database beside authentication.
func (st *Storage) ClearAll(ctx context.Context) error {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("unable to clean DB state: %w", err)
	}
	defer txn.Rollback()

	if _, err := txn.ExecContext(ctx, `DELETE FROM userstate;`); err != nil {
		return fmt.Errorf("unable to delete userstate: %w", err)
	}
	if _, err := txn.ExecContext(ctx, `DELETE FROM statuses;`); err != nil {
		return fmt.Errorf("unable to delete statuses: %w", err)
	}

	return txn.Commit()
}

func (st *Storage) AuthInfo(ctx context.Context, db SQLQueryable) (*AuthInfo, error) {
	var content string
	err := db.QueryRowContext(ctx, "SELECT content FROM authinfo").Scan(&content)
	if err == sql.ErrNoRows {
		glog.Infof("no authinfo in storage")
		return &AuthInfo{UID: 1}, nil
	}
	if err != nil {
		return nil, err
	}

	ai := &AuthInfo{}
	if err := json.Unmarshal([]byte(content), ai); err != nil {
		return nil, fmt.Errorf("unable to decode authinfo content: %v", err)
	}
	return ai, nil
}

func (st *Storage) SetAuthInfo(ctx context.Context, db SQLQueryable, ai *AuthInfo) error {
	content, err := json.Marshal(ai)
	if err != nil {
		return err
	}

	stmt := `INSERT INTO authinfo(uid, content) VALUES(?, ?) ON CONFLICT(uid) DO UPDATE SET content = ?`
	_, err = db.ExecContext(ctx, stmt, ai.UID, content, content)
	if err != nil {
		return err
	}
	return nil
}

func (st *Storage) UserState(ctx context.Context, db SQLQueryable, uid int64) (*UserState, error) {
	var jsonString string
	err := db.QueryRowContext(ctx, "SELECT state FROM userstate WHERE uid = ?", uid).Scan(&jsonString)
	if err == sql.ErrNoRows {
		return &UserState{UID: uid}, nil
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

func (st *Storage) ListingState(ctx context.Context, db SQLQueryable, lid int64) (*ListingState, error) {
	var jsonString string
	err := db.QueryRowContext(ctx, "SELECT state FROM listingstate WHERE lid = ?", lid).Scan(&jsonString)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("listing with lid=%d not found", lid)
	}
	if err != nil {
		return nil, err
	}

	listingState := &ListingState{}
	if err := json.Unmarshal([]byte(jsonString), listingState); err != nil {
		return nil, fmt.Errorf("unable to decode listingstate content: %v", err)
	}
	return listingState, nil
}

func (st *Storage) SetListingState(ctx context.Context, db SQLQueryable, listingState *ListingState) error {
	jsonString, err := json.Marshal(listingState)
	if err != nil {
		return err
	}
	stmt := `INSERT INTO listingstate(lid, state) VALUES(?, ?) ON CONFLICT(lid) DO UPDATE SET state = ?`
	_, err = db.ExecContext(ctx, stmt, listingState.LID, jsonString, jsonString)
	if err != nil {
		return err
	}
	return nil
}

func (st *Storage) ClearStream(ctx context.Context, lid int64) error {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	// Remove everything from the stream.
	if _, err := txn.ExecContext(ctx, `DELETE FROM listingcontent WHERE lid = ?`, lid); err != nil {
		return err
	}

	// Also reset last-read.
	listingState, err := st.ListingState(ctx, txn, lid)
	if err != nil {
		return err
	}
	listingState.LastRead = 0
	if err := st.SetListingState(ctx, txn, listingState); err != nil {
		return err
	}

	return txn.Commit()
}

type OpenStatus struct {
	// Position in the listing.
	Position int64           `json:"position"`
	Status   mastodon.Status `json:"status"`
}

// Opened returns the currently open statuses in the listing.
func (st *Storage) Opened(ctx context.Context, lid int64) ([]*OpenStatus, error) {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	listingState, err := st.ListingState(ctx, txn, lid)
	if err != nil {
		return nil, err
	}

	rows, err := st.DB.QueryContext(ctx, `
		SELECT
			listingcontent.position,
			statuses.status
		FROM
			statuses
			INNER JOIN listingcontent
			USING (sid)
		WHERE
			listingcontent.lid = ?
			AND listingcontent.position > ?
		ORDER BY listingcontent.position
		;
	`, lid, listingState.LastRead)
	if err != nil {
		return nil, err
	}

	results := []*OpenStatus{}

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

		results = append(results, &OpenStatus{
			Position: position,
			Status:   status,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, txn.Commit()
}

// LastPosition returns the position of the latest added status in the stream.
// Returns (0, nil) if there the stream is currently empty.
func (st *Storage) LastPosition(ctx context.Context, lid int64, db SQLQueryable) (int64, error) {
	var position int64
	err := db.QueryRowContext(ctx, `
		SELECT
			position
		FROM
			listingcontent
		WHERE
			lid = ?
		ORDER BY position DESC
		LIMIT 1
	`, lid).Scan(&position)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return position, nil
}

// StatusAt gets the status at the provided position in the stream.
// Returns nil, nil if there is no matching status at that position.
func (st *Storage) StatusAt(ctx context.Context, lid int64, position int64) (*OpenStatus, error) {
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
			INNER JOIN listingcontent
			USING (sid)
		WHERE
			listingcontent.lid = ?
			AND listingcontent.position = ?
		;
	`, lid, position)

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

	return &OpenStatus{
		Position: position,
		Status:   status,
	}, txn.Commit()
}

// PickNext
// Return (nil, nil) if there is no next status.
func (st *Storage) PickNext(ctx context.Context, lid int64) (*OpenStatus, error) {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()
	o, err := st.pickNextInTxn(ctx, lid, txn)
	if err != nil {
		return nil, err
	}
	return o, txn.Commit()
}

func (st *Storage) pickNextInTxn(ctx context.Context, lid int64, txn *sql.Tx) (*OpenStatus, error) {
	// List all statuses which are not listed yet in "listingcontent" for that listing ID.
	rows, err := txn.QueryContext(ctx, `
		SELECT
			statuses.sid,
			statuses.status
		FROM
			statuses
			LEFT OUTER JOIN listingcontent
			USING (sid)
		WHERE
			(listingcontent.lid IS NULL OR listingcontent.lid != ?)
		;
	`, lid)
	if err != nil {
		return nil, err
	}

	var selectedID int64
	var selected *mastodon.Status
	for rows.Next() {
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
		return nil, nil
	}

	// Now, add that status to the listing.
	// Pick current last filled position.
	position, err := st.LastPosition(ctx, lid, txn)
	if err != nil {
		return nil, err
	}
	// Pick the largest existing (or 0) position and just add one to create a new one.
	position += 1

	// Insert the newly selected status in the stream.
	stmt := `INSERT INTO listingcontent(lid, sid, position) VALUES(?, ?, ?);`
	_, err = txn.ExecContext(ctx, stmt, lid, selectedID, position)
	if err != nil {
		return nil, err
	}

	return &OpenStatus{
		Position: position,
		Status:   *selected,
	}, nil
}

type FetchResult struct {
	LastRead int64
	AtEnd    bool
	Items    []*OpenStatus
}

// FetchForward get statuses after the provided position.
// It can insert things in the stream if necessary.
func (st *Storage) FetchForward(ctx context.Context, lid int64, refPosition int64) (*FetchResult, error) {
	if refPosition < 0 {
		return nil, fmt.Errorf("invalid position %d", refPosition)
	}

	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	result := &FetchResult{}

	listingState, err := st.ListingState(ctx, txn, lid)
	if err != nil {
		return nil, err
	}
	result.LastRead = listingState.LastRead

	if refPosition == 0 {
		refPosition = listingState.LastRead
	}

	maxCount := 10

	// Fetch what is currently available after refPosition
	rows, err := st.DB.QueryContext(ctx, `
		SELECT
			listingcontent.position,
			statuses.status
		FROM
			statuses
			INNER JOIN listingcontent
			USING (sid)
		WHERE
			listingcontent.lid = ?
			AND listingcontent.position > ?
		ORDER BY listingcontent.position
		LIMIT ?
		;
	`, lid, refPosition, maxCount)
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
		result.Items = append(result.Items, &OpenStatus{
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
		ost, err := st.pickNextInTxn(ctx, lid, txn)
		if err != nil {
			return nil, err
		}
		if ost == nil {
			// Nothing is available anymore to insert at this point.
			result.AtEnd = true
			break
		}
		result.Items = append(result.Items, ost)
	}

	return result, txn.Commit()
}

// FetchBackward get statuses before the provided position.
// It can insert things in the stream if necessary.
func (st *Storage) FetchBackward(ctx context.Context, lid int64, refPosition int64) (*FetchResult, error) {
	if refPosition < 0 {
		return nil, fmt.Errorf("invalid postion %d", refPosition)
	}

	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	result := &FetchResult{}

	listingState, err := st.ListingState(ctx, txn, lid)
	if err != nil {
		return nil, err
	}
	result.LastRead = listingState.LastRead

	if refPosition == 0 {
		refPosition = listingState.LastRead
	}

	maxCount := 10

	// Fetch what is currently available after refPosition
	rows, err := st.DB.QueryContext(ctx, `
		SELECT
			listingcontent.position,
			statuses.status
		FROM
			statuses
			INNER JOIN listingcontent
			USING (sid)
		WHERE
			listingcontent.lid = ?
			AND listingcontent.position < ?
		ORDER BY listingcontent.position DESC
		LIMIT ?
		;
	`, lid, refPosition, maxCount)
	if err != nil {
		return nil, err
	}

	// Result is descending by position, so reversed compared to what we want.
	var reverseItems []*OpenStatus
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
		reverseItems = append(reverseItems, &OpenStatus{
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

	// No fetching more - all is sorted already in stream when going backward.
	result.AtEnd = (len(result.Items) < maxCount) || (result.Items[0].Position <= 1)

	return result, txn.Commit()
}
