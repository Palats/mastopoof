package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect"
	"github.com/Palats/mastopoof/backend/mastodon"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"
	"github.com/golang/glog"

	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
	"github.com/Palats/mastopoof/proto/gen/mastopoof/mastopoofconnect"
)

type Server struct {
	st             *storage.Storage
	inviteCode     string
	autoLogin      int64
	sessionManager *scs.SessionManager
	selfURL        string
	scopes         string
	client         http.Client
}

func New(st *storage.Storage, inviteCode string, autoLogin int64, selfURL string, scopes string) *Server {
	sessionManager := scs.New()
	sessionManager.Store = sqlite3store.New(st.DB)
	sessionManager.Lifetime = 90 * 24 * time.Hour
	sessionManager.Cookie.Name = "mastopoof"
	// Need Lax and not Strict for oauth redirections
	// https://stackoverflow.com/a/42220786
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode
	sessionManager.Cookie.Secure = true

	s := &Server{
		st:             st,
		sessionManager: sessionManager,
		inviteCode:     inviteCode,
		autoLogin:      autoLogin,
		selfURL:        selfURL,
		scopes:         scopes,
	}
	return s
}

func (s *Server) isLogged(ctx context.Context) error {
	userID := s.sessionManager.GetInt64(ctx, "userid")
	if userID == 0 {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("oh noes"))
	}
	return nil
}

// verifyStdID checks that the logged in user is allowed access to that
// stream.
func (s *Server) verifyStID(ctx context.Context, stid int64) (*storage.UserState, error) {
	userID := s.sessionManager.GetInt64(ctx, "userid")
	userState, err := s.st.UserState(ctx, s.st.DB, userID)
	if err != nil {
		return nil, err
	}
	if userState.DefaultStID != stid {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("stream access denied"))
	}
	return userState, nil
}

func (s *Server) Login(ctx context.Context, req *connect.Request[pb.LoginRequest]) (*connect.Response[pb.LoginResponse], error) {
	err := s.sessionManager.RenewToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to renew token: %w", err)
	}

	if s.autoLogin > 0 {
		// TODO: factorize login setup.
		s.sessionManager.Put(ctx, "userid", s.autoLogin)
	}

	// Trying to login only based on existing session.
	if err := s.isLogged(ctx); err != nil {
		// Not logged - do not return an error, but just no information.
		return connect.NewResponse(&pb.LoginResponse{}), nil
	}

	uid := s.sessionManager.GetInt64(ctx, "userid")

	userState, err := s.st.UserState(ctx, s.st.DB, uid)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&pb.LoginResponse{
		UserInfo: &pb.UserInfo{DefaultStid: userState.DefaultStID},
	}), nil
}

func (s *Server) Logout(ctx context.Context, req *connect.Request[pb.LogoutRequest]) (*connect.Response[pb.LogoutResponse], error) {
	s.sessionManager.Remove(ctx, "userid")
	err := s.sessionManager.RenewToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to renew token: %w", err)
	}
	return connect.NewResponse(&pb.LogoutResponse{}), nil
}

