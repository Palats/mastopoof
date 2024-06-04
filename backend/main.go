package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strconv"

	"github.com/davecgh/go-spew/spew"
	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/Palats/mastopoof/backend/cmds"
	"github.com/Palats/mastopoof/backend/server"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/Palats/mastopoof/frontend"

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
	testData    = flag.String("testdata", "localtestdata", "Directory with backend testdata, for testserve")
	doFix       = flag.Bool("fix", false, "If set, update streamstate based on computed value.")
	insecure    = flag.Bool("insecure", false, "If true, mark cookies as insecure, allowing serving without https")
)

const appMastodonScopes = "read write push"

func getStorage(ctx context.Context, filename string) (*storage.Storage, error) {
	if filename == "" {
		return nil, fmt.Errorf("missing database filename; try specifying --db <filename>")
	}

	st, err := storage.NewStorage(ctx, filename, *selfURL, appMastodonScopes)
	if err != nil {
		return nil, err
	}
	return st, nil
}

func getUserID(_ context.Context, _ *storage.Storage) (storage.UID, error) {
	return storage.UID(*userID), nil
}

func getStreamID(ctx context.Context, st *storage.Storage) (storage.StID, error) {
	if *streamID != 0 {
		return storage.StID(*streamID), nil
	}
	if *userID != 0 {
		userState, err := st.UserState(ctx, nil, storage.UID(*userID))
		if err != nil {
			return 0, err
		}
		return userState.DefaultStID, nil
	}
	return 0, errors.New("no streamID / user ID specified")
}

func getMux(st *storage.Storage, autoLogin storage.UID) (*http.ServeMux, error) {
	mux := http.NewServeMux()

	// Serve frontend content (html, js, etc.).
	content, err := frontend.Content()
	if err != nil {
		return nil, err
	}
	mux.Handle("/", http.FileServer(http.FS(content)))

	// Run the backend RPC server.
	sessionManager := server.NewSessionManager(st)
	if *insecure {
		sessionManager.Cookie.Secure = false
	}
	s := server.New(st, sessionManager, *inviteCode, autoLogin, *selfURL, appMastodonScopes)
	s.RegisterOn(mux)
	return mux, nil
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
			st, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer st.Close()

			return cmds.CmdUsers(ctx, st)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "clear-app",
		Short: "Remove app registrations from local DB, forcing Mastopoof to recreate them when needed.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer st.Close()

			return st.ClearApp(ctx)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "clear-stream",
		Short: "Remove all statuses from the stream, as if nothing was ever looked at.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer st.Close()

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
			st, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer st.Close()

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
			st, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer st.Close()

			uid, err := getUserID(ctx, st)
			if err != nil {
				return err
			}

			return cmds.CmdMe(ctx, st, uid, *showAccount)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Run mastopoof backend server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer st.Close()

			uid, err := getUserID(ctx, st)
			if err != nil {
				return err
			}

			mux, err := getMux(st, uid)
			if err != nil {
				return err
			}
			addr := fmt.Sprintf(":%d", *port)
			fmt.Printf("Listening on %s...\n", addr)
			return http.ListenAndServe(addr, h2c.NewHandler(mux, &http2.Server{}))
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "testserve",
		Short: "Run mastopoof backend server with a fake mastodon server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStorage(ctx, ":memory:")
			if err != nil {
				return err
			}
			defer st.Close()

			serverAddr := fmt.Sprintf("http://localhost:%d", *port)

			_, err = st.CreateAppRegState(ctx, nil, serverAddr)
			if err != nil {
				return fmt.Errorf("unable to create server state: %w", err)
			}

			userState, _, _, err := st.CreateUser(ctx, nil, serverAddr, "1234", "testuser1")
			if err != nil {
				return fmt.Errorf("unable to create testuser: %w", err)
			}

			mux, err := getMux(st, userState.UID)
			if err != nil {
				return err
			}

			return cmds.CmdTestServe(ctx, st, mux, *port, *testData)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "pick-next",
		Short: "Add a status to the stream",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer st.Close()

			stid, err := getStreamID(ctx, st)
			if err != nil {
				return err
			}

			return cmds.CmdPickNext(ctx, st, stid)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "set-read",
		Short: "Set the already-read pointer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer st.Close()

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
			return cmds.CmdSetRead(ctx, st, stid, position)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "check-stream-state",
		Short: "Compare stream state values to its theoritical values.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStorage(ctx, *dbFilename)
			if err != nil {
				return err
			}
			defer st.Close()

			stid, err := getStreamID(ctx, st)
			if err != nil {
				return err
			}

			return cmds.CmdCheckStreamState(ctx, st, stid, *doFix)
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
