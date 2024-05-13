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
	"github.com/alexedwards/scs/v2"
	"github.com/golang/glog"

	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
	"github.com/Palats/mastopoof/proto/gen/mastopoof/mastopoofconnect"
)

func NewSessionManager(st *storage.Storage) *scs.SessionManager {
	sessionManager := scs.New()
	sessionManager.Store = st.NewSCSStore()
	sessionManager.Lifetime = 90 * 24 * time.Hour
	sessionManager.Cookie.Name = "mastopoof"
	// Need Lax and not Strict for oauth redirections
	// https://stackoverflow.com/a/42220786
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode
	sessionManager.Cookie.Secure = true
	return sessionManager
}

type Server struct {
	st             *storage.Storage
	inviteCode     string
	autoLogin      storage.UID
	sessionManager *scs.SessionManager
	selfURL        string
	scopes         string
	client         http.Client
}

func New(st *storage.Storage, sessionManager *scs.SessionManager, inviteCode string, autoLogin storage.UID, selfURL string, scopes string) *Server {
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

func (s *Server) setSessionUserID(ctx context.Context, uid storage.UID) {
	// `uid`` must be converted from `UID` to `int64`, otherwise session manager
	// has trouble serializing it.
	s.sessionManager.Put(ctx, "userid", int64(uid))
}

func (s *Server) isLogged(ctx context.Context) (storage.UID, error) {
	userID := storage.UID(s.sessionManager.GetInt64(ctx, "userid"))
	if userID == 0 {
		return 0, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("oh noes"))
	}
	return userID, nil
}

