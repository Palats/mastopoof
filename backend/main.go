package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/c-bata/go-prompt"
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
	testData    = flag.String("testdata", "testdata", "Directory with backend testdata, for testserve")
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

func cmdUsers(ctx context.Context, st *storage.Storage) error {
	userList, err := st.ListUsers(ctx)
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

func cmdMe(ctx context.Context, st *storage.Storage, uid storage.UID, showAccount bool) error {
	fmt.Println("# User ID:", uid)

	userState, err := st.UserState(ctx, nil, uid)
	if err != nil {
		return err
	}
	fmt.Println("# Default stream ID:", userState.DefaultStID)
	stid := userState.DefaultStID

	accountState, err := st.AccountStateByUID(ctx, nil, uid)
	if err != nil {
		return err
	}
	fmt.Println("# Server address:", accountState.ServerAddr)
	fmt.Println("# Last home status ID:", accountState.LastHomeStatusID)

	appRegState, err := st.AppRegState(ctx, nil, accountState.ServerAddr)
	if err != nil {
		return err
	}
	fmt.Println("# Auth URI:", appRegState.AuthURI)
	fmt.Println("# Redirect URI:", appRegState.RedirectURI)

	streamState, err := st.StreamState(ctx, nil, stid)
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
			Server:       appRegState.ServerAddr,
			ClientID:     appRegState.ClientID,
			ClientSecret: appRegState.ClientSecret,
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

func cmdPickNext(ctx context.Context, st *storage.Storage, stid storage.StID) error {
	ost, err := st.PickNext(ctx, stid)
	if err != nil {
		return err
	}
	spew.Dump(ost)
	return nil
}

func cmdSetRead(ctx context.Context, st *storage.Storage, stid storage.StID, position int64) error {
	return st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
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

		return st.SetStreamState(ctx, txn, streamState)
	})
}

func cmdCheckStreamState(ctx context.Context, st *storage.Storage, stid storage.StID) error {
	return st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
		// Stream content - check for duplicates
		fmt.Println("### Duplicate statuses in stream")
		if err := st.FixDuplicateStatuses(ctx, txn, stid); err != nil {
			return err
		}
		fmt.Println()

		// Check cross user statuses
		fmt.Println("### Statuses from another user")
		if err := st.FixCrossStatuses(ctx, txn, stid); err != nil {
			return err
		}
		fmt.Println()

		// Stream state
		dbStreamState, err := st.StreamState(ctx, txn, stid)
		if err != nil {
			return fmt.Errorf("unable to get streamstate from DB: %w", err)
		}

		fmt.Println("### Stream state in database")
		fmt.Println("Stream ID:", dbStreamState.StID)
		fmt.Println("First position:", dbStreamState.FirstPosition)
		fmt.Println("Last position:", dbStreamState.LastPosition)
		fmt.Println("Remaining:", dbStreamState.Remaining)
		fmt.Println("Last read:", dbStreamState.LastRead)
		fmt.Println()

		computeStreamState, err := st.RecomputeStreamState(ctx, txn, stid)
		if err != nil {
			return fmt.Errorf("unable to calculate streamstate: %w", err)
		}

		fmt.Println("### Calculated stream state")
		fmt.Println("Stream ID:", computeStreamState.StID)
		fmt.Printf("First position: %d [diff: %+d]\n", computeStreamState.FirstPosition, computeStreamState.FirstPosition-dbStreamState.FirstPosition)
		fmt.Printf("Last position: %d [diff: %+d]\n", computeStreamState.LastPosition, computeStreamState.LastPosition-dbStreamState.LastPosition)
		fmt.Printf("Remaining: %d [diff: %+d]\n", computeStreamState.Remaining, computeStreamState.Remaining-dbStreamState.Remaining)
		fmt.Printf("Last read: %d [diff: %+d]\n", computeStreamState.LastRead, computeStreamState.LastRead-dbStreamState.LastRead)
		fmt.Println()

		// Do the fix in the transaction - transaction won't be committed in dry run.
		dbStreamState.FirstPosition = computeStreamState.FirstPosition
		dbStreamState.LastPosition = computeStreamState.LastPosition
		dbStreamState.Remaining = computeStreamState.Remaining
		dbStreamState.LastRead = computeStreamState.LastRead
		if err := st.SetStreamState(ctx, txn, dbStreamState); err != nil {
			return fmt.Errorf("failed to update stream state: %w", err)
		}

		if *doFix {
			fmt.Println("Applying changes in DB...")
			return nil
		}

		fmt.Println("Dry run, ignoring changes")
		return storage.ErrCleanAbortTxn
	})
}

