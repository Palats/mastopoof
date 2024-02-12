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
	"strconv"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/davecgh/go-spew/spew"
	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/Palats/mastopoof/backend/server"
	"github.com/Palats/mastopoof/backend/storage"

	"github.com/Palats/mastopoof/proto/gen/mastopoof/mastopoofconnect"
	_ "github.com/mattn/go-sqlite3"
)

var _ = spew.Sdump("")

var (
	serverAddr = flag.String("server", "", "Mastodon server to track. Only needed when authenticating.")
	clearApp   = flag.Bool("clear_app", false, "Force re-registration of the app against the Mastodon server")
	clearAuth  = flag.Bool("clear_auth", false, "Force re-approval of auth; does not touch app registration")
	port       = flag.Int("port", 8079, "Port to listen on for the 'serve' command")
	userID     = flag.Int64("uid", 0, "User ID to use for commands. Default to use 1 for read-only commands.")
	streamID   = flag.Int64("stream_id", 1, "Stream to use")
)

func getStorage(ctx context.Context) (*storage.Storage, *sql.DB, error) {
	filename := "./mastopoof.db"
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

func cmdInfo(ctx context.Context, st *storage.Storage) error {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	as, err := st.AccountStateByUID(ctx, txn, *userID)
	if err != nil {
		return err
	}

	fmt.Println("Local account UID:", *userID)
	fmt.Println("Server address:", as.ServerAddr)
	fmt.Println("Last home status ID:", as.LastHomeStatusID)

	// Should be readonly.
	return txn.Commit()
}

func cmdAuth(ctx context.Context, st *storage.Storage) error {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	// User state
	us, err := st.UserState(ctx, txn, *userID)
	if errors.Is(err, storage.ErrNotFound) {
		us, err = st.CreateUserState(ctx, txn)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Server state
	// TODO: make it possible to read existing server name when re-authenticating.
	addr := *serverAddr
	if addr == "" {
		return errors.New("missing --server")
	}
	if !strings.HasPrefix(addr, "https://") {
		return fmt.Errorf("server address %q must start with https://", addr)
	}

	ss, err := st.ServerState(ctx, txn, addr)
	if errors.Is(err, storage.ErrNotFound) {
		ss, err = st.CreateServerState(ctx, txn, addr)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Account (Mastodon) state
	as, err := st.AccountStateByUID(ctx, txn, us.UID)
	if errors.Is(err, storage.ErrNotFound) {
		as, err = st.CreateAccountState(ctx, txn, us.UID, ss.ServerAddr)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// First, make sure that mastopoof is registered on the server.
	if ss.ClientID == "" || *clearApp {
		glog.Infof("registering app")
		app, err := mastodon.RegisterApp(ctx, &mastodon.AppConfig{
			Server:     ss.ServerAddr,
			ClientName: "mastopoof",
			Scopes:     "read",
			Website:    "https://github.com/Palats/mastopoof",
		})
		if err != nil {
			return fmt.Errorf("unable to register app on server %s: %w", ss.ServerAddr, err)
		}
		ss.ClientID = app.ClientID
		ss.ClientSecret = app.ClientSecret
		ss.AuthURI = app.AuthURI
		ss.RedirectURI = app.RedirectURI

		if err := st.SetServerState(ctx, txn, ss); err != nil {
			return err
		}
	}

	// Then, get a user token.
	if as.AccessToken == "" || *clearAuth || *clearApp {
		glog.Infof("need user code")

		fmt.Printf("Auth URL: %s\n", ss.AuthURI)

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
			Server:       ss.ServerAddr,
			ClientID:     ss.ClientID,
			ClientSecret: ss.ClientSecret,
		})
		err = client.AuthenticateToken(ctx, authCode, ss.RedirectURI)
		if err != nil {
			return fmt.Errorf("unable to authenticate on server %s: %w", as.ServerAddr, err)
		}

		as.AccessToken = client.Config.AccessToken
		if err := st.SetAccountState(ctx, txn, as); err != nil {
			return err
		}
	} else {
		fmt.Println("Already authentified.")
	}

	return txn.Commit()
}

func cmdClearAll(ctx context.Context, st *storage.Storage) error {
	return st.ClearAll(ctx)
}

func cmdClearStream(ctx context.Context, st *storage.Storage) error {
	stid := *streamID
	return st.ClearStream(ctx, stid)
}

func cmdMe(ctx context.Context, st *storage.Storage, client *mastodon.Client) error {
	stid := *streamID

	if client != nil {
		fmt.Println("# Mastodon Account")
		account, err := client.GetAccountCurrentUser(ctx)
		if err != nil {
			return err
		}
		spew.Dump(account)
		fmt.Println()
	}

	lastPosition, err := st.LastPosition(ctx, stid, st.DB)
	if err != nil {
		return err
	}
	fmt.Println("# Position of last status in stream:", lastPosition)

	streamState, err := st.StreamState(ctx, st.DB, stid)
	if err != nil {
		return err
	}
	fmt.Println("# First position:", streamState.FirstPosition)
	fmt.Println("# Last position:", streamState.LastPosition)
	fmt.Println("# Last read position:", streamState.LastRead)
	fmt.Println("# Remaining in pool:", streamState.Remaining)
	return nil
}

func cmdFetch(ctx context.Context, st *storage.Storage, accountState *storage.AccountState, client *mastodon.Client) error {
	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

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
			MinID: accountState.LastHomeStatusID,
		}
		glog.Infof("Fetching from %s", pg.MinID)
		timeline, err := client.GetTimelineHome(ctx, pg)
		if err != nil {
			return err
		}
		glog.Infof("Found %d new status on home timeline", len(timeline))

		for _, status := range timeline {
			if storage.IDNewer(status.ID, accountState.LastHomeStatusID) {
				accountState.LastHomeStatusID = status.ID
			}

			jsonString, err := json.Marshal(status)
			if err != nil {
				return err
			}
			stmt := `INSERT INTO statuses(uid, uri, status) VALUES(?, ?, ?)`
			_, err = txn.ExecContext(ctx, stmt, *userID, status.URI, jsonString)
			if err != nil {
				return err
			}
		}

		fetchCount++
		// Pagination got updated.
		if pg.MinID != accountState.LastHomeStatusID {
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

	if err := st.SetAccountState(ctx, txn, accountState); err != nil {
		return err
	}

	return txn.Commit()
}

func cmdServe(ctx context.Context, st *storage.Storage) error {
	sessionManager := scs.New()
	sessionManager.Lifetime = 24 * time.Hour

	mux := http.NewServeMux()

	s := server.New(st, sessionManager)

	api := http.NewServeMux()
	api.Handle(mastopoofconnect.NewMastopoofHandler(s))
	mux.Handle("/_rpc/", http.StripPrefix("/_rpc", api))

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Listening on %s...\n", addr)

	return http.ListenAndServe(addr,
		// Use h2c so we can serve HTTP/2 without TLS.
		h2c.NewHandler(sessionManager.LoadAndSave(mux), &http2.Server{}),
	)
}

func cmdList(ctx context.Context, st *storage.Storage) error {
	rows, err := st.DB.QueryContext(ctx, `SELECT status FROM statuses WHERE uid = ?`, *userID)
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

func cmdDumpStatus(ctx context.Context, st *storage.Storage, args []string) error {
	rows, err := st.DB.QueryContext(ctx, `SELECT status FROM statuses WHERE uid = ?`, *userID)
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

func cmdNewStream(ctx context.Context, st *storage.Storage) error {
	stid, err := st.NewStream(ctx, *userID)
	if err != nil {
		return err
	}

	// And now, re-read it to output it.
	stream, err := st.StreamState(ctx, st.DB, stid)
	if err != nil {
		return err
	}
	spew.Dump(stream)
	return nil
}

func cmdPickNext(ctx context.Context, st *storage.Storage) error {
	stid := *streamID

	ost, err := st.PickNext(ctx, stid)
	if err != nil {
		return err
	}
	spew.Dump(ost)
	return nil
}

func cmdSetRead(ctx context.Context, st *storage.Storage, position int64) error {
	stid := *streamID

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

func cmdShowOpened(ctx context.Context, st *storage.Storage) error {
	opened, err := st.Opened(ctx, *streamID)
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
		Use:   "clear-all",
		Short: "Nuke all state in the DB, except for auth against Mastodon server.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdClearAll(ctx, st)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "clear-stream",
		Short: "Remove all statuses from the stream, as if nothing was ever looked at.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdClearStream(ctx, st)
		},
	})

	cmdMeDef := &cobra.Command{
		Use:   "me",
		Short: "Get information about one's own account.",
		Args:  cobra.NoArgs,
	}
	showAccount := cmdMeDef.PersistentFlags().Bool("account", false, "Query and show account state from Mastodon server")
	cmdMeDef.RunE = func(cmd *cobra.Command, args []string) error {
		as, err := st.AccountStateByUID(ctx, st.DB, *userID)
		if err != nil {
			return err
		}
		ss, err := st.ServerState(ctx, st.DB, as.ServerAddr)
		if err != nil {
			return err
		}
		var client *mastodon.Client
		if *showAccount {
			client = mastodon.NewClient(&mastodon.Config{
				Server:       ss.ServerAddr,
				ClientID:     ss.ClientID,
				ClientSecret: ss.ClientSecret,
				AccessToken:  as.AccessToken,
			})
		}
		return cmdMe(ctx, st, client)
	}
	rootCmd.AddCommand(cmdMeDef)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "fetch",
		Short: "Fetch recent home content and add it to the DB.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			as, err := st.AccountStateByUID(ctx, st.DB, *userID)
			if err != nil {
				return err
			}
			ss, err := st.ServerState(ctx, st.DB, as.ServerAddr)
			if err != nil {
				return err
			}
			client := mastodon.NewClient(&mastodon.Config{
				Server:       ss.ServerAddr,
				ClientID:     ss.ClientID,
				ClientSecret: ss.ClientSecret,
				AccessToken:  as.AccessToken,
			})
			return cmdFetch(ctx, st, as, client)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Run mastopoof backend server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdServe(ctx, st)

		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Get list of known statuses",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdList(ctx, st)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "dump-status",
		Short: "Display one status, identified by ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdDumpStatus(ctx, st, args)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "new-stream",
		Short: "Create a new empty stream.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdNewStream(ctx, st)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "pick-next",
		Short: "Add a status to the stream",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdPickNext(ctx, st)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "set-read",
		Short: "Set the already-read pointer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			position := int64(-1)
			if len(args) > 0 {
				position, err = strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return err
				}
			}
			return cmdSetRead(ctx, st, position)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "show-opened",
		Short: "List currently opened statuses (picked & not read)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdShowOpened(ctx, st)
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
