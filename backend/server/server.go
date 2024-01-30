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

func (s *Server) Fetch(ctx context.Context, req *connect.Request[pb.FetchRequest]) (*connect.Response[pb.FetchResponse], error) {
	lid := int64(1)
	resp := &pb.FetchResponse{}

	var err error
	var fetchResult *storage.FetchResult
	switch req.Msg.Direction {
	case pb.FetchRequest_FORWARD, pb.FetchRequest_DEFAULT:
		fetchResult, err = s.st.FetchForward(ctx, lid, req.Msg.Position)
		if err != nil {
			return nil, err
		}
		resp.ForwardState = pb.FetchResponse_PARTIAL
		if fetchResult.HasLast {
			resp.ForwardState = pb.FetchResponse_DONE
		}
	case pb.FetchRequest_BACKWARD:
		fetchResult, err = s.st.FetchBackward(ctx, lid, req.Msg.Position)
		if err != nil {
			return nil, err
		}
		// Looking backward never checks for potential extra statuses to insert
		// into the stream, so it cannot say anything about.
		resp.ForwardState = pb.FetchResponse_UNKNOWN
	default:
		return nil, fmt.Errorf("unknown direction %v", req.Msg.Direction)
	}

	resp.LastRead = fetchResult.ListingState.LastRead
	resp.LastPosition = fetchResult.ListingState.LastPosition
	resp.RemainingPool = fetchResult.ListingState.Remaining
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
	lid := int64(1)

	listingState, err := s.st.ListingState(ctx, s.st.DB, lid)
	if err != nil {
		return nil, err
	}

	v := req.Msg.GetLastRead()
	if v < listingState.FirstPosition-1 || v > listingState.LastPosition {
		return nil, fmt.Errorf("position %d is invalid", v)
	}
	listingState.LastRead = v
	if err := s.st.SetListingState(ctx, s.st.DB, listingState); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.SetReadResponse{}), nil
}