var spaces = regexp.MustCompile(`\s+`)

func cmdTestServe(ctx context.Context) error {
	st, err := getStorage(ctx, "file::memory:?cache=shared")
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

	testDataFS := os.DirFS(*testData)
	ts := testserver.New()
	if err := ts.AddJSONStatuses(testDataFS); err != nil {
		return err
	}
	ts.RegisterOn(mux)

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Listening on %s...\n", addr)
	go func() {
		err := http.ListenAndServe(addr, h2c.NewHandler(mux, &http2.Server{}))
		glog.Exit(err)
	}()

	// Everything is started, let's have a prompt to allow to fiddle with
	// the test server.

	fmt.Println()
	fmt.Println("<tab> to see command list")

	executor := func(text string) {
		glog.Infof("prompt input: %v", text)
		var cmds [][]string
		// Support multiple commands separated by semi-colon.
		for _, sub := range strings.Split(text, ";") {
			// Remove comments.
			if idx := strings.Index(sub, "#"); idx >= 0 {
				sub = sub[:idx]
			}
			var words []string
			for _, w := range spaces.Split(sub, -1) {
				if w != "" {
					words = append(words, w)
				}
			}
			if len(words) > 0 {
				cmds = append(cmds, words)
			}
		}

		for _, words := range cmds {
			cmd := words[0]
			args := words[1:]
			switch cmd {
			case "fake-statuses":
				if len(args) > 1 {
					fmt.Printf("At most one parameter allowed")
					break
				}
				count := int64(10)
				if len(args) > 0 {
					count, err = strconv.ParseInt(args[0], 10, 64)
					if err != nil {
						fmt.Printf("unable to parse %s: %v", args[0], err)
						break
					}
				}
				for i := int64(0); i < count; i++ {
					ts.AddFakeStatus()
				}
				fmt.Printf("Added %d fake statuses.\n", count)
			case "set-list-delay":
				if len(args) != 1 {
					fmt.Printf("One parameter needed to specify the delay, as Go ParseDuration format (e.g., '3s').")
					break
				}
				d, err := time.ParseDuration(args[0])
				if err != nil {
					fmt.Printf("Invalid duration: %v", err)
					break
				}
				ts.SetListDelay(d)
			case "exit":
				if len(args) > 0 {
					fmt.Printf("'exit' does not take parameters")
					break
				}
				glog.Exit(0)
			default:
				fmt.Printf("Unknown command %q\n", cmd)
			}
		}

	}
	completer := func(d prompt.Document) []prompt.Suggest {
		s := []prompt.Suggest{
			{Text: "fake-statuses", Description: "Add fake statuses; opt: number of statuses"},
			{Text: "set-list-delay", Description: "Introduce delay when listing statuses from Mastodon"},
			{Text: "exit", Description: "Shutdown"},
		}
		return prompt.FilterHasPrefix(s, d.GetWordBeforeCursor(), true)
	}
	p := prompt.New(
		executor,
		completer,
		prompt.OptionPrefix(">>> "),
		prompt.OptionAddKeyBind(prompt.KeyBind{
			Key: prompt.ControlC,
			Fn:  func(b *prompt.Buffer) { os.Exit(0) },
		}),
	)
	p.Run()
	return errors.New("prompt has exited")
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

			return cmdUsers(ctx, st)
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

			return cmdMe(ctx, st, uid, *showAccount)
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
			return cmdTestServe(ctx)
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

			return cmdPickNext(ctx, st, stid)
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
			return cmdSetRead(ctx, st, stid, position)
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

			return cmdCheckStreamState(ctx, st, stid)
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
