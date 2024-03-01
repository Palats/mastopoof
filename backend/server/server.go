package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/alexedwards/scs/v2"
	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"

	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
)

type Server struct {
	st             *storage.Storage
	mux            *http.ServeMux
	autoLogin      int64
	sessionManager *scs.SessionManager
	getRedirectURI func(string) string
}

func New(st *storage.Storage, sm *scs.SessionManager, autoLogin int64, getRedirectURI func(string) string) *Server {
	s := &Server{
		st:             st,
		sessionManager: sm,
		autoLogin:      autoLogin,
		mux:            http.NewServeMux(),
		getRedirectURI: getRedirectURI,
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
func (s *Server) verifyStID(ctx context.Context, stid int64) error {
	userID := s.sessionManager.GetInt64(ctx, "userid")
	userState, err := s.st.UserState(ctx, s.st.DB, userID)
	if err != nil {
		return err
	}
	if userState.DefaultStID != stid {
		return connect.NewError(connect.CodePermissionDenied, errors.New("stream access denied"))
	}
	return nil
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
	serverAddr := req.Msg.ServerAddr
	if !strings.HasPrefix(serverAddr, "https://") {
		return nil, fmt.Errorf("Mastodon server address should start with https:// ; got: %q", serverAddr)
	}
	redirectURI := s.getRedirectURI(serverAddr)

	// TODO: split transactions to avoid remote requests in the middle.
	txn, err := s.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	ss, err := s.st.ServerState(ctx, txn, serverAddr, redirectURI)
	if errors.Is(err, storage.ErrNotFound) {
		glog.Infof("Creating server state for %q", serverAddr)
		ss, err = s.st.CreateServerState(ctx, txn, serverAddr, redirectURI)
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
			Server:       ss.ServerAddr,
			ClientName:   "mastopoof",
			Scopes:       "read",
			Website:      "https://github.com/Palats/mastopoof",
			RedirectURIs: redirectURI,
		})
		if err != nil {
			return nil, fmt.Errorf("unable to register app on server %s: %w", ss.ServerAddr, err)
		}
		ss.ClientID = app.ClientID
		ss.ClientSecret = app.ClientSecret
		ss.AuthURI = app.AuthURI
		ss.RedirectURI = redirectURI

		if err := s.st.SetServerState(ctx, txn, ss); err != nil {
			return nil, err
		}
	}

	if err := txn.Commit(); err != nil {
		return nil, err
	}

	return connect.NewResponse(&pb.AuthorizeResponse{
		AuthorizeAddr: ss.AuthURI,
	}), nil
}

func (s *Server) Token(ctx context.Context, req *connect.Request[pb.TokenRequest]) (*connect.Response[pb.TokenResponse], error) {
	// TODO: sanitization of server addr to be factorized with Authorize.
	serverAddr := req.Msg.ServerAddr
	if !strings.HasPrefix(serverAddr, "https://") {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Mastodon server address should start with https:// ; got: %q", serverAddr))
	}
	redirectURI := s.getRedirectURI(serverAddr)

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

	serverState, err := s.st.ServerState(ctx, txn, serverAddr, redirectURI)
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

	err = client.AuthenticateToken(ctx, authCode, redirectURI)
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
		userState, err := s.st.CreateUserState(ctx, txn)
		if err != nil {
			return nil, err
		}
		// And then create the mastodon account state.
		accountState, err = s.st.CreateAccountState(ctx, txn, userState.UID, serverAddr, string(accountID), string(username))
		if err != nil {
			return nil, err
		}

		// Also, create a stream.
		stID, err := s.st.CreateStreamState(ctx, txn, userState.UID)
		if err != nil {
			return nil, err
		}
		userState.DefaultStID = stID
		if err := s.st.SetUserState(ctx, txn, userState); err != nil {
			return nil, err
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

func (s *Server) Fetch(ctx context.Context, req *connect.Request[pb.FetchRequest]) (*connect.Response[pb.FetchResponse], error) {
	stid := req.Msg.Stid
	if err := s.verifyStID(ctx, stid); err != nil {
		return nil, err
	}

	resp := &pb.FetchResponse{}

	var err error
	var fetchResult *storage.FetchResult
	switch req.Msg.Direction {
	case pb.FetchRequest_FORWARD, pb.FetchRequest_DEFAULT:
		fetchResult, err = s.st.FetchForward(ctx, stid, req.Msg.Position)
		if err != nil {
			return nil, err
		}
		resp.ForwardState = pb.FetchResponse_PARTIAL
		if fetchResult.HasLast {
			resp.ForwardState = pb.FetchResponse_DONE
		}
	case pb.FetchRequest_BACKWARD:
		fetchResult, err = s.st.FetchBackward(ctx, stid, req.Msg.Position)
		if err != nil {
			return nil, err
		}
		// Looking backward never checks for potential extra statuses to insert
		// into the stream, so it cannot say anything about.
		resp.ForwardState = pb.FetchResponse_UNKNOWN
	default:
		return nil, fmt.Errorf("unknown direction %v", req.Msg.Direction)
	}

	resp.LastRead = fetchResult.StreamState.LastRead
	resp.LastPosition = fetchResult.StreamState.LastPosition
	resp.RemainingPool = fetchResult.StreamState.Remaining
	resp.BackwardState = pb.FetchResponse_PARTIAL
	if fetchResult.HasFirst {
		resp.BackwardState = pb.FetchResponse_DONE
	}

	if len(fetchResult.Items) > 0 {
		resp.BackwardPosition = fetchResult.Items[0].Position
		resp.ForwardPosition = fetchResult.Items[len(fetchResult.Items)-1].Position
	}

	for _, item := range fetchResult.Items {
		raw, err := json.Marshal(item.Status)
		if err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, &pb.Item{
			Status:   &pb.MastodonStatus{Content: string(raw)},
			Position: item.Position,
		})
	}

	return connect.NewResponse(resp), nil
}

func (s *Server) SetRead(ctx context.Context, req *connect.Request[pb.SetReadRequest]) (*connect.Response[pb.SetReadResponse], error) {
	stid := req.Msg.Stid
	if err := s.verifyStID(ctx, stid); err != nil {
		return nil, err
	}

	streamState, err := s.st.StreamState(ctx, s.st.DB, stid)
	if err != nil {
		return nil, err
	}

	v := req.Msg.GetLastRead()
	if v < streamState.FirstPosition-1 || v > streamState.LastPosition {
		return nil, fmt.Errorf("position %d is invalid", v)
	}
	streamState.LastRead = v
	if err := s.st.SetStreamState(ctx, s.st.DB, streamState); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.SetReadResponse{}), nil
}
