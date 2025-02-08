package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/alexedwards/scs/v2"
	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"
	"google.golang.org/protobuf/proto"

	mpdata "github.com/Palats/mastopoof/proto/data"
	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
	"github.com/Palats/mastopoof/proto/gen/mastopoof/mastopoofconnect"
	settingspb "github.com/Palats/mastopoof/proto/gen/mastopoof/settings"
	stpb "github.com/Palats/mastopoof/proto/gen/mastopoof/storage"
)

const AppMastodonScopes = "read write push"

// URI to use in oauth to indicate that no redirection should happen but instead
// the user should copy/paste the auth code explictly.
const OutOfBandURI = "urn:ietf:wg:oauth:2.0:oob"

// validateAddress verifies that a Mastodon server adress is vaguely looking good.
func validateAddress(addr string) error {
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		return fmt.Errorf("mastodon server address should start with https:// or http:// ; got: %s", addr)
	}
	return nil
}

// AppRegistry manages app registration on Mastodon servers
// mastodon clients.
type AppRegistry struct {
	st     *storage.Storage
	client *http.Client
}

func NewAppRegistry(st *storage.Storage) *AppRegistry {
	return &AppRegistry{
		st:     st,
		client: http.DefaultClient,
	}
}

func (appreg *AppRegistry) Register(ctx context.Context, serverAddr string, selfURL *url.URL) (*stpb.AppRegState, error) {
	appRegInfo := appreg.appRegInfo(serverAddr, selfURL)

	var appRegState *stpb.AppRegState
	err := appreg.st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
		var err error
		appRegState, err = appreg.st.AppRegState(ctx, txn, appRegInfo)
		if errors.Is(err, storage.ErrNotFound) {
			glog.Infof("Creating server state for %q", appRegInfo.ServerAddr)
			appRegState, err = appreg.callRegister(ctx, appRegInfo)
			if err != nil {
				return err
			}
			// RegisterApp does not persists it, don't forget to do it.
			if err := appreg.st.CreateAppRegState(ctx, txn, appRegState); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		return nil
	})
	return appRegState, err
}

func (appreg *AppRegistry) MastodonClient(appRegState *stpb.AppRegState, accessToken string) *mastodon.Client {
	// TODO: Re-use mastodon clients.
	client := mastodon.NewClient(&mastodon.Config{
		Server:       appRegState.ServerAddr,
		ClientID:     appRegState.ClientId,
		ClientSecret: appRegState.ClientSecret,
		AccessToken:  accessToken,
	})
	// TODO: figure out if there is a way to have mastodon lib not require
	// duplicate the client struct.
	client.Client = *appreg.client
	return client
}

func (appreg *AppRegistry) appRegInfo(serverAddr string, selfURL *url.URL) *storage.AppRegInfo {
	redirectURI := OutOfBandURI

	if selfURL != nil {
		u := selfURL.JoinPath(redirectPath)
		// RedirectURI for auth must contain information about the mastodon server
		// it is about. Otherwise, when getting a code back after auth, the server
		// cannot know what it is about.
		q := u.Query()
		q.Set("host", serverAddr)
		u.RawQuery = q.Encode()
		redirectURI = u.String()
	}

	return &storage.AppRegInfo{
		ServerAddr:  serverAddr,
		RedirectURI: redirectURI,
		Scopes:      AppMastodonScopes,
	}
}

// callRegister register Mastopoof on the specified mastodon server, with the provided scopes and redirect URI.
func (appreg *AppRegistry) callRegister(ctx context.Context, nfo *storage.AppRegInfo) (*stpb.AppRegState, error) {
	// TODO: rate limiting to avoid abuse.
	// TODO: garbage collection of unused ones.
	// TODO: update redirect URIs as needed.

	glog.Infof("Registering app on server %q", nfo.ServerAddr)
	app, err := mastodon.RegisterApp(ctx, &mastodon.AppConfig{
		// TODO: figure out if there is a way to have mastodon lib not require
		// duplicate the client struct.
		Client:       *appreg.client,
		Server:       nfo.ServerAddr,
		ClientName:   "mastopoof",
		Scopes:       nfo.Scopes,
		Website:      "https://github.com/Palats/mastopoof",
		RedirectURIs: nfo.RedirectURI,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to register app on server %s: %w", nfo.ServerAddr, err)
	}

	return &stpb.AppRegState{
		Key:          nfo.Key(),
		ServerAddr:   nfo.ServerAddr,
		Scopes:       nfo.Scopes,
		RedirectUri:  nfo.RedirectURI,
		ClientId:     app.ClientID,
		ClientSecret: app.ClientSecret,
		AuthUri:      app.AuthURI,
	}, nil
}

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
	// Config to send to the frontend.
	// Do not modify once it is serving.
	FrontendConfig MastopoofConfig

	st             *storage.Storage
	inviteCode     string
	autoLogin      storage.UID
	sessionManager *scs.SessionManager
	selfURL        *url.URL
	appRegistry    *AppRegistry
}

