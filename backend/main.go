package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/davecgh/go-spew/spew"
	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	_ "github.com/mattn/go-sqlite3"
)

var _ = spew.Sdump("")

var (
	serverAddr = flag.String("server", "", "Mastodon server to track. Only needed when authenticating.")
	clearApp   = flag.Bool("clear_app", false, "Force re-registration of the app against the Mastodon server")
	clearAuth  = flag.Bool("clear_auth", false, "Force re-approval of auth; does not touch app registration")
	port       = flag.Int("port", 8079, "Port to listen on for the 'serve' command")
	listingID  = flag.Int("listing_id", 1, "Listing to use")
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
	db *sql.DB
}

func NewStorage(db *sql.DB) *Storage {
	return &Storage{db: db}
}

const schemaVersion = 4

func (st *Storage) Init(ctx context.Context) error {
	// Get version of the storage.
	row := st.db.QueryRow("PRAGMA user_version")
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
	txn, err := st.db.BeginTx(ctx, nil)
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

		rows, err := st.db.QueryContext(ctx, `SELECT sid, status FROM statuses`)
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

// ClearState clears the database beside authentication.
func (st *Storage) ClearState(ctx context.Context) error {
	txn, err := st.db.BeginTx(ctx, nil)
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

func (st *Storage) ListingState(ctx context.Context, db SQLQueryable, lid int) (*ListingState, error) {
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

func registerApp(ctx context.Context, ai *AuthInfo) (*mastodon.Application, error) {
	app, err := mastodon.RegisterApp(ctx, &mastodon.AppConfig{
		Server:     ai.ServerAddr,
		ClientName: "mastopoof",
		Scopes:     "read",
		Website:    "https://github.com/Palats/mastopoof",
	})
	if err != nil {
		return nil, fmt.Errorf("unable to register app on server %s: %w", ai.ServerAddr, err)
	}
	return app, nil
}

func getStorage(ctx context.Context) (*Storage, *sql.DB, error) {
	filename := "./mastopoof.db"
	db, err := sql.Open("sqlite3", filename)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to open storage %s: %w", filename, err)
	}

	st := NewStorage(db)
	if err := st.Init(ctx); err != nil {
		return nil, nil, fmt.Errorf("unable to init storage: %w", err)
	}
	return st, db, nil
}

func cmdInfo(ctx context.Context, st *Storage) error {
	txn, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()
	ai, err := st.AuthInfo(ctx, txn)
	if err != nil {
		return err
	}

	us, err := st.UserState(ctx, txn, ai.UID)
	if err != nil {
		return err
	}
	fmt.Println("Local account UID:", ai.UID)
	fmt.Println("Server address:", ai.ServerAddr)
	fmt.Println("Client ID:", ai.ClientID)
	fmt.Println("AuthURI:", ai.AuthURI)
	fmt.Println("Last home status ID:", us.LastHomeStatusID)

	// Should be readonly.
	return txn.Commit()
}

func cmdAuth(ctx context.Context, st *Storage) error {
	txn, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()
	ai, err := st.AuthInfo(ctx, txn)
	if err != nil {
		return err
	}

	addr := *serverAddr
	if addr == "" {
		return errors.New("missing --server")
	}
	if !strings.HasPrefix(addr, "https://") {
		return fmt.Errorf("server address %q must start with https://", addr)
	}

	if ai.ServerAddr == "" || *clearApp {
		glog.Infof("setting server address")
		if *serverAddr == "" {
			return errors.New("please specify server name with --server")
		}
		ai.ServerAddr = *serverAddr

		if err := st.SetAuthInfo(ctx, txn, ai); err != nil {
			return err
		}
	} else {
		glog.Infof("server address: %v", ai.ServerAddr)
		if addr != ai.ServerAddr {
			return fmt.Errorf("server mismatch: %s vs %s; use --clear_app", ai.ServerAddr, addr)
		}
	}

	if ai.ClientID == "" || *clearApp {
		glog.Infof("registering app")
		app, err := registerApp(ctx, ai)
		if err != nil {
			return err
		}
		ai.ClientID = app.ClientID
		ai.ClientSecret = app.ClientSecret
		ai.AuthURI = app.AuthURI
		ai.RedirectURI = app.RedirectURI

		if err := st.SetAuthInfo(ctx, txn, ai); err != nil {
			return err
		}
	} else {
		glog.Infof("app already registered")
	}

	if ai.AccessToken == "" || *clearAuth || *clearApp {
		glog.Infof("need user code")
		fmt.Printf("Auth URL: %s\n", ai.AuthURI)

		reader := bufio.NewReader(os.Stdin)
		fmt.Printf("Enter code:")
		authCode, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("unable to read stdin: %w", err)
		}
		authCode = strings.TrimSpace(authCode)
		if authCode == "" {
			return errors.New("empty code, aborting")
		}

		client := mastodon.NewClient(&mastodon.Config{
			Server:       ai.ServerAddr,
			ClientID:     ai.ClientID,
			ClientSecret: ai.ClientSecret,
		})
		err = client.AuthenticateToken(ctx, authCode, ai.RedirectURI)
		if err != nil {
			return fmt.Errorf("unable to authenticate on server %s: %w", ai.ServerAddr, err)
		}

		ai.AccessToken = client.Config.AccessToken
		if err := st.SetAuthInfo(ctx, txn, ai); err != nil {
			return err
		}
	} else {
		fmt.Println("Already authentified.")
	}

	return txn.Commit()
}

func cmdClearState(ctx context.Context, st *Storage) error {
	return st.ClearState(ctx)
}

func cmdFetch(ctx context.Context, st *Storage, authInfo *AuthInfo, client *mastodon.Client) error {
	txn, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	userState, err := st.UserState(ctx, txn, authInfo.UID)
	if err != nil {
		return fmt.Errorf("unable to fetch user state: %w", err)
	}

	fetchCount := 0
	// Do multiple fetching, until either up to date, or up to a boundary to avoid infinite loops by mistake.
	for fetchCount < 10 {
		// Pagination object is updated by GetTimelimeHome, based on the `Link` header
		// returned by the API - see https://docs.joinmastodon.org/api/guidelines/#pagination .
		// In practice, it seems:
		//  - MinID is set to the most recent ID returned (from the "prev" Link, which is for future statuses)
		//  - MaxID is set to an older ID (from the "next" Link, which is for older status)
		//  - SinceID, Limit are empty/0.
		// See https://github.com/mattn/go-mastodon/blob/9faaa4f0dc23d9001ccd1010a9a51f56ba8d2f9f/mastodon.go#L317
		// It seems that if MaxID and MinID are identical, it means the end has been reached and some result were given.
		// And if there is no MaxID, the end has been reached.
		pg := &mastodon.Pagination{
			MinID: userState.LastHomeStatusID,
		}
		glog.Infof("Fetching from %s", pg.MinID)
		timeline, err := client.GetTimelineHome(ctx, pg)
		if err != nil {
			return err
		}
		glog.Infof("Found %d new status on home timeline", len(timeline))

		for _, status := range timeline {
			if IDNewer(status.ID, userState.LastHomeStatusID) {
				userState.LastHomeStatusID = status.ID
			}

			jsonString, err := json.Marshal(status)
			if err != nil {
				return err
			}
			stmt := `INSERT INTO statuses(uid, uri, status) VALUES(?, ?, ?)`
			_, err = txn.ExecContext(ctx, stmt, authInfo.UID, status.URI, jsonString)
			if err != nil {
				return err
			}
		}

		fetchCount++
		// Pagination got updated.
		if pg.MinID != userState.LastHomeStatusID {
			// Either there is a mismatch in the data or no `Link` was returned
			// - in either case, we don't know enough to safely continue.
			glog.Infof("no returned MinID / ID mismatch, stopping fetch")
			break
		}
		if pg.MaxID == "" || pg.MaxID == pg.MinID {
			// We've reached the end - either nothing was fetched, or just the
			// latest ones.
			break
		}
		if len(timeline) == 0 {
			// Nothing was returned, assume it is because we've reached the end.
			break
		}
	}

	if err := st.SetUserState(ctx, txn, userState); err != nil {
		return err
	}

	return txn.Commit()
}

type httpErr int

func (herr httpErr) Error() string {
	return fmt.Sprintf("%d: %s", int(herr), http.StatusText(int(herr)))
}

func httpFunc(f func(w http.ResponseWriter, r *http.Request) error) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		err := f(w, r)
		if err != nil {
			var herr httpErr
			if errors.As(err, &herr) {
				http.Error(w, err.Error(), int(herr))
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
	}
}

func cmdServe(ctx context.Context, st *Storage, authInfo *AuthInfo) error {
	http.HandleFunc("/list", httpFunc(func(w http.ResponseWriter, r *http.Request) error {
		rows, err := st.db.QueryContext(ctx, `SELECT status FROM statuses WHERE uid = ?`, authInfo.UID)
		if err != nil {
			return err
		}

		data := []any{}
		for rows.Next() {
			var jsonString string
			if err := rows.Scan(&jsonString); err != nil {
				return err
			}
			var status mastodon.Status
			if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
				return err
			}

			data = append(data, status)
		}
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(data)
	}))

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Listening on %s...\n", addr)
	return http.ListenAndServe(addr, nil)
}

