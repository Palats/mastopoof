package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"
	"github.com/davecgh/go-spew/spew"
	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/Palats/mastopoof/backend/mastodon"
	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	"github.com/Palats/mastopoof/backend/server"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/Palats/mastopoof/frontend"

	"github.com/Palats/mastopoof/proto/gen/mastopoof/mastopoofconnect"
	_ "github.com/mattn/go-sqlite3"
)

var _ = spew.Sdump("")

var (
	port        = flag.Int("port", 8079, "Port to listen on for the 'serve' command")
	userID      = flag.Int64("uid", 0, "User ID to use for commands. With 'serve', will auto login that user.")
	streamID    = flag.Int64("stream_id", 0, "Stream to use")
	dbFilename  = flag.String("db", "", "SQLite file")
	showAccount = flag.Bool("show_account", false, "Query and show account state from Mastodon server")
	selfURL     = flag.String("self_url", "", "URL to use for authentication redirection on the frontend. When empty, uses out-of-band auth.")
	inviteCode  = flag.String("invite_code", "", "If not empty, users can only be created by providing this code.")
)

func getStorage(ctx context.Context, filename string) (*storage.Storage, *sql.DB, error) {
	if filename == "" {
		return nil, nil, fmt.Errorf("missing database filename; try specifying --db <filename>")
	}
	glog.Infof("Using %s as datasource", filename)
	db, err := sql.Open("sqlite3", filename)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to open storage %s: %w", filename, err)
	}

	st := storage.NewStorage(db)
	if err := st.Init(ctx); err != nil {
		return nil, nil, fmt.Errorf("unable to init storage: %w", err)
	}
	return st, db, nil
}

func getUserID(_ context.Context, _ *storage.Storage) (int64, error) {
	return *userID, nil
}

func getStreamID(ctx context.Context, st *storage.Storage) (int64, error) {
	if *streamID != 0 {
		return *streamID, nil
	}
	if *userID != 0 {
		userState, err := st.UserState(ctx, st.DB, *userID)
		if err != nil {
			return 0, err
		}
		return userState.DefaultStID, nil
	}
	return 0, errors.New("no streamID / user ID specified")
}

func redirectURIFunc(target string) (func(string) string, error) {
	if target == "" {
		return func(string) string {
			return "urn:ietf:wg:oauth:2.0:oob"
		}, nil
	}

	baseURL, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("unable to parse redirect URI %q: %w", target, err)
	}
	baseURL = baseURL.JoinPath("_redirect")
	glog.Infof("Using redirect URI %s", baseURL)

	return func(serverAddr string) string {
		// RedirectURI for auth must contain information about the mastodon server
		// it is about. Otherwise, when getting a code back after auth, the server
		// cannot know what it is about.
		u := *baseURL // Make a copy to not modify the base URL.
		q := u.Query()
		q.Set("host", serverAddr)
		u.RawQuery = q.Encode()
		return u.String()
	}, nil
}

func cmdUsers(ctx context.Context, st *storage.Storage) error {
	userList, err := st.ListUsers(ctx, st.DB)
	if err != nil {
		return err
	}
	for _, userEntry := range userList {
		fmt.Printf("uid=%d,asid=%d: username=%s id=%s server=%s stream=%d\n",
			userEntry.UserState.UID,
			userEntry.AccountState.ASID,
			userEntry.AccountState.Username,
			userEntry.AccountState.AccountID,
			userEntry.AccountState.ServerAddr,
			userEntry.UserState.DefaultStID)
	}
	return nil
}

func cmdMe(ctx context.Context, st *storage.Storage, uid int64, showAccount bool, selfURL string) error {
	fmt.Println("# User ID:", uid)

	userState, err := st.UserState(ctx, st.DB, uid)
	if err != nil {
		return err
	}
	fmt.Println("# Default stream ID:", userState.DefaultStID)
	stid := userState.DefaultStID

	accountState, err := st.AccountStateByUID(ctx, st.DB, uid)
	if err != nil {
		return err
	}
	fmt.Println("# Server address:", accountState.ServerAddr)
	fmt.Println("# Last home status ID:", accountState.LastHomeStatusID)

	redirectURIFunc, err := redirectURIFunc(selfURL)
	if err != nil {
		return err
	}
	serverState, err := st.ServerState(ctx, st.DB, accountState.ServerAddr, redirectURIFunc(accountState.ServerAddr))
	if err != nil {
		return err
	}
	fmt.Println("# Auth URI:", serverState.AuthURI)
	fmt.Println("# Redirect URI:", serverState.RedirectURI)

	streamState, err := st.StreamState(ctx, st.DB, stid)
	if err != nil {
		return err
	}
	fmt.Println("# First position:", streamState.FirstPosition)
	fmt.Println("# Last recorded position:", streamState.LastPosition)
	fmt.Println("# Last read position:", streamState.LastRead)
	fmt.Println("# Remaining in pool:", streamState.Remaining)

	var client *mastodon.Client
	if showAccount {
		client = mastodon.NewClient(&mastodon.Config{
			Server:       serverState.ServerAddr,
			ClientID:     serverState.ClientID,
			ClientSecret: serverState.ClientSecret,
			AccessToken:  accountState.AccessToken,
		})
		fmt.Println("# Mastodon Account")
		account, err := client.GetAccountCurrentUser(ctx)
		if err != nil {
			return err
		}
		spew.Dump(account)
		fmt.Println()
	}
	return nil
}

