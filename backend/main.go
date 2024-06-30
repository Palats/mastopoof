package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"

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

func FlagPort(fs *pflag.FlagSet) *int {
	return fs.Int("port", 8079, "Port to listen on for the 'serve' command")
}

func FlagUserID(fs *pflag.FlagSet) *storage.UID {
	return (*storage.UID)(fs.Int64("uid", 0, "User ID to use for commands. With 'serve', will auto login that user."))
}

func FlagStreamID(fs *pflag.FlagSet) *storage.StID {
	return (*storage.StID)(fs.Int64("stream_id", 0, "Stream to use"))
}

func FlagDBFilename(fs *pflag.FlagSet) *string {
	return fs.String("db", "", "SQLite file")
}

func FlagSelfURL(fs *pflag.FlagSet) *string {
	return fs.String("self_url", "", "URL to use for authentication redirection on the frontend. When empty, uses out-of-band auth.")
}
func FlagInviteCode(fs *pflag.FlagSet) *string {
	return fs.String("invite_code", "", "If not empty, users can only be created by providing this code.")
}
func FlagInsecure(fs *pflag.FlagSet) *bool {
	return fs.Bool("insecure", false, "If true, mark cookies as insecure, allowing serving without https")
}

func getStreamID(ctx context.Context, st *storage.Storage, streamID storage.StID, userID storage.UID) (storage.StID, error) {
	if streamID != 0 {
		return streamID, nil
	}
	if userID != 0 {
		userState, err := st.UserState(ctx, nil, userID)
		if err != nil {
			return 0, err
		}
		return userState.DefaultStID, nil
	}
	return 0, errors.New("no streamID / user ID specified")
}

func getMux(st *storage.Storage, autoLogin storage.UID, inviteCode string, insecure bool, selfURL string) (*http.ServeMux, error) {
	mux := http.NewServeMux()

	// Serve frontend content (html, js, etc.).
	content, err := frontend.Content()
	if err != nil {
		return nil, err
	}
	mux.Handle("/", http.FileServer(http.FS(content)))

	// Run the backend RPC server.
	sessionManager := server.NewSessionManager(st)
	if insecure {
		sessionManager.Cookie.Secure = false
	}

	u, err := url.Parse(selfURL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse self URL %q: %w", selfURL, err)
	}

	s := server.New(st, sessionManager, inviteCode, autoLogin, server.AppMastodonScopes, u)
	s.RegisterOn(mux)
	return mux, nil
}

func cmdUsers() *cobra.Command {
	c := &cobra.Command{
		Use:   "users",
		Short: "List users",
		Args:  cobra.NoArgs,
	}
	dbFilename := FlagDBFilename(c.PersistentFlags())
	c.MarkPersistentFlagRequired("db")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		st, err := storage.NewStorage(ctx, *dbFilename)
		if err != nil {
			return err
		}
		defer st.Close()
		return cmds.CmdUsers(ctx, st)
	}
	return c
}

func cmdClearApp() *cobra.Command {
	c := &cobra.Command{
		Use:   "clear-app",
		Short: "Remove app registrations from local DB, forcing Mastopoof to recreate them when needed.",
		Args:  cobra.NoArgs,
	}
	dbFilename := FlagDBFilename(c.PersistentFlags())
	c.MarkPersistentFlagRequired("db")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		st, err := storage.NewStorage(ctx, *dbFilename)
		if err != nil {
			return err
		}
		defer st.Close()
		return st.ClearApp(ctx)
	}
	return c
}

func cmdClearStream() *cobra.Command {
	c := &cobra.Command{
		Use:   "clear-stream",
		Short: "Remove all statuses from the stream, as if nothing was ever looked at.",
		Args:  cobra.NoArgs,
	}
	dbFilename := FlagDBFilename(c.PersistentFlags())
	c.MarkPersistentFlagRequired("db")
	userID := FlagUserID(c.PersistentFlags())
	streamID := FlagStreamID(c.PersistentFlags())
	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		st, err := storage.NewStorage(ctx, *dbFilename)
		if err != nil {
			return err
		}
		defer st.Close()

		stid, err := getStreamID(ctx, st, *streamID, *userID)
		if err != nil {
			return err
		}
		glog.Infof("using stream ID %d", stid)
		return st.ClearStream(ctx, stid)
	}
	return c
}