func (s *Server) Authorize(ctx context.Context, req *connect.Request[pb.AuthorizeRequest]) (*connect.Response[pb.AuthorizeResponse], error) {
	if s.inviteCode != "" {
		if req.Msg.InviteCode != s.inviteCode {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("Invalid invite code"))
		}
		// TODO: make it less hacky
		s.sessionManager.Put(ctx, "invitecheck", true)
	}

	serverAddr := req.Msg.ServerAddr
	if err := mastodon.ValidateAddress(serverAddr); err != nil {
		return nil, err
	}

	// TODO: split transactions to avoid remote requests in the middle.
	txn, err := s.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	ss, err := s.st.ServerState(ctx, txn, serverAddr)
	if errors.Is(err, storage.ErrNotFound) {
		glog.Infof("Creating server state for %q", serverAddr)
		ss, err = s.st.CreateServerState(ctx, txn, serverAddr)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	// If the server has no registration info, do it now.
	if ss.AuthURI == "" {
		// TODO: rate limiting to avoid abuse.
		// TODO: garbage collection of unused ones.
		// TODO: update redirect URIs as needed.
		glog.Infof("Registering app on server %q", serverAddr)
		app, err := mastodon.RegisterApp(ctx, &mastodon.AppConfig{
			Client:       s.client,
			Server:       ss.ServerAddr,
			ClientName:   "mastopoof",
			Scopes:       s.scopes,
			Website:      "https://github.com/Palats/mastopoof",
			RedirectURIs: ss.RedirectURI,
		})
		if err != nil {
			return nil, fmt.Errorf("unable to register app on server %s: %w", ss.ServerAddr, err)
		}
		ss.ClientID = app.ClientID
		ss.ClientSecret = app.ClientSecret
		ss.AuthURI = app.AuthURI

		if err := s.st.SetServerState(ctx, txn, ss); err != nil {
			return nil, err
		}
	}

	if err := txn.Commit(); err != nil {
		return nil, err
	}

	authAddr, err := url.Parse(ss.ServerAddr)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unable to parse %s: %w", ss.ServerAddr, err))
	}
	authAddr.Path = "/oauth/authorize"
	q := authAddr.Query()
	q.Set("client_id", ss.ClientID)
	q.Set("redirect_uri", ss.RedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "read")
	authAddr.RawQuery = q.Encode()

	return connect.NewResponse(&pb.AuthorizeResponse{
		AuthorizeAddr: authAddr.String(),
	}), nil
}

func (s *Server) Token(ctx context.Context, req *connect.Request[pb.TokenRequest]) (*connect.Response[pb.TokenResponse], error) {
	if s.inviteCode != "" {
		// TODO: make it less hacky
		if !s.sessionManager.GetBool(ctx, "invitecheck") {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("missing invite code"))
		}
	}

	// TODO: sanitization of server addr to be factorized with Authorize.
	serverAddr := req.Msg.ServerAddr
	if err := mastodon.ValidateAddress(serverAddr); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	authCode := req.Msg.AuthCode
	if authCode == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing authcode"))
	}

	// TODO: split transactions to avoid remote requests in the middle.
	txn, err := s.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	serverState, err := s.st.ServerState(ctx, txn, serverAddr)
	if err != nil {
		// Do not create the server - it should have been created on a previous step. If it is not there,
		// it is odd, so error out.
		return nil, err
	}

	// TODO: Re-use mastodon clients.
	client := mastodon.NewClient(&mastodon.Config{
		Server:       serverState.ServerAddr,
		ClientID:     serverState.ClientID,
		ClientSecret: serverState.ClientSecret,
	})
	client.Client = s.client

	err = client.AuthenticateToken(ctx, authCode, serverState.RedirectURI)
	if err != nil {
		return nil, fmt.Errorf("unable to authenticate on server %s: %w", serverState.ServerAddr, err)
	}

	// Now get info about the mastodon mastodonAccount so we can match it
	// to a local mastodonAccount.
	mastodonAccount, err := client.GetAccountCurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	accountID := mastodonAccount.ID
	if accountID == "" {
		return nil, errors.New("missing account ID")
	}
	username := mastodonAccount.Username

	// Find the account state (Mastodon).
	accountState, err := s.st.AccountStateByAccountID(ctx, txn, serverAddr, string(accountID))
	if errors.Is(err, storage.ErrNotFound) {
		// No mastodon account - and the way to find actual user is through the mastodon
		// account, so it means we need to create a user and then we can create
		// the mastodon account state.
		userState, err := s.st.CreateUser(ctx, txn, serverAddr, accountID, username)
		if err != nil {
			return nil, err
		}

		// And load the account state properly now it is created.
		accountState, err = s.st.AccountStateByUID(ctx, txn, userState.UID)
		if err != nil {
			return nil, fmt.Errorf("unable to re-read account state: %w", err)
		}
	} else if err != nil {
		return nil, err
	}

	// Get the userState
	// TODO: don't re-read it if it was just created
	userState, err := s.st.UserState(ctx, txn, accountState.UID)
	if err != nil {
		return nil, err
	}

	// Now, let's write the access token we got in the account state.
	accountState.AccessToken = client.Config.AccessToken
	if err := s.st.SetAccountState(ctx, txn, accountState); err != nil {
		return nil, err
	}

	if err := txn.Commit(); err != nil {
		return nil, err
	}

	// And mark the session as logged in.
	err = s.sessionManager.RenewToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to renew token: %w", err)
	}
	s.sessionManager.Put(ctx, "userid", userState.UID)

	return connect.NewResponse(&pb.TokenResponse{
		UserInfo: &pb.UserInfo{DefaultStid: userState.DefaultStID},
	}), nil
}

