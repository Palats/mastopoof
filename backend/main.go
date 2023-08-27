package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

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

func (st *Storage) Init(ctx context.Context) error {
	sqlStmt := `
		CREATE TABLE IF NOT EXISTS authinfo (
			id integer not null primary key,
			content text
		);
	`
	_, err := st.db.Exec(sqlStmt)
	if err != nil {
		return fmt.Errorf("unable to run %q: %w", sqlStmt, err)
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
		Scopes:     "read write follow",
		Website:    "https://github.com/Palats/mastopoof",
	})
	if err != nil {
		return nil, fmt.Errorf("unable to register app on server %s: %w", ai.ServerAddr, err)
	}
	return app, nil
}

func testClient(ctx context.Context, client *mastodon.Client) {
	timeline, err := client.GetTimelineHome(context.Background(), nil)
	if err != nil {
		glog.Fatal(err)
	}
	for i := len(timeline) - 1; i >= 0; i-- {
		fmt.Println(timeline[i])
	}
}

func main() {
	ctx := context.Background()
	flag.Parse()
	fmt.Println("Mastopoof")

	db, err := sql.Open("sqlite3", "./mastopoof.db")
	if err != nil {
		glog.Fatal(err)
	}
	defer db.Close()

	st := NewStorage(db)
	if err := st.Init(ctx); err != nil {
		glog.Exitf("unable to init storage: %v", err)
	}

	ai, err := st.AuthInfo(ctx)
	if err != nil {
		glog.Exit(err)
	}

	if ai.ServerAddr == "" || *clearApp {
		glog.Infof("setting server address")
		if *serverAddr == "" {
			glog.Exit("please specify server name with --server")
		}
		ai.ServerAddr = *serverAddr

		if err := st.SetAuthInfo(ctx, ai); err != nil {
			glog.Exit(err)
		}
	} else {
		glog.Infof("server address: %v", ai.ServerAddr)
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

	if ai.AccessToken == "" || *clearAuth {
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
		glog.Infof("access token available")
	}

	client := mastodon.NewClient(&mastodon.Config{
		Server:       ai.ServerAddr,
		ClientID:     ai.ClientID,
		ClientSecret: ai.ClientSecret,
		AccessToken:  ai.AccessToken,
	})
	/*err = client.AuthenticateToken(ctx, ai.AuthCode, ai.RedirectURI)
	if err != nil {
		glog.Exitf("unable to authenticate on server %s: %v", ai.ServerAddr, err)
	}*/

	testClient(ctx, client)
}
