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
	listingID  = flag.Int64("listing_id", 1, "Listing to use")
)

func registerApp(ctx context.Context, ai *storage.AuthInfo) (*mastodon.Application, error) {
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

func cmdAuth(ctx context.Context, st *storage.Storage) error {
	txn, err := st.DB.BeginTx(ctx, nil)
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

func cmdClearAll(ctx context.Context, st *storage.Storage) error {
	return st.ClearAll(ctx)
}

func cmdClearStream(ctx context.Context, st *storage.Storage) error {
	lid := *listingID
	return st.ClearStream(ctx, lid)
}

func cmdMe(ctx context.Context, st *storage.Storage, authInfo *storage.AuthInfo, client *mastodon.Client) error {
	lid := *listingID

	if client != nil {
		fmt.Println("# Mastodon Account")
		account, err := client.GetAccountCurrentUser(ctx)
		if err != nil {
			return err
		}
		spew.Dump(account)
		fmt.Println()
	}

	lastPosition, err := st.LastPosition(ctx, lid, st.DB)
	if err != nil {
		return err
	}
	fmt.Println("# Position of last status in stream:", lastPosition)

	listingState, err := st.ListingState(ctx, st.DB, lid)
	if err != nil {
		return err
	}
	fmt.Println("# Last read position:", listingState.LastRead)
	return nil
}

func cmdFetch(ctx context.Context, st *storage.Storage, authInfo *storage.AuthInfo, client *mastodon.Client) error {
	txn, err := st.DB.BeginTx(ctx, nil)
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
			if storage.IDNewer(status.ID, userState.LastHomeStatusID) {
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

func cmdServe(ctx context.Context, st *storage.Storage, authInfo *storage.AuthInfo) error {
	mux := http.NewServeMux()

	s := server.New(st, authInfo)

	api := http.NewServeMux()
	api.Handle(mastopoofconnect.NewMastopoofHandler(s))
	mux.Handle("/_rpc/", http.StripPrefix("/_rpc", api))

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Listening on %s...\n", addr)

	return http.ListenAndServe(addr,
		// Use h2c so we can serve HTTP/2 without TLS.
		h2c.NewHandler(mux, &http2.Server{}),
	)
}

func cmdList(ctx context.Context, st *storage.Storage, authInfo *storage.AuthInfo) error {
	rows, err := st.DB.QueryContext(ctx, `SELECT status FROM statuses WHERE uid = ?`, authInfo.UID)
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

func cmdDumpStatus(ctx context.Context, st *storage.Storage, authInfo *storage.AuthInfo, args []string) error {
	rows, err := st.DB.QueryContext(ctx, `SELECT status FROM statuses WHERE uid = ?`, authInfo.UID)
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

func cmdNewListing(ctx context.Context, st *storage.Storage, authInfo *storage.AuthInfo) error {
	txn, err := st.DB.BeginTx(ctx, nil)
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

	listing := &storage.ListingState{
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
	listing, err = st.ListingState(ctx, st.DB, lid)
	if err != nil {
		return err
	}
	spew.Dump(listing)
	return nil
}

func cmdPickNext(ctx context.Context, st *storage.Storage, authInfo *storage.AuthInfo) error {
	lid := *listingID

	ost, err := st.PickNext(ctx, lid)
	if err != nil {
		return err
	}
	spew.Dump(ost)
	return nil
}

func cmdSetRead(ctx context.Context, st *storage.Storage, authInfo *storage.AuthInfo, position int64) error {
	lid := *listingID

	txn, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txn.Rollback()

	listingState, err := st.ListingState(ctx, txn, lid)
	if err != nil {
		return err
	}

	fmt.Println("Current position:", listingState.LastRead)
	if position < 0 {
		listingState.LastRead += -position
	} else {
		listingState.LastRead = position
	}
	fmt.Println("New position:", listingState.LastRead)

	if err := st.SetListingState(ctx, txn, listingState); err != nil {
		return err
	}

	return txn.Commit()
}

func cmdShowOpened(ctx context.Context, st *storage.Storage, authInfo *storage.AuthInfo) error {
	opened, err := st.Opened(ctx, *listingID)
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
		ai, err := st.AuthInfo(ctx, st.DB)
		if err != nil {
			return err
		}
		var client *mastodon.Client
		if *showAccount {
			client = mastodon.NewClient(&mastodon.Config{
				Server:       ai.ServerAddr,
				ClientID:     ai.ClientID,
				ClientSecret: ai.ClientSecret,
				AccessToken:  ai.AccessToken,
			})
		}
		return cmdMe(ctx, st, ai, client)
	}
	rootCmd.AddCommand(cmdMeDef)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "fetch",
		Short: "Fetch recent home content and add it to the DB.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.DB)
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
			ai, err := st.AuthInfo(ctx, st.DB)
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
			ai, err := st.AuthInfo(ctx, st.DB)
			if err != nil {
				return err
			}
			return cmdList(ctx, st, ai)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "dump-status",
		Short: "Display one status, identified by ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.DB)
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
			ai, err := st.AuthInfo(ctx, st.DB)
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
			ai, err := st.AuthInfo(ctx, st.DB)
			if err != nil {
				return err
			}
			return cmdPickNext(ctx, st, ai)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "set-read",
		Short: "Set the already-read pointer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.DB)
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
			return cmdSetRead(ctx, st, ai, position)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "show-opened",
		Short: "List currently opened statuses (picked & not read)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ai, err := st.AuthInfo(ctx, st.DB)
			if err != nil {
				return err
			}
			return cmdShowOpened(ctx, st, ai)
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
