package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"connectrpc.com/connect"
	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/Palats/mastopoof/backend/testdata"
	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
)

func TestBasic(t *testing.T) {
	ctx := context.Background()
	selfURL := ""
	scopes := "read"

	// Create Mastodon server.
	ts, err := testserver.New(testdata.Content())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ts.RegisterOn(mux)
	mastServer := httptest.NewServer(mux)
	defer mastServer.Close()
	addr := mastServer.URL

	// Creates mastopoof server.
	db, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := storage.NewStorage(db, selfURL, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Init(ctx); err != nil {
		t.Fatal(err)
	}
	s := New(st, "invite1", 0 /* autoLogin */, selfURL, scopes)

	// In the real server, the session token is transported through cookies
	// on the http layer. Here we're directly attaching it to the context
	// instead.
	sessionCtx, err := s.SessionManager.Load(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	// Get an authorize address.
	resp, err := s.Authorize(sessionCtx, connect.NewRequest(&pb.AuthorizeRequest{
		ServerAddr: addr,
		InviteCode: "invite1",
	}))
	if err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(resp.Msg.AuthorizeAddr)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.Path, "/oauth/authorize"; got != want {
		t.Errorf("Got addr path %s, want %s", got, want)
	}
}