func New(st *storage.Storage, sessionManager *scs.SessionManager, inviteCode string, autoLogin storage.UID, selfURL *url.URL, appRegistry *AppRegistry) *Server {
	s := &Server{
		FrontendConfig: MastopoofConfig{
			Src:       "server",
			DefServer: "mastodon.social",
			Invite:    inviteCode != "",
		},
		st:             st,
		sessionManager: sessionManager,
		inviteCode:     inviteCode,
		autoLogin:      autoLogin,
		selfURL:        selfURL,
		appRegistry:    appRegistry,
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

// getUserInfo builds a UserInfo proto suitable for the UI.
func (s *Server) getUserInfo(ctx context.Context, userState *stpb.UserState) (*pb.UserInfo, error) {
	userInfo := &pb.UserInfo{
		DefaultStid: userState.DefaultStid,
	}

	accountStates, err := s.st.AllAccountStateByUID(ctx, nil, storage.UID(userState.Uid))
	if err != nil {
		return nil, err
	}
	for _, accountState := range accountStates {
		userInfo.Accounts = append(userInfo.Accounts, storage.AccountStateToAccountProto(accountState))
	}

	userInfo.Settings = userState.Settings

	return userInfo, nil
}

// verifyStdID checks that the logged in user is allowed access to that
// stream.
func (s *Server) verifyStID(ctx context.Context, stid storage.StID) (*stpb.UserState, error) {
	userID, err := s.isLogged(ctx)
	if err != nil {
		return nil, err
	}
	userState, err := s.st.UserState(ctx, nil, userID)
	if err != nil {
		return nil, err
	}
	if storage.StID(userState.DefaultStid) != stid {
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
	userInfo, err := s.getUserInfo(ctx, userState)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&pb.LoginResponse{
		UserInfo: userInfo,
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

func (s *Server) UpdateSettings(ctx context.Context, req *connect.Request[pb.UpdateSettingsRequest]) (*connect.Response[pb.UpdateSettingsResponse], error) {
	userID, err := s.isLogged(ctx)
	if err != nil {
		return nil, err
	}

	// Clone the settings - not sure what is the lifecycle of the one provided
	// for the RPC, and it will be used in userstate.
	settings := proto.Clone(req.Msg.GetSettings()).(*settingspb.Settings)
	if settings == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing settings"))
	}

	if v, min := settings.GetListCount().GetValue(), mpdata.SettingsInfo().GetListCount().GetMin(); v < min {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("DefaultListCount must be at least %d; got: %d", min, v))
	}
	if v, max := settings.GetListCount().GetValue(), mpdata.SettingsInfo().GetListCount().GetMax(); v > max {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("DefaultListCount must be less or equal to %d; got: %d", max, v))
	}

	err = s.st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
		userState, err := s.st.UserState(ctx, txn, userID)
		if err != nil {
			return err
		}

		userState.Settings = settings

		return s.st.SetUserState(ctx, txn, userState)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.UpdateSettingsResponse{}), nil
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
	if err := validateAddress(serverAddr); err != nil {
		return nil, err
	}

	appRegState, err := s.appRegistry.Register(ctx, serverAddr, s.selfURL)
	if err != nil {
		return nil, err
	}

	authAddr, err := url.Parse(appRegState.ServerAddr)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unable to parse %s: %w", appRegState.ServerAddr, err))
	}
	authAddr.Path = "/oauth/authorize"
	q := authAddr.Query()
	q.Set("client_id", appRegState.ClientId)
	q.Set("redirect_uri", appRegState.RedirectUri)
	q.Set("response_type", "code")
	// TODO: narrow down scopes
	// TODO: support changing scope (or at least detecting)
	q.Set("scope", "read write")
	authAddr.RawQuery = q.Encode()

	return connect.NewResponse(&pb.AuthorizeResponse{
		AuthorizeAddr: authAddr.String(),
		OutOfBand:     appRegState.RedirectUri == OutOfBandURI,
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
	if err := validateAddress(serverAddr); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unable to validate address %s: %w", serverAddr, err))
	}

	authCode := req.Msg.AuthCode
	if authCode == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing authcode"))
	}

	// This can write to the DB. However, this is just about mastodon client registration, which is independent
	// from the rest of the state - so even if something else fail here afterward, it is fine to keep
	// around a successfull App registration.
	appRegState, err := s.appRegistry.Register(ctx, serverAddr, s.selfURL)
	if err != nil {
		return nil, err
	}
	client := s.appRegistry.MastodonClient(appRegState, "" /* accessToken */)

	err = client.AuthenticateToken(ctx, authCode, appRegState.RedirectUri)
	if err != nil {
		return nil, fmt.Errorf("unable to authenticate on server %s: %w", appRegState.ServerAddr, err)
	}

	// Now get info about the mastodon mastodonAccount so we can match it
	// to a local mastodonAccount.
	mastodonAccount, err := client.GetAccountCurrentUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("failure whena calling Mastodon AccountCurrentUser: %w", err)
	}
	accountID := mastodonAccount.ID
	if accountID == "" {
		return nil, errors.New("missing account ID")
	}
	username := mastodonAccount.Username

	var userState *stpb.UserState
	err = s.st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
		// Find the account state (Mastodon).
		accountState, err := s.st.AccountStateByAccountID(ctx, txn, serverAddr, accountID)
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
		userState, err = s.st.UserState(ctx, txn, storage.UID(accountState.Uid))
		if err != nil {
			return fmt.Errorf("unable to load userstate for UID %d: %w", accountState.Uid, err)
		}

		// Now, let's write the access token we got in the account state.
		accountState.AccessToken = client.Config.AccessToken
		if err := s.st.SetAccountState(ctx, txn, accountState); err != nil {
			return fmt.Errorf("failed to set account state %d: %w", accountState.Asid, err)
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
	s.setSessionUserID(ctx, storage.UID(userState.Uid))

	userInfo, err := s.getUserInfo(ctx, userState)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&pb.TokenResponse{
		UserInfo: userInfo,
	}), nil
}