func cmdList(ctx context.Context, st *Storage, authInfo *AuthInfo) error {
	rows, err := st.db.QueryContext(ctx, `SELECT status FROM statuses WHERE uid = ?`, authInfo.UID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var jsonString string
		if err := rows.Scan(&jsonString); err != nil {
			return err
		}
		var status mastodon.Status
		if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
			return err
		}
		fmt.Printf("Status %s: %s\n", status.ID, status.URL)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func cmdDumpStatus(ctx context.Context, st *Storage, authInfo *AuthInfo, args []string) error {
	rows, err := st.db.QueryContext(ctx, `SELECT status FROM statuses WHERE uid = ?`, authInfo.UID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var jsonString string
		if err := rows.Scan(&jsonString); err != nil {
			return err
		}
		var status mastodon.Status
		if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
			return err
		}

		for _, a := range args {
			if a != string(status.ID) {
				continue
			}
			spew.Dump(status)
		}
	}
	return nil
}

func cmdNewListing(ctx context.Context, st *Storage, authInfo *AuthInfo) error {
	txn, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	var lid int64
	err = txn.QueryRowContext(ctx, "SELECT lid FROM listingstate ORDER BY lid LIMIT 1").Scan(&lid)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	// Pick the largest existing (or 0) listing ID and just add one to create a new one.
	lid += 1

	listing := &ListingState{
		LID: lid,
		UID: authInfo.UID,
	}

	if err := st.SetListingState(ctx, txn, listing); err != nil {
		return err
	}

	if err := txn.Commit(); err != nil {
		return err
	}

	// And now, re-read it to output it.
	listing, err = st.ListingState(ctx, st.db, int(lid))
	if err != nil {
		return err
	}
	spew.Dump(listing)
	return nil
}