// verifyStdID checks that the logged in user is allowed access to that
// stream.
func (s *Server) verifyStID(ctx context.Context, stid storage.StID) (*storage.UserState, error) {
	userID, err := s.isLogged(ctx)
	if err != nil {
		return nil, err
	}
	userState, err := s.st.UserState(ctx, nil, userID)
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
		s.setSessionUserID(ctx, s.autoLogin)
	}

	// Trying to login only based on existing session.
	uid, err := s.isLogged(ctx)
	if err != nil {
		// Not logged - do not return an error, but just no information.
		return connect.NewResponse(&pb.LoginResponse{}), nil
	}

	userState, err := s.st.UserState(ctx, nil, uid)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&pb.LoginResponse{
		UserInfo: &pb.UserInfo{DefaultStid: int64(userState.DefaultStID)},
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
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid invite code"))
		}
		// TODO: make it less hacky
		s.sessionManager.Put(ctx, "invitecheck", true)
	}

	serverAddr := req.Msg.ServerAddr
	if err := mastodon.ValidateAddress(serverAddr); err != nil {
		return nil, err
	}

	// TODO: split transactions to avoid remote requests in the middle.

	var appRegState *storage.AppRegState
	err := s.st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
		var err error
		appRegState, err = s.st.AppRegState(ctx, txn, serverAddr)
		if errors.Is(err, storage.ErrNotFound) {
			glog.Infof("Creating server state for %q", serverAddr)
			appRegState, err = s.st.CreateAppRegState(ctx, txn, serverAddr)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		// If the server has no registration info, do it now.
		if appRegState.AuthURI == "" {
			// TODO: rate limiting to avoid abuse.
			// TODO: garbage collection of unused ones.
			// TODO: update redirect URIs as needed.
			glog.Infof("Registering app on server %q", serverAddr)
			app, err := mastodon.RegisterApp(ctx, &mastodon.AppConfig{
				Client:       s.client,
				Server:       appRegState.ServerAddr,
				ClientName:   "mastopoof",
				Scopes:       s.scopes,
				Website:      "https://github.com/Palats/mastopoof",
				RedirectURIs: appRegState.RedirectURI,
			})
			if err != nil {
				return fmt.Errorf("unable to register app on server %s: %w", appRegState.ServerAddr, err)
			}
			appRegState.ClientID = app.ClientID
			appRegState.ClientSecret = app.ClientSecret
			appRegState.AuthURI = app.AuthURI

			if err := s.st.SetAppRegState(ctx, txn, appRegState); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	authAddr, err := url.Parse(appRegState.ServerAddr)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unable to parse %s: %w", appRegState.ServerAddr, err))
	}
	authAddr.Path = "/oauth/authorize"
	q := authAddr.Query()
	q.Set("client_id", appRegState.ClientID)
	q.Set("redirect_uri", appRegState.RedirectURI)
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
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unable to validate address %s: %w", serverAddr, err))
	}

	authCode := req.Msg.AuthCode
	if authCode == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing authcode"))
	}

	var userState *storage.UserState

	// TODO: split transactions to avoid remote requests in the middle.
	err := s.st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
		appRegState, err := s.st.AppRegState(ctx, txn, serverAddr)
		if err != nil {
			// Do not create the server - it should have been created on a previous step. If it is not there,
			// it is odd, so error out.
			return fmt.Errorf("unable to load server state for addr %s: %w", serverAddr, err)
		}

		// TODO: Re-use mastodon clients.
		client := mastodon.NewClient(&mastodon.Config{
			Server:       appRegState.ServerAddr,
			ClientID:     appRegState.ClientID,
			ClientSecret: appRegState.ClientSecret,
		})
		client.Client = s.client

		err = client.AuthenticateToken(ctx, authCode, appRegState.RedirectURI)
		if err != nil {
			return fmt.Errorf("unable to authenticate on server %s: %w", appRegState.ServerAddr, err)
		}

		// Now get info about the mastodon mastodonAccount so we can match it
		// to a local mastodonAccount.
		mastodonAccount, err := client.GetAccountCurrentUser(ctx)
		if err != nil {
			return fmt.Errorf("failure whena calling Mastodon AccountCurrentUser: %w", err)
		}
		accountID := mastodonAccount.ID
		if accountID == "" {
			return errors.New("missing account ID")
		}
		username := mastodonAccount.Username

		// Find the account state (Mastodon).
		accountState, err := s.st.AccountStateByAccountID(ctx, txn, serverAddr, string(accountID))
		if errors.Is(err, storage.ErrNotFound) {
			// No mastodon account - and the way to find actual user is through the mastodon
			// account, so it means we need to create a user and then we can create
			// the mastodon account state.
			userState, accountState, _, err = s.st.CreateUser(ctx, txn, serverAddr, accountID, username)
			if err != nil {
				return fmt.Errorf("failed to create user %s/%s@%s: %w", accountID, username, serverAddr, err)
			}
		} else if err != nil {
			return err
		}

		// Get the userState
		// TODO: don't re-read it if it was just created
		userState, err = s.st.UserState(ctx, txn, accountState.UID)
		if err != nil {
			return fmt.Errorf("unable to load userstate for UID %d: %w", accountState.UID, err)
		}

		// Now, let's write the access token we got in the account state.
		accountState.AccessToken = client.Config.AccessToken
		if err := s.st.SetAccountState(ctx, txn, accountState); err != nil {
			return fmt.Errorf("failed to set account state %d: %w", accountState.ASID, err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("Token txn failed: %w", err)
	}

	// And mark the session as logged in.
	err = s.sessionManager.RenewToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to renew token: %w", err)
	}
	s.setSessionUserID(ctx, userState.UID)

	return connect.NewResponse(&pb.TokenResponse{
		UserInfo: &pb.UserInfo{DefaultStid: int64(userState.DefaultStID)},
	}), nil
}

func (s *Server) List(ctx context.Context, req *connect.Request[pb.ListRequest]) (*connect.Response[pb.ListResponse], error) {
	stid := storage.StID(req.Msg.Stid)
	userState, err := s.verifyStID(ctx, stid)
	if err != nil {
		return nil, err
	}

	accountState, err := s.st.AccountStateByUID(ctx, nil, userState.UID)
	if err != nil {
		return nil, err
	}
	accountStateProto := accountState.ToAccountProto()

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
			// TODO: account is potentially per status, while it is currently considered per user.
			Account: accountStateProto,
		})
	}

	return connect.NewResponse(resp), nil
}