func (s *Server) List(ctx context.Context, req *connect.Request[pb.ListRequest]) (*connect.Response[pb.ListResponse], error) {
	stid := req.Msg.Stid
	userState, err := s.verifyStID(ctx, stid)
	if err != nil {
		return nil, err
	}

	accountState, err := s.st.AccountStateByUID(ctx, s.st.DB, userState.UID)
	if err != nil {
		return nil, err
	}
	account := &pb.Account{
		ServerAddr: accountState.ServerAddr,
		AccountId:  accountState.AccountID,
		Username:   accountState.Username,
	}

	resp := &pb.ListResponse{}

	var listResult *storage.ListResult
	switch req.Msg.Direction {
	case pb.ListRequest_FORWARD, pb.ListRequest_DEFAULT:
		listResult, err = s.st.ListForward(ctx, stid, req.Msg.Position)
		if err != nil {
			return nil, err
		}
	case pb.ListRequest_BACKWARD:
		listResult, err = s.st.ListBackward(ctx, stid, req.Msg.Position)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown direction %v", req.Msg.Direction)
	}

	resp.StreamInfo = listResult.StreamState.ToStreamInfo()

	if len(listResult.Items) > 0 {
		resp.BackwardPosition = listResult.Items[0].Position
		resp.ForwardPosition = listResult.Items[len(listResult.Items)-1].Position
	}

	for _, item := range listResult.Items {
		raw, err := json.Marshal(item.Status)
		if err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, &pb.Item{
			Status:   &pb.MastodonStatus{Content: string(raw)},
			Position: item.Position,
			Account:  account,
		})
	}

	return connect.NewResponse(resp), nil
}

