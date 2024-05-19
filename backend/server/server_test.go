package server

import (
	"bytes"
	"context"
	"flag"
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
	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
	"golang.org/x/net/publicsuffix"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func init() {
	flag.Lookup("alsologtostderr").Value.Set("true")
}

type WithError[T any] struct {
	value T
	err   error
}

func NewWithError[T any](value T, err error) WithError[T] {
	return WithError[T]{value, err}
}

// LoggingHandler is an intercept http handler which writes http
// traffic on the provided testing construct.
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
	}

	if link := writer.Header().Get("Link"); link != "" {
		h.T.Logf("HTTP Response %d: Link: %v", idx, link)
	}
}

func MustBody(t testing.TB, r *http.Response) string {
	t.Helper()
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
	// Number of statuses to have available on Mastodon side.
	StatusesCount int

	// Provided after Init.
	client         *http.Client
	addr           string
	rpcAddr        string
	httpServer     *httptest.Server
	mastodonServer *testserver.Server
}

func (env *TestEnv) Init(ctx context.Context) *TestEnv {
	env.t.Helper()
	mux := http.NewServeMux()

	selfURL := ""
	scopes := "read"

	// Create Mastodon server.
	env.mastodonServer = testserver.New()
	for i := 0; i < int(env.StatusesCount); i++ {
		if err := env.mastodonServer.AddFakeStatus(); err != nil {
			env.t.Fatal(err)
		}
	}
	env.mastodonServer.RegisterOn(mux)

	// Creates mastopoof server.
	st, err := storage.NewStorage(ctx, "file::memory:?cache=shared", selfURL, scopes)
	if err != nil {
		env.t.Fatal(err)
	}
	mastopoof := New(st, NewSessionManager(st), "invite1", 0 /* autoLogin */, selfURL, scopes)
	mastopoof.RegisterOn(mux)

	// Create the http server
	env.httpServer = httptest.NewTLSServer(&LoggingHandler{T: env.t, Handler: mux})
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

// Request issues a call to the server.
// Safe in go-routines.
func Request[TRequest proto.Message](env *TestEnv, method string, req TRequest) (*http.Response, error) {
	raw, err := protojson.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("cannot marshal request: %w", err)
	}

	addr := env.rpcAddr + method
	httpResp, err := env.client.Post(addr, "application/json", bytes.NewBuffer(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to issue Post request: %w", err)
	}
	return httpResp, nil
}

// MustRequest issues a requests and returns the http response - which can include error codes
// from the server.
// Can call t.Fatal, unsafe from go routines.
func MustRequest[TRequest proto.Message](env *TestEnv, method string, req TRequest) *http.Response {
	env.t.Helper()
	httpResp, err := Request(env, method, req)
	if err != nil {
		env.t.Fatal(err)
	}
	return httpResp
}

// Call issues a request and parses the response as the provided message type.
func Call[TRespMsg any, TResponse Msg[TRespMsg], TRequest proto.Message](env *TestEnv, method string, req TRequest) (TResponse, error) {
	httpResp, err := Request(env, method, req)
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("unable to read http resp body: %w", err)
	}
	httpResp.Body.Close()

	if got, want := httpResp.StatusCode, http.StatusOK; got != want {
		return nil, fmt.Errorf("got status %v [%s], want %v; body=%s", got, httpResp.Status, want, string(body))
	}

	resp := TResponse(new(TRespMsg))
	if err := protojson.Unmarshal(body, resp); err != nil {
		return nil, fmt.Errorf("unable to parse body as JSON: %w", err)
	}
	return resp, nil
}

// MustCall calls a method on the server, parses the response and expect a success.
// Call t.Fatal otherwise, unsafe from go routines.
func MustCall[TRespMsg any, TResponse Msg[TRespMsg], TRequest proto.Message](env *TestEnv, method string, req TRequest) TResponse {
	env.t.Helper()
	resp, err := Call[TRespMsg, TResponse](env, method, req)
	if err != nil {
		env.t.Fatal(err)
	}
	return resp
}

func TestBasic(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t:             t,
		StatusesCount: 10,
	}).Init(ctx)
	defer env.Close()

	// Try authorize with no invite code.
	req := &pb.AuthorizeRequest{
		ServerAddr: env.addr,
		InviteCode: "",
	}
	if got, want := MustRequest(env, "Authorize", req), http.StatusForbidden; got.StatusCode != want {
		t.Errorf("Got status %s, want %v", got.Status, want)
	}

	// Try with invalid code
	req.InviteCode = "invalid"
	if got, want := MustRequest(env, "Authorize", req), http.StatusForbidden; got.StatusCode != want {
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
		Stid:      stid,
		Direction: pb.ListRequest_FORWARD,
	})
	if got, want := len(listResp.Items), fetchResp.FetchedCount; int64(got) != want {
		t.Errorf("List returned %d statuses, while fetch provided %d", got, want)
	}
}

