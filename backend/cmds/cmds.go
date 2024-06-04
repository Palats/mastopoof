package cmds

import (
	"context"
	"errors"
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
	"github.com/mattn/go-mastodon"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	"github.com/Palats/mastopoof/backend/storage"

	_ "github.com/mattn/go-sqlite3"
)

func CmdUsers(ctx context.Context, st *storage.Storage) error {
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

func CmdMe(ctx context.Context, st *storage.Storage, uid storage.UID, showAccount bool) error {
	fmt.Println("# User ID:", uid)

	userState, err := st.UserState(ctx, nil, uid)
	if err != nil {
		return err
	}
	fmt.Println("# Default stream ID:", userState.DefaultStID)
	stid := userState.DefaultStID

	accountState, err := st.FirstAccountStateByUID(ctx, nil, uid)
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

func CmdPickNext(ctx context.Context, st *storage.Storage, stid storage.StID) error {
	ost, err := st.PickNext(ctx, stid)
	if err != nil {
		return err
	}
	spew.Dump(ost)
	return nil
}

func CmdSetRead(ctx context.Context, st *storage.Storage, stid storage.StID, position int64) error {
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

func CmdCheckStreamState(ctx context.Context, st *storage.Storage, stid storage.StID, doFix bool) error {
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

		if doFix {
			fmt.Println("Applying changes in DB...")
			return nil
		}

		fmt.Println("Dry run, ignoring changes")
		return storage.ErrCleanAbortTxn
	})
}

var spaces = regexp.MustCompile(`\s+`)

func CmdTestServe(ctx context.Context, st *storage.Storage, mux *http.ServeMux, port int, testData string) error {
	testDataFS := os.DirFS(testData)
	ts := testserver.New()
	if err := ts.AddJSONStatuses(testDataFS); err != nil {
		return err
	}
	ts.RegisterOn(mux)

	addr := fmt.Sprintf(":%d", port)
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
				var err error
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
			case "fake-notifications":
				if len(args) > 1 {
					fmt.Printf("At most one parameter allowed")
					break
				}
				count := int64(10)
				var err error
				if len(args) > 0 {
					count, err = strconv.ParseInt(args[0], 10, 64)
					if err != nil {
						fmt.Printf("unable to parse %s: %v", args[0], err)
						break
					}
				}
				for i := int64(0); i < count; i++ {
					ts.AddFakeNotification()
				}
				fmt.Printf("Added %d fake notifications.\n", count)
			case "clear-notifications":
				if len(args) > 0 {
					fmt.Printf("'clear-notifications' does not take parameters")
					break
				}
				ts.ClearNotifications()
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
			{Text: "fake-notifications", Description: "Add notifications statuses; opt: number of notifications"},
			{Text: "clear-notifications", Description: "Clear all notifications"},
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