func (s *Server) List(ctx context.Context, req *connect.Request[pb.ListRequest]) (*connect.Response[pb.ListResponse], error) {
	stid := storage.StID(req.Msg.Stid)
	userState, err := s.verifyStID(ctx, stid)
	if err != nil {
		return nil, err
	}

	accountState, err := s.st.FirstAccountStateByUID(ctx, nil, storage.UID(userState.Uid))
	if err != nil {
		return nil, err
	}
	accountStateProto := storage.AccountStateToAccountProto(accountState)

	resp := &pb.ListResponse{}

	var listResult *storage.ListResult
	switch req.Msg.Direction {
	case pb.ListRequest_DEFAULT:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing direction specification"))
	case pb.ListRequest_INITIAL:
		listResult, err = s.st.ListForward(ctx, userState, stid, req.Msg.Position, true /* isInitial */)
		if err != nil {
			return nil, err
		}
	case pb.ListRequest_FORWARD:
		listResult, err = s.st.ListForward(ctx, userState, stid, req.Msg.Position, false /* isInitial */)
		if err != nil {
			return nil, err
		}
	case pb.ListRequest_BACKWARD:
		listResult, err = s.st.ListBackward(ctx, userState, stid, req.Msg.Position)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown direction %v", req.Msg.Direction)
	}

	resp.StreamInfo = storage.StreamStateToStreamInfo(listResult.StreamState)

	if len(listResult.Items) > 0 {
		resp.BackwardPosition = listResult.Items[0].Position
		resp.ForwardPosition = listResult.Items[len(listResult.Items)-1].Position
	} else {
		// Got not result. That can happen if there are not statuses,
		// or if on initial load, the read marker was placed on the latest status - therefore
		// giving back zero status.
		// TODO: add testing
		resp.BackwardPosition = listResult.StreamState.LastPosition
		resp.ForwardPosition = listResult.StreamState.LastPosition
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
			Account:           accountStateProto,
			Meta:              item.StatusMeta,
			StreamStatusState: item.StreamStatusState,
		})
	}

	return connect.NewResponse(resp), nil
}