func TestSetRead(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t:             t,
		StatusesCount: 10,
	}).Init(ctx)
	defer env.Close()
	userInfo := env.Login()

	// Make sure some statuses are in the pool.
	MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})

	// Need to start at 0.
	listResp := MustCall[pb.ListResponse](env, "List", &pb.ListRequest{
		Stid:      userInfo.DefaultStid,
		Direction: pb.ListRequest_FORWARD,
	})
	if got, want := listResp.StreamInfo.LastRead, int64(0); got != want {
		t.Errorf("Got last read %d, wanted %d", got, want)
	}

	// Updating without mode should fail.
	req := &pb.SetReadRequest{
		Stid:     userInfo.DefaultStid,
		LastRead: 2,
	}
	if resp, want := MustRequest(env, "SetRead", req), http.StatusBadRequest; resp.StatusCode != want {
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

func TestMultiFetch(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t:             t,
		StatusesCount: 100,
	}).Init(ctx)
	defer env.Close()
	userInfo := env.Login()

	// Read some statuses. We've added quite a few, but on first fetch,
	// it just gets the recent ones. That appears has one fetch with continuation
	// and one empty fetch.
	resp := MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})
	if got, want := resp.Status, pb.FetchResponse_MORE; got != want {
		t.Errorf("Got status %v, wanted %v; fetched %d statuses", got, want, resp.FetchedCount)
	}
	resp = MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})
	if got, want := resp.Status, pb.FetchResponse_DONE; got != want {
		t.Errorf("Got status %v, wanted %v; fetched %d statuses", got, want, resp.FetchedCount)
	}
	if resp.FetchedCount != 0 {
		t.Errorf("Got %d statuses, wanted 0", resp.FetchedCount)
	}

	// Insert more statuses - enough to require multiple fetches.
	for i := 0; i < 100; i++ {
		if err := env.mastodonServer.AddFakeStatus(); err != nil {
			t.Fatal(err)
		}
	}

	// Do one fetch, we should indicate a continuation.
	resp = MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})
	if got, want := resp.Status, pb.FetchResponse_MORE; got != want {
		t.Errorf("Got status %v, wanted %v; fetched %d statuses", got, want, resp.FetchedCount)
	}

	// And make sure we can continue to fetch until done in not too many iterations.
	count := 0
	for {
		count++
		resp = MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
			Stid: userInfo.DefaultStid,
		})
		if resp.Status == pb.FetchResponse_DONE {
			break
		}
		if count > 100 {
			t.Fatal("infinite fetch detected")
		}
	}
}

// TestConcurrentFetch verifies that when 2 fetch operations are started in parallel,
// one is rejected.
func TestConcurrentFetch(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t:             t,
		StatusesCount: 100,
	}).Init(ctx)
	defer env.Close()

	env.mastodonServer.TestBlockList = make(chan chan struct{})

	userInfo := env.Login()

	// Trigger first fetch request.
	resp1ch := make(chan WithError[*pb.FetchResponse])
	go func() {
		resp1ch <- NewWithError(Call[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
			Stid: userInfo.DefaultStid,
		}))
	}()

	// Verify that it is blocked on Mastodon server. It is done before starting
	// the second one to guarantee some consistent ordering.
	var block1ch chan struct{}
	select {
	case block1ch = <-env.mastodonServer.TestBlockList:
	case r := <-resp1ch:
		t.Fatalf("early response: err=%v, response=%v", r.err, r.value)
	}

	// Start second request.
	resp2ch := make(chan WithError[*http.Response])
	go func() {
		resp2ch <- NewWithError(Request(env, "Fetch", &pb.FetchRequest{
			Stid: userInfo.DefaultStid,
		}))
	}()

	// Verify that the second request is also blocked.
	var block2ch chan struct{}
	select {
	case block2ch = <-env.mastodonServer.TestBlockList:
	case r := <-resp2ch:
		t.Fatalf("early response: err=%v, response=%v", r.err, r.value)
	}

	// Unblock the first request.
	close(block1ch)
	resp1 := <-resp1ch
	if resp1.err != nil {
		t.Fatal(resp1.err)
	}
	if got, want := resp1.value.Status, pb.FetchResponse_MORE; got != want {
		t.Errorf("Got status %v, wanted %v; fetched %d statuses", got, want, resp1.value.FetchedCount)
	}

	// Unblock the second request, which should fail because the first request
	// was happening.
	close(block2ch)
	resp2 := <-resp2ch
	if resp2.err != nil {
		t.Fatal(resp1.err)
	}
	if got, want := resp2.value.StatusCode, http.StatusServiceUnavailable; got != want {
		body := MustBody(t, resp2.value)
		t.Fatalf("Got status %v [%s], want %v; body=%s", got, resp2.value.Status, want, body)
	}
}

func TestSearchStatusID(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t:             t,
		StatusesCount: 10,
	}).Init(ctx)
	defer env.Close()

	userInfo := env.Login()

	// Must fetch the statuses from Mastodon - otherwise, they're not in cache and not
	// visible to search.
	MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{Stid: userInfo.DefaultStid})

	// Test a search of one of the automatically generated status.
	searchResp := MustCall[pb.SearchResponse](env, "Search", &pb.SearchRequest{
		StatusId: "3",
	})
	if got, want := len(searchResp.Items), 1; got != want {
		t.Errorf("Got %d statuses, wanted %d; response:\n%v", got, want, searchResp)
	}
	if got, want := searchResp.Items[0].Account.Username, "testuser1"; got != want {
		t.Errorf("Got account username %s, want %s", got, want)
	}

	// Test for an unknown status - it should be empty, not an error.
	searchResp = MustCall[pb.SearchResponse](env, "Search", &pb.SearchRequest{
		StatusId: "999",
	})
	if got, want := len(searchResp.Items), 0; got != want {
		t.Errorf("Got %d statuses, wanted %d; response:\n%v", got, want, searchResp)
	}
}
