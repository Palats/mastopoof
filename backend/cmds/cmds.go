package cmds

import (
	"context"
	"fmt"

	"github.com/davecgh/go-spew/spew"
	"github.com/mattn/go-mastodon"

	"github.com/Palats/mastopoof/backend/server"
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
			userEntry.UserState.Uid,
			userEntry.AccountState.ASID,
			userEntry.AccountState.Username,
			userEntry.AccountState.AccountID,
			userEntry.AccountState.ServerAddr,
			userEntry.UserState.DefaultStid)
	}
	return nil
}

func CmdMe(ctx context.Context, st *storage.Storage, uid storage.UID, showAccount bool) error {
	fmt.Println("# User ID:", uid)

	userState, err := st.UserState(ctx, nil, uid)
	if err != nil {
		return err
	}
	fmt.Println("# Default stream ID:", userState.DefaultStid)
	stid := storage.StID(userState.DefaultStid)

	accountState, err := st.FirstAccountStateByUID(ctx, nil, uid)
	if err != nil {
		return err
	}
	fmt.Println("# Server address:", accountState.ServerAddr)
	fmt.Println("# Last home status ID:", accountState.LastHomeStatusID)

	appRegistry := server.NewAppRegistry(st)

	// TODO: That should pick up the first available registration for that server, ignoring (most) scopes and redirection.
	appRegState, err := appRegistry.Register(ctx, accountState.ServerAddr, nil)
	if err != nil {
		return err
	}
	fmt.Println("# Auth URI:", appRegState.AuthUri)
	fmt.Println("# Redirect URI:", appRegState.RedirectUri)

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
			ClientID:     appRegState.ClientId,
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
