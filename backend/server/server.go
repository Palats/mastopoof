package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"github.com/Palats/mastopoof/backend/storage"

	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
)

type Server struct {
	st       *storage.Storage
	authInfo *storage.AuthInfo
	mux      *http.ServeMux
}

func New(st *storage.Storage, authInfo *storage.AuthInfo) *Server {
	s := &Server{
		st:       st,
		authInfo: authInfo,
		mux:      http.NewServeMux(),
	}
	return s
}

func (s *Server) Ping(ctx context.Context, req *connect.Request[pb.PingRequest]) (*connect.Response[pb.PingResponse], error) {
	resp := connect.NewResponse(&pb.PingResponse{
		Msg: fmt.Sprintf("Got: %v", req.Msg),
	})
	return resp, nil
}

func (s *Server) InitialStatuses(ctx context.Context, req *connect.Request[pb.InitialStatusesRequest]) (*connect.Response[pb.InitialStatusesResponse], error) {
	resp := &pb.InitialStatusesResponse{}

	lid := int64(1)

	opened, err := s.st.Opened(ctx, lid)
	if err != nil {
		return nil, err
	}
	for _, openedStatus := range opened {
		raw, err := json.Marshal(openedStatus.Status)
		if err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, &pb.Item{
			Status:   &pb.MastodonStatus{Content: string(raw)},
			Position: openedStatus.Position,
		})
	}

	lstate, err := s.st.ListingState(ctx, s.st.DB, lid)
	if err != nil {
		return nil, err
	}
	resp.LastRead = lstate.LastRead

	return connect.NewResponse(resp), nil
}

func (s *Server) GetStatus(ctx context.Context, req *connect.Request[pb.GetStatusRequest]) (*connect.Response[pb.GetStatusResponse], error) {
	lid := int64(1)
	ost, err := s.st.StatusAt(ctx, lid, req.Msg.Position)
	if err != nil {
		return nil, err
	}

	if ost == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no status at position %d", req.Msg.Position))
	}

	resp := &pb.GetStatusResponse{}
	raw, err := json.Marshal(ost.Status)
	if err != nil {
		return nil, err
	}
	resp.Item = &pb.Item{
		Status:   &pb.MastodonStatus{Content: string(raw)},
		Position: ost.Position,
	}
	return connect.NewResponse(resp), nil
}