func cmdClearPoolStream() *cobra.Command {
	c := &cobra.Command{
		Use:   "clear-pool-stream",
		Short: "Remove all statuses from the pool and stream, as if nothing had ever been fetched.",
		Args:  cobra.NoArgs,
	}
	dbFilename := FlagDBFilename(c.PersistentFlags())
	c.MarkPersistentFlagRequired("db")
	userID := FlagUserID(c.PersistentFlags())
	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		st, err := storage.NewStorage(ctx, *dbFilename)
		if err != nil {
			return err
		}
		defer st.Close()

		return st.ClearPoolAndStream(ctx, *userID)
	}
	return c
}

func cmdMe() *cobra.Command {
	c := &cobra.Command{
		Use:   "me",
		Short: "Get information about one's own account.",
		Args:  cobra.NoArgs,
	}
	dbFilename := FlagDBFilename(c.PersistentFlags())
	c.MarkPersistentFlagRequired("db")
	userID := FlagUserID(c.PersistentFlags())
	showAccount := c.PersistentFlags().Bool("show_account", false, "Query and show account state from Mastodon server")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		st, err := storage.NewStorage(ctx, *dbFilename)
		if err != nil {
			return err
		}
		defer st.Close()

		return cmds.CmdMe(ctx, st, *userID, *showAccount)
	}
	return c
}

func cmdServe() *cobra.Command {
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run mastopoof backend server",
		Args:  cobra.NoArgs,
	}
	dbFilename := FlagDBFilename(c.PersistentFlags())
	c.MarkPersistentFlagRequired("db")
	selfURL := FlagSelfURL(c.PersistentFlags())
	port := FlagPort(c.PersistentFlags())
	userID := FlagUserID(c.PersistentFlags())
	inviteCode := FlagInviteCode(c.PersistentFlags())
	insecure := FlagInsecure(c.PersistentFlags())

	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		st, err := storage.NewStorage(ctx, *dbFilename)
		if err != nil {
			return err
		}
		defer st.Close()

		mux, err := getMux(st, *userID, *inviteCode, *insecure, *selfURL)
		if err != nil {
			return err
		}
		addr := fmt.Sprintf(":%d", *port)
		fmt.Printf("Listening on %s...\n", addr)
		return http.ListenAndServe(addr, h2c.NewHandler(mux, &http2.Server{}))
	}
	return c
}

func cmdTestServe() *cobra.Command {
	c := &cobra.Command{
		Use:   "testserve",
		Short: "Run mastopoof backend server with a fake mastodon server",
		Args:  cobra.NoArgs,
	}
	selfURL := FlagSelfURL(c.PersistentFlags())
	port := FlagPort(c.PersistentFlags())
	inviteCode := FlagInviteCode(c.PersistentFlags())
	insecure := FlagInsecure(c.PersistentFlags())
	testData := c.PersistentFlags().String("testdata", "localtestdata", "Directory with backend testdata, for testserve")

	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		serverAddr := fmt.Sprintf("http://localhost:%d", *port)
		parsedSelfURL, err := url.Parse(*selfURL)
		if err != nil {
			return fmt.Errorf("unable to parse self URL %q: %w", *selfURL, err)
		}
		appRefInfo := server.NewAppRegInfo(serverAddr, parsedSelfURL, server.AppMastodonScopes)

		st, err := storage.NewStorage(ctx, ":memory:")
		if err != nil {
			return err
		}
		defer st.Close()

		_, err = st.CreateAppRegState(ctx, nil, appRefInfo)
		if err != nil {
			return fmt.Errorf("unable to create server state: %w", err)
		}

		userState, _, _, err := st.CreateUser(ctx, nil, serverAddr, "1234", "testuser1")
		if err != nil {
			return fmt.Errorf("unable to create testuser: %w", err)
		}

		mux, err := getMux(st, userState.UID, *inviteCode, *insecure, *selfURL)
		if err != nil {
			return err
		}

		return cmds.NewTestServe(mux, *port, os.DirFS(*testData)).Run(ctx)
	}
	return c
}

