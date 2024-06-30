package server

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Palats/mastopoof/backend/mastodon/testserver"
	"github.com/Palats/mastopoof/backend/storage"
	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
	"github.com/mattn/go-mastodon"
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

func MustUnmarshal[T any](t testing.TB, data []byte) T {
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
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

	scopes := "read"

	// Create Mastodon server.
	env.mastodonServer = testserver.New()
	for i := 0; i < int(env.StatusesCount); i++ {
		if _, err := env.mastodonServer.AddFakeStatus(); err != nil {
			env.t.Fatal(err)
		}
	}
	env.mastodonServer.RegisterOn(mux)

	// Creates mastopoof server.
	st, err := storage.NewStorage(ctx, "file::memory:?cache=shared")
	if err != nil {
		env.t.Fatal(err)
	}
	mastopoof := New(st, NewSessionManager(st), "invite1", 0 /* autoLogin */, scopes, nil)
	mastopoof.RegisterOn(mux)

	// Create the http server
	env.httpServer = httptest.NewTLSServer(&testserver.LoggingHandler{T: env.t, Handler: mux})
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

// FullLogin makes sure that the client is logged on a test user, with some statuses already fetched.
func (env *TestEnv) FullLogin() *pb.UserInfo {
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
	userInfo := env.FullLogin()

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
	userInfo := env.FullLogin()

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
		if _, err := env.mastodonServer.AddFakeStatus(); err != nil {
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

	userInfo := env.FullLogin()

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
	// Unblock the second request, which should fail because the first request
	// was happening.
	// This needs to be done even in case of test not passing - otherwise, the request stays in flight, which
	// prevents the test server to close all outstanding connections. And it seems that
	// connection cleanup does not cancel the context, so it is not detected.
	close(block2ch)
	resp2 := <-resp2ch

	if resp1.err != nil {
		t.Fatal(resp1.err)
	}
	if got, want := resp1.value.Status, pb.FetchResponse_MORE; got != want {
		t.Errorf("Got status %v, wanted %v; fetched %d statuses", got, want, resp1.value.FetchedCount)
	}

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

	userInfo := env.FullLogin()

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

func TestNotifs(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t: t,
	}).Init(ctx)
	defer env.Close()
	userInfo := env.FullLogin()

	// Fist, no statuses.
	resp := MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})
	if got, want := resp.StreamInfo.NotificationState, pb.StreamInfo_NOTIF_EXACT; got != want {
		t.Errorf("Got notif state %v, wanted %v", got, want)
	}
	if got, want := resp.StreamInfo.NotificationsCount, int64(0); got != want {
		t.Errorf("Got %d notifications, wanted %d", got, want)
	}

	// Now, let's add a few statuses and see that we get them.
	// It needs to have a status to notify about.
	if _, err := env.mastodonServer.AddFakeStatus(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := env.mastodonServer.AddFakeNotification(); err != nil {
			t.Fatal(err)
		}
	}
	resp = MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})
	if got, want := resp.StreamInfo.NotificationState, pb.StreamInfo_NOTIF_EXACT; got != want {
		t.Errorf("Got notif state %v, wanted %v", got, want)
	}
	if got, want := resp.StreamInfo.NotificationsCount, int64(5); got != want {
		t.Errorf("Got %d notifications, wanted %d", got, want)
	}

	// Add a lot more notifications - it should not have the exact number.
	for i := 0; i < 100; i++ {
		if err := env.mastodonServer.AddFakeNotification(); err != nil {
			t.Fatal(err)
		}
	}
	resp = MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})
	if got, want := resp.StreamInfo.NotificationState, pb.StreamInfo_NOTIF_MORE; got != want {
		t.Errorf("Got notif state %v, wanted %v", got, want)
	}

	// And reset notifications.
	env.mastodonServer.ClearNotifications()
	resp = MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})
	if got, want := resp.StreamInfo.NotificationState, pb.StreamInfo_NOTIF_EXACT; got != want {
		t.Errorf("Got notif state %v, wanted %v", got, want)
	}
	if got, want := resp.StreamInfo.NotificationsCount, int64(0); got != want {
		t.Errorf("Got %d notifications, wanted %d", got, want)
	}
}