func cmdPickNext(ctx context.Context, st *Storage, authInfo *AuthInfo) error {
	lid := *listingID

	// List all statuses which are not listed yet in "listingcontent" for that listing ID.
	rows, err := st.db.QueryContext(ctx, `
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
		return err
	}

	var selectedID int64
	var selected *mastodon.Status
	for rows.Next() {
		var sid int64
		var jsonString string
		if err := rows.Scan(&sid, &jsonString); err != nil {
			return err
		}
		var status mastodon.Status
		if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
			return err
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
		return err
	}

	if selected == nil {
		fmt.Println("No next status available")
		return nil
	}

	// Now, add that status to the listing.
	txn, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	var position int64
	err = txn.QueryRowContext(ctx, `
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
		return err
	}
	// Pick the largest existing (or 0) position and just add one to create a new one.
	position += 1

	stmt := `INSERT INTO listingcontent(lid, sid, position) VALUES(?, ?, ?);`
	_, err = txn.ExecContext(ctx, stmt, lid, selectedID, position)
	if err != nil {
		return err
	}

	spew.Dump(selected)

	return txn.Commit()
}

func run(ctx context.Context) error {
	st, db, err := getStorage(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	var rootCmd = &cobra.Command{
		Use:   "mastopoof",
		Short: "Mastopoof is a Mastodon client",
		Long:  `More about Mastopoof`,
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "info",
		Short: "Current account config info",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdInfo(ctx, st)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "auth",
		Short: "Authenticate against server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdAuth(ctx, st)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "clearstate",
		Short: "Nuke all state in the DB, except for auth against Mastodon server.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdClearState(ctx, st)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "me",
		Short: "Get information about one's own account.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.db)
			if err != nil {
				return err
			}
			client := mastodon.NewClient(&mastodon.Config{
				Server:       ai.ServerAddr,
				ClientID:     ai.ClientID,
				ClientSecret: ai.ClientSecret,
				AccessToken:  ai.AccessToken,
			})
			account, err := client.GetAccountCurrentUser(ctx)
			if err != nil {
				return err
			}
			spew.Dump(account)
			return nil
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "fetch",
		Short: "Fetch recent home content and add it to the DB.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.db)
			if err != nil {
				return err
			}
			client := mastodon.NewClient(&mastodon.Config{
				Server:       ai.ServerAddr,
				ClientID:     ai.ClientID,
				ClientSecret: ai.ClientSecret,
				AccessToken:  ai.AccessToken,
			})
			return cmdFetch(ctx, st, ai, client)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Run mastopoof backend server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.db)
			if err != nil {
				return err
			}
			return cmdServe(ctx, st, ai)

		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Get list of known statuses",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.db)
			if err != nil {
				return err
			}
			return cmdList(ctx, st, ai)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "dumpstatus",
		Short: "Display one status, identified by ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.db)
			if err != nil {
				return err
			}
			return cmdDumpStatus(ctx, st, ai, args)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "new-listing",
		Short: "Create a new empty listing.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.db)
			if err != nil {
				return err
			}
			return cmdNewListing(ctx, st, ai)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "pick-next",
		Short: "Add a status to the listing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.db)
			if err != nil {
				return err
			}
			return cmdPickNext(ctx, st, ai)
		},
	})

	return rootCmd.ExecuteContext(ctx)
}

func main() {
	ctx := context.Background()
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	fmt.Println("Mastopoof")

	err := run(ctx)
	if err != nil {
		glog.Exit(err)
	}
}
