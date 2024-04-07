package server

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/Palats/mastopoof/backend/testdata"
	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
	"golang.org/x/net/publicsuffix"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type LoggingHandler struct {
	T       testing.TB
	Handler http.Handler

	m   sync.Mutex
	idx int64
}

func (h *LoggingHandler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	h.m.Lock()
	idx := h.idx
	h.idx++
	h.m.Unlock()

	cookie, _ := req.Cookie("mastopoof")
	h.T.Logf("HTTP Request %d: %s %s [cookie:%s]", idx, req.Host, req.URL, cookie)

	// Do the actual request.
	h.Handler.ServeHTTP(writer, req)

	// And see the cookies that were sent back.
	header := http.Header{}
	header.Add("Cookie", writer.Header().Get("Set-Cookie"))
	respCookie, err := (&http.Request{Header: header}).Cookie("mastopoof")
	if err == nil {
		h.T.Logf("HTTP Response %d: Set-Cookie:%v", idx, respCookie)
	} else {
		h.T.Logf("HTTP Response %d: no cookie set", idx)
	}
}

func MustBody(t testing.TB, r *http.Response) string {
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type Msg[T any] interface {
	*T
	proto.Message
}

type TestEnv struct {
	// To be provided.
	t testing.TB

	// Provided after Init.
	client     *http.Client
	addr       string
	rpcAddr    string
	db         *sql.DB
	httpServer *httptest.Server
}

func (env *TestEnv) Init(ctx context.Context) *TestEnv {
	env.t.Helper()
	mux := http.NewServeMux()

	selfURL := ""
	scopes := "read"

	// Create Mastodon server.
	ts, err := testserver.New(testdata.Content())
	if err != nil {
		env.t.Fatal(err)
	}
	ts.RegisterOn(mux)

	// Creates mastopoof server.
	env.db, err = sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		env.t.Fatal(err)
	}
	// defer db.Close()
	st, err := storage.NewStorage(env.db, selfURL, scopes)
	if err != nil {
		env.t.Fatal(err)
	}
	if err := st.Init(ctx); err != nil {
		env.t.Fatal(err)
	}
	mastopoof := New(st, "invite1", 0 /* autoLogin */, selfURL, scopes)
	mastopoof.RegisterOn(mux)

	// Create the http server
	env.httpServer = httptest.NewTLSServer(&LoggingHandler{T: env.t, Handler: mux})
	// defer httpServer.Close()
	env.addr = env.httpServer.URL
	env.rpcAddr = env.httpServer.URL + "/_rpc/mastopoof.Mastopoof/"
	mastopoof.client = *env.httpServer.Client()

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		env.t.Fatal(err)
	}

	env.client = env.httpServer.Client()
	env.client.Jar = jar
	env.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return fmt.Errorf("forbidding redirect for %s", req.URL)
	}

	return env
}

func (env *TestEnv) Close() {
	if env.httpServer != nil {
		env.httpServer.Close()
	}
	if env.db != nil {
		env.db.Close()
	}
}

// Login makes sure that the client is logged on a test user, with some statuses already fetched.
func (env *TestEnv) Login() *pb.UserInfo {
	MustCall[pb.AuthorizeResponse](env, "Authorize", &pb.AuthorizeRequest{
		ServerAddr: env.addr,
		InviteCode: "invite1",
	})
	tokenResp := MustCall[pb.TokenResponse](env, "Token", &pb.TokenRequest{
		ServerAddr: env.addr,
		AuthCode:   "foo",
	})
	return tokenResp.UserInfo
}

func Request[TRequest proto.Message](env *TestEnv, method string, req TRequest) *http.Response {
	t := env.t
	t.Helper()

	raw, err := protojson.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	addr := env.rpcAddr + method
	httpResp, err := env.client.Post(addr, "application/json", bytes.NewBuffer(raw))
	if err != nil {
		t.Fatal(err)
	}
	return httpResp
}