func cmdPickNext() *cobra.Command {
	c := &cobra.Command{
		Use:   "pick-next",
		Short: "Add a status to the stream",
		Args:  cobra.NoArgs,
	}
	dbFilename := FlagDBFilename(c.PersistentFlags())
	c.MarkPersistentFlagRequired("db")
	userID := FlagUserID(c.PersistentFlags())
	streamID := FlagStreamID(c.PersistentFlags())
	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		st, err := storage.NewStorage(ctx, *dbFilename)
		if err != nil {
			return err
		}
		defer st.Close()

		stid, err := getStreamID(ctx, st, *streamID, *userID)
		if err != nil {
			return err
		}

		return cmds.CmdPickNext(ctx, st, stid)
	}
	return c
}

func cmdSetRead() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-read",
		Short: "Set the already-read pointer",
		Args:  cobra.MaximumNArgs(1),
	}
	dbFilename := FlagDBFilename(c.PersistentFlags())
	c.MarkPersistentFlagRequired("db")
	userID := FlagUserID(c.PersistentFlags())
	streamID := FlagStreamID(c.PersistentFlags())

	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		st, err := storage.NewStorage(ctx, *dbFilename)
		if err != nil {
			return err
		}
		defer st.Close()

		stid, err := getStreamID(ctx, st, *streamID, *userID)
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
	}
	return c
}

func CmdCheckStreamState() *cobra.Command {
	c := &cobra.Command{
		Use:   "check-stream-state",
		Short: "Compare stream state values to its theoritical values.",
		Args:  cobra.NoArgs,
	}
	dbFilename := FlagDBFilename(c.PersistentFlags())
	c.MarkPersistentFlagRequired("db")
	userID := FlagUserID(c.PersistentFlags())
	streamID := FlagStreamID(c.PersistentFlags())
	doFix := c.PersistentFlags().Bool("fix", false, "If set, update streamstate based on computed value.")

	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		st, err := storage.NewStorage(ctx, *dbFilename)
		if err != nil {
			return err
		}
		defer st.Close()

		stid, err := getStreamID(ctx, st, *streamID, *userID)
		if err != nil {
			return err
		}

		return cmds.CmdCheckStreamState(ctx, st, stid, *doFix)
	}
	return c
}

func main() {
	ctx := context.Background()
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	fmt.Println("Mastopoof")

	rootCmd := &cobra.Command{
		Use:   "mastopoof",
		Short: "Mastopoof is a Mastodon client",
		Long:  `More about Mastopoof`,
		// Avoid writing usage for random errors not related to CLI mistakes.
		// Unfortunately, it also prevents having usage for errors such as missing
		// flags, but https://github.com/spf13/cobra/issues/340 does not give simple
		// alternative.
		SilenceUsage: true,
	}

	rootCmd.AddCommand(cmdUsers())
	rootCmd.AddCommand(cmdClearApp())
	rootCmd.AddCommand(cmdClearStream())
	rootCmd.AddCommand(cmdClearPoolStream())
	rootCmd.AddCommand(cmdMe())
	rootCmd.AddCommand(cmdServe())
	rootCmd.AddCommand(cmdTestServe())
	rootCmd.AddCommand(cmdPickNext())
	rootCmd.AddCommand(cmdSetRead())
	rootCmd.AddCommand(CmdCheckStreamState())

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		glog.Exit(err)
	}
}