func TestSendUserInfo(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t: t,
	}).Init(ctx)
	defer env.Close()

	// Check user info content when initially logging in.
	userInfo := env.FullLogin()
	if got, want := len(userInfo.GetAccounts()), 1; got != want {
		t.Fatalf("Got %d accounts, wanted %d", got, want)
	}
	if got, want := userInfo.Accounts[0].Username, "testuser1"; got != want {
		t.Errorf("Got username %s, wanted %s", got, want)
	}

	// Also check Login() method, which is really verifying that
	// the cookie exists and is valid.
	resp := MustCall[pb.LoginResponse](env, "Login", &pb.LoginRequest{})
	userInfo = resp.UserInfo
	if got, want := len(userInfo.GetAccounts()), 1; got != want {
		t.Fatalf("Got %d accounts, wanted %d", got, want)
	}
	if got, want := userInfo.Accounts[0].Username, "testuser1"; got != want {
		t.Errorf("Got username %s, wanted %s", got, want)
	}
}

func TestFavourite(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t: t,
	}).Init(ctx)
	defer env.Close()
	userInfo := env.FullLogin()

	refStatus, err := env.mastodonServer.AddFakeStatus()
	if err != nil {
		t.Fatal(err)
	}

	MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})

	// Set favourite
	resp := MustCall[pb.SetStatusResponse](env, "SetStatus", &pb.SetStatusRequest{
		StatusId: string(refStatus.ID),
		Action:   pb.SetStatusRequest_FAVOURITE,
	})
	var gotStatus mastodon.Status
	if err := json.Unmarshal([]byte(resp.GetStatus().GetContent()), &gotStatus); err != nil {
		t.Fatal(err)
	}
	if got, want := gotStatus.Favourited, true; got != want {
		t.Errorf("got favourite %v, want %v", got, want)
	}

	// And unset
	resp = MustCall[pb.SetStatusResponse](env, "SetStatus", &pb.SetStatusRequest{
		StatusId: string(refStatus.ID),
		Action:   pb.SetStatusRequest_UNFAVOURITE,
	})
	if err := json.Unmarshal([]byte(resp.GetStatus().GetContent()), &gotStatus); err != nil {
		t.Fatal(err)
	}
	if got, want := gotStatus.Favourited, false; got != want {
		t.Errorf("got favourite %v, want %v", got, want)
	}
}

func TestRefreshStatus(t *testing.T) {
	ctx := context.Background()
	env := (&TestEnv{
		t: t,
		// Add a few extra statuses - this way, that check that the right one is updated.
		StatusesCount: 3,
	}).Init(ctx)
	defer env.Close()
	userInfo := env.FullLogin()

	// Create a status on the server.
	refStatus, err := env.mastodonServer.AddFakeStatus()
	if err != nil {
		t.Fatal(err)
	}

	// Make sure it is fetched and init the stream.
	MustCall[pb.FetchResponse](env, "Fetch", &pb.FetchRequest{
		Stid: userInfo.DefaultStid,
	})
	listInitResp := MustCall[pb.ListResponse](env, "List", &pb.ListRequest{
		Stid:      userInfo.DefaultStid,
		Direction: pb.ListRequest_INITIAL,
	})

	// Now, change its content on Mastodon
	newStatus := *refStatus
	newStatus.Content = "newcontent1"
	if err := env.mastodonServer.UpdateStatus(&newStatus); err != nil {
		t.Fatal(err)
	}

	// Test that Refresh status gets the new content
	resp := MustCall[pb.SetStatusResponse](env, "SetStatus", &pb.SetStatusRequest{
		StatusId: string(refStatus.ID),
		Action:   pb.SetStatusRequest_REFRESH,
	})
	refreshStatus := MustUnmarshal[mastodon.Status](t, []byte(resp.GetStatus().GetContent()))
	if got, want := refreshStatus.Content, "newcontent1"; got != want {
		t.Errorf("Got status with content %q, wanted %q", got, want)
	}

	// And verify that the list also gives back the refreshed status.
	// This means that the DB was updated.
	listResp := MustCall[pb.ListResponse](env, "List", &pb.ListRequest{
		Stid:      userInfo.DefaultStid,
		Direction: pb.ListRequest_FORWARD,
		Position:  listInitResp.ForwardPosition,
	})
	var listStatus *mastodon.Status
	for _, item := range listResp.Items {
		s := MustUnmarshal[mastodon.Status](t, []byte(item.Status.Content))
		if s.ID == refStatus.ID {
			listStatus = &s
			break
		}
	}
	if listStatus == nil {
		t.Fatalf("unable to find status %q", refStatus.ID)
	}
	if got, want := listStatus.Content, "newcontent1"; got != want {
		t.Errorf("Got status with content %q, wanted %q", got, want)
	}
}