func (s *Server) SetRead(ctx context.Context, req *connect.Request[pb.SetReadRequest]) (*connect.Response[pb.SetReadResponse], error) {
	stid := storage.StID(req.Msg.Stid)
	if _, err := s.verifyStID(ctx, stid); err != nil {
		return nil, err
	}

	var streamState *storage.StreamState
	err := s.st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
		var err error
		streamState, err = s.st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}

		oldValue := streamState.LastRead

		requestedValue := req.Msg.GetLastRead()
		if requestedValue < streamState.FirstPosition-1 || requestedValue > streamState.LastPosition {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("position %d is invalid; first=%d, last=%d", requestedValue, streamState.FirstPosition, streamState.LastPosition))
		}

		switch req.Msg.Mode {
		case pb.SetReadRequest_ABSOLUTE:
			streamState.LastRead = requestedValue
		case pb.SetReadRequest_ADVANCE:
			if streamState.LastRead < requestedValue {
				streamState.LastRead = requestedValue
			}
		default:
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid SetRead mode %v", req.Msg.Mode))
		}

		if streamState.LastRead != oldValue {
			if err := s.st.SetStreamState(ctx, txn, streamState); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&pb.SetReadResponse{
		StreamInfo: streamState.ToStreamInfo(),
	}), nil
}

