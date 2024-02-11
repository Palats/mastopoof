package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/alexedwards/scs/v2"

	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
)

type Server struct {
	st             *storage.Storage
	mux            *http.ServeMux
	sessionManager *scs.SessionManager
}

func New(st *storage.Storage, sm *scs.SessionManager) *Server {
	s := &Server{
		st:             st,
		sessionManager: sm,
		mux:            http.NewServeMux(),
	}
	return s
}

func (s *Server) isLogged(ctx context.Context) error {
	userID := s.sessionManager.GetString(ctx, "userid")
	if userID == "" {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("oh noes"))
	}
	return nil
}

func (s *Server) Ping(ctx context.Context, req *connect.Request[pb.PingRequest]) (*connect.Response[pb.PingResponse], error) {
	resp := connect.NewResponse(&pb.PingResponse{
		Msg: fmt.Sprintf("Got: %v", req.Msg),
	})
	return resp, nil
}

func (s *Server) Login(ctx context.Context, req *connect.Request[pb.LoginRequest]) (*connect.Response[pb.LoginResponse], error) {
	err := s.sessionManager.RenewToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to renew token: %w", err)
	}

	stid := req.Msg.GetTmpStid()

	if stid == 0 {
		// Trying to login only based on existing session.
		if err := s.isLogged(ctx); err != nil {
			return nil, err
		}
	} else {
		s.sessionManager.Put(ctx, "userid", "autologin")
	}
	return connect.NewResponse(&pb.LoginResponse{
		UserInfo: &pb.UserInfo{DefaultStid: 1},
	}), nil
}

func (s *Server) Fetch(ctx context.Context, req *connect.Request[pb.FetchRequest]) (*connect.Response[pb.FetchResponse], error) {
	stid := int64(1)
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
	stid := int64(1)

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