func MustCall[TRespMsg any, TResponse Msg[TRespMsg], TRequest proto.Message](env *TestEnv, method string, req TRequest) TResponse {
	t := env.t
	t.Helper()
	httpResp := Request(env, method, req)

	if got, want := httpResp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("Got status %v [%s], want %v; body=%s", got, httpResp.Status, want, MustBody(t, httpResp))
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
	env := (&TestEnv{
		t: t,
	}).Init(ctx)
	defer env.Close()

	// Try authorize with no invite code.
	req := &pb.AuthorizeRequest{
		ServerAddr: env.addr,
		InviteCode: "",
	}
	if got, want := Request(env, "Authorize", req), http.StatusForbidden; got.StatusCode != want {
		t.Errorf("Got status %s, want %v", got.Status, want)
	}

	// Try with invalid code
	req.InviteCode = "invalid"
	if got, want := Request(env, "Authorize", req), http.StatusForbidden; got.StatusCode != want {
		t.Errorf("Got status %s, want %v", got.Status, want)
	}

	// Try with valid invite
	req.InviteCode = "invite1"
	resp := MustCall[pb.AuthorizeResponse](env, "Authorize", req)
	u, err := url.Parse(resp.AuthorizeAddr)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.Path, "/oauth/authorize"; got != want {
		t.Errorf("Got addr path %s, want %s", got, want)
	}

	// Now get token
	tokenResp := MustCall[pb.TokenResponse](env, "Token", &pb.TokenRequest{
		ServerAddr: env.addr,
		AuthCode:   "foo",
	})
	stid := tokenResp.UserInfo.DefaultStid

	// Fetch a few statuses in the stream.
	fetchResp := MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: stid,
	})
	if got, want := fetchResp.FetchedCount, int64(2); got < want {
		t.Errorf("Fetched %d statuses, wanted %d", got, want)
	}

	// Try to list them
	listResp := MustCall[pb.ListResponse](env, "List", &pb.ListRequest{
		Stid: stid,
	})
	if got, want := len(listResp.Items), fetchResp.FetchedCount; int64(got) != want {
		t.Errorf("List returned %d statuses, while fetch provided %d", got, want)
	}
}

func TestSetRead(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t: t,
	}).Init(ctx)
	defer env.Close()
	userInfo := env.Login()

	// Make sure some statuses are in the pool.
	MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})

	// Need to start at 0.
	listResp := MustCall[pb.ListResponse](env, "List", &pb.ListRequest{
		Stid: userInfo.DefaultStid,
	})
	if got, want := listResp.StreamInfo.LastRead, int64(0); got != want {
		t.Errorf("Got last read %d, wanted %d", got, want)
	}

	// Updating without mode should fail.
	req := &pb.SetReadRequest{
		Stid:     userInfo.DefaultStid,
		LastRead: 2,
	}
	if resp, want := Request(env, "SetRead", req), http.StatusBadRequest; resp.StatusCode != want {
		t.Errorf("Got status code %v, wanted %v", resp.StatusCode, want)
	}

	// Update last read to another position.
	lastReadResp := MustCall[pb.SetReadResponse](env, "SetRead", &pb.SetReadRequest{
		Stid:     userInfo.DefaultStid,
		LastRead: 2,
		Mode:     pb.SetReadRequest_ADVANCE,
	})
	if got, want := lastReadResp.StreamInfo.LastRead, int64(2); got != want {
		t.Errorf("Got last read %d, wanted %d", got, want)
	}

	// Trying to go backward in advance mode should not change things.
	lastReadResp = MustCall[pb.SetReadResponse](env, "SetRead", &pb.SetReadRequest{
		Stid:     userInfo.DefaultStid,
		LastRead: 1,
		Mode:     pb.SetReadRequest_ADVANCE,
	})
	if got, want := lastReadResp.StreamInfo.LastRead, int64(2); got != want {
		t.Errorf("Got last read %d, wanted %d", got, want)
	}

	// But backward in absolute mode should be fine.
	lastReadResp = MustCall[pb.SetReadResponse](env, "SetRead", &pb.SetReadRequest{
		Stid:     userInfo.DefaultStid,
		LastRead: 1,
		Mode:     pb.SetReadRequest_ABSOLUTE,
	})
	if got, want := lastReadResp.StreamInfo.LastRead, int64(1); got != want {
		t.Errorf("Got last read %d, wanted %d", got, want)
	}
}