func (s *Server) SetRead(ctx context.Context, req *connect.Request[pb.SetReadRequest]) (*connect.Response[pb.SetReadResponse], error) {
	stid := storage.StID(req.Msg.Stid)
	if _, err := s.verifyStID(ctx, stid); err != nil {
		return nil, err
	}

	var streamState *stpb.StreamState
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
		StreamInfo: storage.StreamStateToStreamInfo(streamState),
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
	var accountState *stpb.AccountState

	err := s.st.InTxnRO(ctx, func(ctx context.Context, txn storage.SQLReadOnly) error {
		streamState, err := s.st.StreamState(ctx, txn, stid)
		if err != nil {
			return err
		}

		// For support for having multiple Mastodon account, this would
		// need to list the accounts and do the fetching from each account.
		accountState, err = s.st.FirstAccountStateByUID(ctx, txn, storage.UID(streamState.Uid))
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	appRegState, err := s.appRegistry.Register(ctx, accountState.ServerAddr, s.selfURL)
	if err != nil {
		return nil, err
	}
	client := s.appRegistry.MastodonClient(appRegState, accountState.AccessToken)

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
		MinID: mastodon.ID(accountState.LastHomeStatusId),
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

	newStatusID := mastodon.ID(accountState.LastHomeStatusId)
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

	// Get notifications count
	// Start by getting marker position on notifications to know what has been read.
	markers, err := client.GetMarkers(ctx, []string{"notifications"})
	if err != nil {
		return nil, fmt.Errorf("unable to get notification marker: %w", err)
	}
	marker := markers["notifications"]
	if marker == nil {
		return nil, fmt.Errorf("server failed to return a 'notifications' marker; got: %v", markers)
	}

	// And do request notifications.
	maxNotifs := int64(20)
	notifsPg := mastodon.Pagination{
		Limit:   maxNotifs,
		SinceID: marker.LastReadID,
	}
	notifs, err := client.GetNotifications(ctx, &notifsPg)
	if err != nil {
		return nil, fmt.Errorf("unable to list notifications: %v", err)
	}
	notifsCount := int64(len(notifs))
	notifsState := stpb.StreamState_NOTIF_EXACT
	if notifsCount >= maxNotifs {
		notifsState = stpb.StreamState_NOTIF_MORE
	}

	// Record a timestamp for reference.
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
		streamState.NotificationsState = notifsState
		streamState.NotificationsCount = notifsCount

		currentAccountState, err := s.st.FirstAccountStateByUID(ctx, txn, storage.UID(streamState.Uid))
		if err != nil {
			return fmt.Errorf("unable to verify for race conditions: %w", err)
		}
		if currentAccountState.LastHomeStatusId != accountState.LastHomeStatusId {
			return connect.NewError(connect.CodeUnavailable, errors.New("concurrent fetch of Mastodon statuses - aborting"))
		}

		currentAccountState.LastHomeStatusId = string(newStatusID)
		if err := s.st.SetAccountState(ctx, txn, currentAccountState); err != nil {
			return err
		}

		// InsertStatuses updates streamState IN PLACE and persists it.
		if err := s.st.InsertStatuses(ctx, txn, storage.ASID(accountState.Asid), streamState, timeline, filters); err != nil {
			return err
		}
		resp.StreamInfo = storage.StreamStateToStreamInfo(streamState)
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
	var accountState *stpb.AccountState
	err = s.st.InTxnRO(ctx, func(ctx context.Context, txn storage.SQLReadOnly) error {
		var err error
		accountState, err = s.st.FirstAccountStateByUID(ctx, txn, uid)
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
	accountStateProto := storage.AccountStateToAccountProto(accountState)
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
			Meta:     item.StatusMeta,
		})
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) SetStatus(ctx context.Context, req *connect.Request[pb.SetStatusRequest]) (*connect.Response[pb.SetStatusResponse], error) {
	uid, err := s.isLogged(ctx)
	if err != nil {
		return nil, err
	}

	var accountState *stpb.AccountState
	err = s.st.InTxnRO(ctx, func(ctx context.Context, txn storage.SQLReadOnly) error {
		accountState, err = s.st.FirstAccountStateByUID(ctx, txn, uid)
		if err != nil {
			return err
		}
		return nil
	})

	appRegState, err := s.appRegistry.Register(ctx, accountState.ServerAddr, s.selfURL)
	if err != nil {
		return nil, err
	}

	client := s.appRegistry.MastodonClient(appRegState, accountState.AccessToken)

	var status *mastodon.Status
	switch action := req.Msg.GetAction(); action {
	case pb.SetStatusRequest_FAVOURITE:
		status, err = client.Favourite(ctx, mastodon.ID(req.Msg.StatusId))
	case pb.SetStatusRequest_UNFAVOURITE:
		status, err = client.Unfavourite(ctx, mastodon.ID(req.Msg.StatusId))
	case pb.SetStatusRequest_REFRESH:
		status, err = client.GetStatus(ctx, mastodon.ID(req.Msg.StatusId))
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid action %v", action))
	}

	if err != nil {
		return nil, connect.NewError(connect.CodeUnknown, fmt.Errorf("unable to set favourite status for %s: %w", req.Msg.StatusId, err))
	}

	// Update status in DB.
	filters, err := client.GetFilters(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get filters: %w", err)
	}
	err = s.st.InTxnRW(ctx, func(ctx context.Context, txn storage.SQLReadWrite) error {
		return s.st.UpdateStatus(ctx, txn, storage.ASID(accountState.Asid), status, filters)
	})
	if err != nil {
		return nil, fmt.Errorf("unable to update status: %w", err)
	}

	// And return the new status.
	raw, err := json.Marshal(status)
	if err != nil {
		return nil, err
	}
	resp := &pb.SetStatusResponse{
		Status: &pb.MastodonStatus{Content: string(raw)},
	}
	return connect.NewResponse(resp), nil
}

const redirectPath = "/_redirect"

func (s *Server) RedirectHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	if req.Method != "GET" {
		http.Error(w, "invalid method", http.StatusBadRequest)
		return
	}

	// Code is provided by the Mastodon server. This is what will allow
	// to make authentified requests.
	authCode := req.URL.Query().Get("code")
	if authCode == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	// 'host' was provided as redirect_uri by mastopoof to the Mastodon server.
	// This allows to know for which Mastodon server the signup was made.
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

	p, found := strings.CutSuffix(req.URL.Path, redirectPath)
	if !found {
		fmt.Fprintf(w, "Auth done, no redirect configured.")
		return
	}

	if p == "" {
		p = "/"
	}

	http.Redirect(w, req, p, http.StatusFound)
}

func (s *Server) ConfigHandler(w http.ResponseWriter, req *http.Request) {
	encodedCfg, err := json.Marshal(s.FrontendConfig)
	if err != nil {
		http.Error(w, fmt.Sprintf("unable to encode config: %v", err), http.StatusInternalServerError)
		return
	}
	// This is loaded as a script to have priority over the main typescript codebase.
	// This way, this is loaded first, and typescript code is sure that some
	// config data is available.
	content := fmt.Sprintf("mastopoofConfig = %s;", string(encodedCfg))
	w.Header().Add("Content-Type", "text/javascript")
	_, err = w.Write([]byte(content))
	if err != nil {
		http.Error(w, fmt.Sprintf("unable to write config: %v", err), http.StatusInternalServerError)
		return
	}
}

func (s *Server) RegisterOn(mux *http.ServeMux) {
	api := http.NewServeMux()
	api.Handle(mastopoofconnect.NewMastopoofHandler(s))
	mux.Handle("/_rpc/", s.sessionManager.LoadAndSave(http.StripPrefix("/_rpc", api)))
	mux.Handle(redirectPath, s.sessionManager.LoadAndSave(http.HandlerFunc(s.RedirectHandler)))
	mux.Handle("/_config", s.sessionManager.LoadAndSave(http.HandlerFunc(s.ConfigHandler)))
}

// MastopoofConfig is data that is being sent upfront to the frontend.
// See common.ts for more details.
type MastopoofConfig struct {
	Src       string `json:"src"`
	DefServer string `json:"defServer"`
	Invite    bool   `json:"invite"`
}
