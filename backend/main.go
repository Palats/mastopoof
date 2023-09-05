package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/davecgh/go-spew/spew"
	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"

	_ "github.com/mattn/go-sqlite3"
)

var (
	serverAddr = flag.String("server", "", "Mastodon server to track. Only needed when authenticating.")
	clearApp   = flag.Bool("clear_app", false, "Force re-registration of the app against the Mastodon server")
	clearAuth  = flag.Bool("clear_auth", false, "Force re-approval of auth; does not touch app registration")
)

type AuthInfo struct {
	// Auth ID within storage.
	ID int

	ServerAddr   string `json:"server_addr"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
	RedirectURI  string `json:"redirect_uri"`

	AccessToken string `json:"access_token"`
}

type Storage struct {
	db *sql.DB
}

func NewStorage(db *sql.DB) *Storage {
	return &Storage{db: db}
}

const schemaVersion = 1

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
				id integer not null primary key,
				content text
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

func (st *Storage) Close() {}

func (st *Storage) AuthInfo(ctx context.Context) (*AuthInfo, error) {
	var content string
	err := st.db.QueryRowContext(ctx, "SELECT content FROM authinfo").Scan(&content)
	if err == sql.ErrNoRows {
		glog.Infof("no authinfo in storage")
		return &AuthInfo{ID: 0}, nil
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

func (st *Storage) SetAuthInfo(ctx context.Context, ai *AuthInfo) error {
	content, err := json.Marshal(ai)
	if err != nil {
		return err
	}

	stmt := `INSERT INTO authinfo(id, content) VALUES(?, ?) ON CONFLICT(id) DO UPDATE SET content = ?`
	_, err = st.db.ExecContext(ctx, stmt, ai.ID, content, content)
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

func cmdAuth(ctx context.Context, st *Storage) error {
	ai, err := st.AuthInfo(ctx)
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

		if err := st.SetAuthInfo(ctx, ai); err != nil {
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
			glog.Exit(err)
		}
		ai.ClientID = app.ClientID
		ai.ClientSecret = app.ClientSecret
		ai.AuthURI = app.AuthURI
		ai.RedirectURI = app.RedirectURI

		if err := st.SetAuthInfo(ctx, ai); err != nil {
			glog.Exit(err)
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
			glog.Exitf("unable to read stdin: %v", err)
		}
		authCode = strings.TrimSpace(authCode)
		if authCode == "" {
			glog.Exit("empty code, aborting")
		}

		client := mastodon.NewClient(&mastodon.Config{
			Server:       ai.ServerAddr,
			ClientID:     ai.ClientID,
			ClientSecret: ai.ClientSecret,
		})
		err = client.AuthenticateToken(ctx, authCode, ai.RedirectURI)
		if err != nil {
			glog.Exitf("unable to authenticate on server %s: %v", ai.ServerAddr, err)
		}

		ai.AccessToken = client.Config.AccessToken
		if err := st.SetAuthInfo(ctx, ai); err != nil {
			glog.Exit(err)
		}
	} else {
		fmt.Println("Already authentified.")
	}

	return nil
}

func cmdList(ctx context.Context, client *mastodon.Client) error {
	timeline, err := client.GetTimelineHome(context.Background(), nil)
	if err != nil {
		return err
	}
	for i := len(timeline) - 1; i >= 0; i-- {
		fmt.Println("-------------------------------")
		spew.Dump(timeline[i].ID, timeline[i].URL)
	}
	return nil
}

func run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		glog.Exit("missing subcommand")
	}

	st, db, err := getStorage(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	switch cmd := args[0]; cmd {
	case "auth":
		return cmdAuth(ctx, st)
	case "list":
		ai, err := st.AuthInfo(ctx)
		if err != nil {
			return err
		}
		client := mastodon.NewClient(&mastodon.Config{
			Server:       ai.ServerAddr,
			ClientID:     ai.ClientID,
			ClientSecret: ai.ClientSecret,
			AccessToken:  ai.AccessToken,
		})
		return cmdList(ctx, client)
	default:
		return fmt.Errorf("unknown command %s", cmd)
	}
}

func main() {
	ctx := context.Background()
	flag.Parse()
	fmt.Println("Mastopoof")

	err := run(ctx, flag.Args())
	if err != nil {
		glog.Exit(err)
	}
}