func (s *Server) SetRead(ctx context.Context, req *connect.Request[pb.SetReadRequest]) (*connect.Response[pb.SetReadResponse], error) {
	stid := req.Msg.Stid
	if _, err := s.verifyStID(ctx, stid); err != nil {
		return nil, err
	}

	txn, err := s.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	streamState, err := s.st.StreamState(ctx, txn, stid)
	if err != nil {
		return nil, err
	}

	oldValue := streamState.LastRead

	requestedValue := req.Msg.GetLastRead()
	if requestedValue < streamState.FirstPosition-1 || requestedValue > streamState.LastPosition {
		return nil, fmt.Errorf("position %d is invalid", requestedValue)
	}

	switch req.Msg.Mode {
	case pb.SetReadRequest_ABSOLUTE:
		streamState.LastRead = requestedValue
	case pb.SetReadRequest_ADVANCE:
		if streamState.LastRead < requestedValue {
			streamState.LastRead = requestedValue
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Invalid SetRead mode %v", req.Msg.Mode))
	}

	if streamState.LastRead != oldValue {
		if err := s.st.SetStreamState(ctx, txn, streamState); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&pb.SetReadResponse{
		StreamInfo: streamState.ToStreamInfo(),
	}), txn.Commit()
}

func (s *Server) Fetch(ctx context.Context, req *connect.Request[pb.FetchRequest]) (*connect.Response[pb.FetchResponse], error) {
	stid := req.Msg.Stid
	if _, err := s.verifyStID(ctx, stid); err != nil {
		return nil, err
	}

	txn, err := s.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	streamState, err := s.st.StreamState(ctx, txn, stid)
	if err != nil {
		return nil, err
	}
	uid := streamState.UID

	accountState, err := s.st.AccountStateByUID(ctx, txn, uid)
	if err != nil {
		return nil, err
	}

	serverState, err := s.st.ServerState(ctx, txn, accountState.ServerAddr)
	if err != nil {
		return nil, err
	}

	// TODO: Re-use mastodon clients.
	client := mastodon.NewClient(&mastodon.Config{
		Server:       serverState.ServerAddr,
		ClientID:     serverState.ClientID,
		ClientSecret: serverState.ClientSecret,
		AccessToken:  accountState.AccessToken,
	})
	client.Client = s.client

	var statuses []*mastodon.Status

	fetchCount := 0
	// TODO: absolutely do not do fetching from server while in a local transaction.
	// Do multiple fetching, until either up to date, or up to a boundary to avoid infinite loops by mistake.
	for fetchCount < 10 {
		// Pagination object is updated by GetTimelimeHome, based on the `Link` header
		// returned by the API - see https://docs.joinmastodon.org/api/guidelines/#pagination .
		// On the query:
		//  - MaxID is an upper bound.
		//  - MinID will indicate to get statuses starting at that ID - aka, cursor like.
		//  - SinceID sets a lower bound on the results, but will prioritize recent results. I.e., it will
		//     return the last $Limit statuses, assuming they are all more recent than SinceID.
		// On the result, it seems:
		//  - MinID is set to the most recent ID returned (from the "prev" Link, which is for future statuses)
		//  - MaxID is set to an older ID (from the "next" Link, which is for older status)
		//  - SinceID, Limit are empty/0.
		// See https://github.com/mattn/go-mastodon/blob/9faaa4f0dc23d9001ccd1010a9a51f56ba8d2f9f/mastodon.go#L317
		// It seems that if MaxID and MinID are identical, it means the end has been reached and some result were given.
		// And if there is no MaxID, the end has been reached.
		pg := &mastodon.Pagination{
			MinID: accountState.LastHomeStatusID,
		}
		glog.Infof("Fetching from %s", pg.MinID)
		timeline, err := client.GetTimelineHome(ctx, pg)
		if err != nil {
			return nil, err
		}
		glog.Infof("Found %d new status on home timeline", len(timeline))

		statuses = append(statuses, timeline...)
		for _, status := range timeline {
			if storage.IDNewer(status.ID, accountState.LastHomeStatusID) {
				accountState.LastHomeStatusID = status.ID
			}
		}

		fetchCount++
		// Pagination got updated.
		if pg.MinID != accountState.LastHomeStatusID {
			// Either there is a mismatch in the data or no `Link` was returned
			// - in either case, we don't know enough to safely continue.
			glog.Infof("no returned MinID / ID mismatch, stopping fetch")
			break
		}
		if pg.MaxID == "" || pg.MaxID == pg.MinID {
			// We've reached the end - either nothing was fetched, or just the
			// latest ones.
			break
		}
		if len(timeline) == 0 {
			// Nothing was returned, assume it is because we've reached the end.
			break
		}
	}

	if err := s.st.SetAccountState(ctx, txn, accountState); err != nil {
		return nil, err
	}

	// TODO: mess of stream state - there is another version above.
	streamState, err = s.st.InsertStatuses(ctx, txn, uid, statuses)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&pb.FetchResponse{
		FetchedCount: int64(len(statuses)),
		StreamInfo:   streamState.ToStreamInfo(),
	}), txn.Commit()
}

func (s *Server) RedirectHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	if req.Method != "GET" {
		http.Error(w, "invalid method", http.StatusBadRequest)
		return
	}
	authCode := req.URL.Query().Get("code")
	if authCode == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	serverAddr := req.URL.Query().Get("host")
	if serverAddr == "" {
		http.Error(w, "missing host", http.StatusBadRequest)
		return
	}
	glog.Infof("redirect for serverAddr: %v", serverAddr)

	_, err := s.Token(ctx, connect.NewRequest(&pb.TokenRequest{
		ServerAddr: serverAddr,
		AuthCode:   authCode,
	}))
	if err != nil {
		msg := fmt.Sprintf("unable to identify: %v", err)
		glog.Errorf(msg)
		http.Error(w, msg, http.StatusForbidden)
		return
	}

	if s.selfURL == "" {
		fmt.Fprintf(w, "Auth done, no redirect configured.")
	} else {
		http.Redirect(w, req, s.selfURL, http.StatusFound)
	}
}

func (s *Server) RegisterOn(mux *http.ServeMux) {
	api := http.NewServeMux()
	api.Handle(mastopoofconnect.NewMastopoofHandler(s))
	mux.Handle("/_rpc/", s.sessionManager.LoadAndSave(http.StripPrefix("/_rpc", api)))
	mux.Handle("/_redirect", s.sessionManager.LoadAndSave(http.HandlerFunc(s.RedirectHandler)))
}