func (s *Server) Fetch(ctx context.Context, req *connect.Request[pb.FetchRequest]) (*connect.Response[pb.FetchResponse], error) {
	// Check for credentials.
	stid := storage.StID(req.Msg.Stid)
	if _, err := s.verifyStID(ctx, stid); err != nil {
		return nil, err
	}

	// Do a first transaction to get the state of the stream. That will serve as
	// reference when trying to inject in the DB the statuses - while avoiding
	// having a transaction opened while fetching.
	var accountState *storage.AccountState
	var appRegState *storage.AppRegState
	err := s.st.InTxnRO(ctx, func(ctx context.Context, txn storage.SQLReadOnly) error {
		streamState, err := s.st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}

		// For support for having multiple Mastodon account, this would
		// need to list the accounts and do the fetching from each account.
		accountState, err = s.st.AccountStateByUID(ctx, txn, streamState.UID)
		if err != nil {
			return err
		}

		appRegState, err = s.st.AppRegState(ctx, txn, accountState.ServerAddr)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// TODO: Re-use mastodon clients.
	client := mastodon.NewClient(&mastodon.Config{
		Server:       appRegState.ServerAddr,
		ClientID:     appRegState.ClientID,
		ClientSecret: appRegState.ClientSecret,
		AccessToken:  accountState.AccessToken,
	})
	client.Client = s.client

	// We've got what we wanted from the DB, now we can fetch from Mastodon outside
	// a transaction.

	// Pagination object is updated by GetTimelimeHome, based on the `Link` header
	// returned by the API - see https://docs.joinmastodon.org/api/guidelines/#pagination .
	// On the query:
	//  - max_id (MaxID) is an upper bound, not included.
	//  - min_id (MinID) will indicate to get statuses starting at that ID - aka, cursor like.
	//  - since_id (SinceID) sets a lower bound on the results, but will prioritize recent results. I.e., it will
	//     return the last $Limit statuses, assuming they are all more recent than SinceID.
	// On the result, it seems:
	//  - min_id is set to the most recent ID returned (from the "prev" Link, which is for future statuses)
	//  - max_id is set to an older ID (from the "next" Link, which is for older statuses).
	//    Set to 0 when no statuses are returned when having reached most recent stuff.
	//  - since_id, Limit are empty/0.
	// See https://github.com/mattn/go-mastodon/blob/9faaa4f0dc23d9001ccd1010a9a51f56ba8d2f9f/mastodon.go#L317
	// It seems that if max_id and min_id are identical, it means the end has been reached and some result were given.
	// And if there is no max_id, the end has been reached.
	pg := &mastodon.Pagination{
		MinID: accountState.LastHomeStatusID,
	}
	glog.Infof("Fetching... (max_id:%v, min_id:%v, since_id:%v)", pg.MaxID, pg.MinID, pg.SinceID)
	timeline, err := client.GetTimelineHome(ctx, pg)
	if err != nil {
		glog.Errorf("unable to get timeline: %v", err)
		return nil, err
	}

	filters, err := client.GetFilters(ctx)
	if err != nil {
		glog.Errorf("unable to get filters: %v", err)
		return nil, err
	}

	newStatusID := accountState.LastHomeStatusID
	for _, status := range timeline {
		if storage.IDNewer(status.ID, newStatusID) {
			newStatusID = status.ID
		}
	}

	boundaries := ""
	if len(timeline) > 0 {
		boundaries = fmt.Sprintf(" (%s -- %s)", timeline[0].ID, timeline[len(timeline)-1].ID)
	}
	glog.Infof("Found %d new status on home timeline (LastHomeStatusID=%v) (max_id:%v, min_id:%v, since_id:%v)%s", len(timeline), newStatusID, pg.MaxID, pg.MinID, pg.SinceID, boundaries)

	lastFetchSecs := time.Now().Unix()

	// Start preparing the response.
	resp := &pb.FetchResponse{
		Status:       pb.FetchResponse_MORE,
		FetchedCount: int64(len(timeline)),
	}

	// Now do another transaction to update the DB - both statuses
	// and inserting statuses.
	err = s.st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
		// Refetch stream state - we do not want to use an old one from a previous transaction, which
		// might have outdated content.
		streamState, err := s.st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}
		streamState.LastFetchSecs = lastFetchSecs

		currentAccountState, err := s.st.AccountStateByUID(ctx, txn, streamState.UID)
		if err != nil {
			return fmt.Errorf("unable to verify for race conditions: %w", err)
		}
		if currentAccountState.LastHomeStatusID != accountState.LastHomeStatusID {
			return connect.NewError(connect.CodeUnavailable, errors.New("concurrent fetch of Mastodon statuses - aborting"))
		}

		currentAccountState.LastHomeStatusID = newStatusID
		if err := s.st.SetAccountState(ctx, txn, currentAccountState); err != nil {
			return err
		}

		// InsertStatuses updates streamState IN PLACE and persists it.
		if err := s.st.InsertStatuses(ctx, txn, accountState.ASID, streamState, timeline, filters); err != nil {
			return err
		}
		resp.StreamInfo = streamState.ToStreamInfo()
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Pagination got updated.
	if pg.MinID != newStatusID {
		// Either there is a mismatch in the data or no `Link` was returned
		// - in either case, we don't know enough to safely continue.
		glog.Infof("no returned MinID / ID mismatch, stopping fetch")
		resp.Status = pb.FetchResponse_DONE
	}
	if pg.MaxID == "" || pg.MaxID == pg.MinID {
		// We've reached the end - either nothing was fetched, or just the
		// latest ones.
		resp.Status = pb.FetchResponse_DONE
	}
	if len(timeline) == 0 {
		// Nothing was returned, assume it is because we've reached the end.
		resp.Status = pb.FetchResponse_DONE
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) Search(ctx context.Context, req *connect.Request[pb.SearchRequest]) (*connect.Response[pb.SearchResponse], error) {
	uid, err := s.isLogged(ctx)
	if err != nil {
		return nil, err
	}

	var results []*storage.Item
	var accountState *storage.AccountState
	err = s.st.InTxnRO(ctx, func(ctx context.Context, txn storage.SQLReadOnly) error {
		var err error
		accountState, err = s.st.AccountStateByUID(ctx, txn, uid)
		if err != nil {
			return err
		}

		results, err = s.st.SearchByStatusID(ctx, txn, uid, mastodon.ID(req.Msg.GetStatusId()))
		return err
	})
	if err != nil {
		return nil, err
	}

	// TODO: account is potentially per status, while it is currently considered per user.
	accountStateProto := accountState.ToAccountProto()
	resp := &pb.SearchResponse{}
	for _, item := range results {

		raw, err := json.Marshal(item.Status)
		if err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, &pb.Item{
			Status:   &pb.MastodonStatus{Content: string(raw)},
			Position: item.Position,
			Account:  accountStateProto,
		})
	}
	return connect.NewResponse(resp), nil
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
