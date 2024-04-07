package server

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/Palats/mastopoof/backend/testdata"
	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
	"golang.org/x/net/publicsuffix"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type Msg[T any] interface {
	*T
	proto.Message
}

type TestClient struct {
	t        testing.TB
	client   *http.Client
	baseAddr string
}

func NewTestClient(t testing.TB, server *httptest.Server) *TestClient {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
	}

	return &TestClient{
		t:        t,
		client:   client,
		baseAddr: server.URL + "/_rpc/mastopoof.Mastopoof/",
	}
}

func Request[TRequest proto.Message](testClient *TestClient, method string, req TRequest) *http.Response {
	t := testClient.t
	t.Helper()

	raw, err := protojson.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	httpResp, err := testClient.client.Post(testClient.baseAddr+method, "application/json", bytes.NewBuffer(raw))
	if err != nil {
		t.Fatal(err)
	}
	return httpResp
}

func MustCall[TRespMsg any, TResponse Msg[TRespMsg], TRequest proto.Message](testClient *TestClient, method string, req TRequest) TResponse {
	t := testClient.t
	t.Helper()
	httpResp := Request(testClient, method, req)

	if got, want := httpResp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("Got status %v, want %v", got, want)
	}

	b, err := io.ReadAll(httpResp.Body)
	httpResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	resp := TResponse(new(TRespMsg))
	if err := protojson.Unmarshal(b, resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestBasic(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()

	selfURL := ""
	scopes := "read"

	// Create Mastodon server.
	ts, err := testserver.New(testdata.Content())
	if err != nil {
		t.Fatal(err)
	}
	ts.RegisterOn(mux)

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
	mastopoof := New(st, "invite1", 0 /* autoLogin */, selfURL, scopes)
	mastopoof.RegisterOn(mux)

	// Create the http server
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	testClient := NewTestClient(t, httpServer)

	// Try authorize with no invite code.
	req := &pb.AuthorizeRequest{
		ServerAddr: httpServer.URL,
		InviteCode: "",
	}
	if got, want := Request(testClient, "Authorize", req), http.StatusForbidden; got.StatusCode != want {
		t.Errorf("Got status %s, want %v", got.Status, want)
	}

	// Try with invalid code
	req.InviteCode = "invalid"
	if got, want := Request(testClient, "Authorize", req), http.StatusForbidden; got.StatusCode != want {
		t.Errorf("Got status %s, want %v", got.Status, want)
	}

	// Try with valid invite
	req.InviteCode = "invite1"
	resp := MustCall[pb.AuthorizeResponse](testClient, "Authorize", req)
	u, err := url.Parse(resp.AuthorizeAddr)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.Path, "/oauth/authorize"; got != want {
		t.Errorf("Got addr path %s, want %s", got, want)
	}
}