func cmdServe(_ context.Context, st *storage.Storage, selfURL string, inviteCode string, autoLogin int64, testServer bool) error {
	content, err := frontend.Content()
	if err != nil {
		return err
	}

	sessionManager := scs.New()
	sessionManager.Store = sqlite3store.New(st.DB)
	sessionManager.Lifetime = 90 * 24 * time.Hour
	sessionManager.Cookie.Name = "mastopoof"
	// Need Lax and not Strict for oauth redirections
	// https://stackoverflow.com/a/42220786
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode
	sessionManager.Cookie.Secure = true

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(content)))

	if testServer {
		testserver.New().RegisterOn(mux)
	}

	redirectURIFunc, err := redirectURIFunc(selfURL)
	if err != nil {
		return err
	}

	s := server.New(st, sessionManager, inviteCode, autoLogin, selfURL, redirectURIFunc)

	api := http.NewServeMux()
	api.Handle(mastopoofconnect.NewMastopoofHandler(s))
	mux.Handle("/_rpc/", http.StripPrefix("/_rpc", api))
	mux.HandleFunc("/_redirect", s.RedirectHandler)

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Listening on %s...\n", addr)

	return http.ListenAndServe(addr,
		// Use h2c so we can serve HTTP/2 without TLS.
		h2c.NewHandler(sessionManager.LoadAndSave(mux), &http2.Server{}),
	)
}

func cmdPickNext(ctx context.Context, st *storage.Storage, stid int64) error {
	ost, err := st.PickNext(ctx, stid)
	if err != nil {
		return err
	}
	spew.Dump(ost)
	return nil
}

func cmdSetRead(ctx context.Context, st *storage.Storage, stid int64, position int64) error {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	streamState, err := st.StreamState(ctx, txn, stid)
	if err != nil {
		return err
	}

	fmt.Println("Current position:", streamState.LastRead)
	if position < 0 {
		streamState.LastRead += -position
	} else {
		streamState.LastRead = position
	}
	fmt.Println("New position:", streamState.LastRead)

	if err := st.SetStreamState(ctx, txn, streamState); err != nil {
		return err
	}

	return txn.Commit()
}

func cmdShowOpened(ctx context.Context, st *storage.Storage, stid int64) error {
	opened, err := st.Opened(ctx, stid)
	if err != nil {
		return err
	}

	for _, openStatus := range opened {
		status := openStatus.Status
		subject := strings.ReplaceAll(status.Content[:50], "\n", "  ")
		fmt.Printf("[%d] %s@%v %s...\n", openStatus.Position, status.ID, status.CreatedAt, subject)
	}
	return nil
}

func run(ctx context.Context) error {
	var rootCmd = &cobra.Command{
		Use:   "mastopoof",
		Short: "Mastopoof is a Mastodon client",
		Long:  `More about Mastopoof`,
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "users",
		Short: "List users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer db.Close()

			return cmdUsers(ctx, st)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "clear-app",
		Short: "Remove app registrations from local DB, forcing Mastopoof to recreate them when needed.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer db.Close()

			return st.ClearApp(ctx)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "clear-stream",
		Short: "Remove all statuses from the stream, as if nothing was ever looked at.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer db.Close()

			stid, err := getStreamID(ctx, st)
			if err != nil {
				return err
			}
			glog.Infof("using stream ID %d", stid)

			return st.ClearStream(ctx, stid)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "clear-pool-stream",
		Short: "Remove all statuses from the pool and stream, as if nothing had ever been fetched.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer db.Close()

			uid, err := getUserID(ctx, st)
			if err != nil {
				return err
			}
			return st.ClearPoolAndStream(ctx, uid)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "me",
		Short: "Get information about one's own account.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer db.Close()

			uid, err := getUserID(ctx, st)
			if err != nil {
				return err
			}

			return cmdMe(ctx, st, uid, *showAccount, *selfURL)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Run mastopoof backend server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer db.Close()

			uid, err := getUserID(ctx, st)
			if err != nil {
				return err
			}

			return cmdServe(ctx, st, *selfURL, *inviteCode, uid, false /*testServer*/)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "testserve",
		Short: "Run mastopoof backend server with a fake mastodon server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, "file::memory:?cache=shared")
			if err != nil {
				return err
			}
			defer db.Close()

			serverAddr := fmt.Sprintf("http://localhost:%d", *port)
			userState, err := st.CreateUser(ctx, st.DB, serverAddr, "1234", "testuser1")
			if err != nil {
				return fmt.Errorf("unable to create testuser: %w", err)
			}

			return cmdServe(ctx, st, *selfURL, "", userState.UID, true /*testServer*/)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "pick-next",
		Short: "Add a status to the stream",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer db.Close()

			stid, err := getStreamID(ctx, st)
			if err != nil {
				return err
			}

			return cmdPickNext(ctx, st, stid)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "set-read",
		Short: "Set the already-read pointer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer db.Close()

			stid, err := getStreamID(ctx, st)
			if err != nil {
				return err
			}

			position := int64(-1)
			if len(args) > 0 {
				position, err = strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return err
				}
			}
			return cmdSetRead(ctx, st, stid, position)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "show-opened",
		Short: "List currently opened statuses (picked & not read)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, db, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer db.Close()

			stid, err := getStreamID(ctx, st)
			if err != nil {
				return err
			}

			return cmdShowOpened(ctx, st, stid)
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
