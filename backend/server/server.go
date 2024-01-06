package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"connectrpc.com/connect"
	"github.com/Palats/mastopoof/backend/storage"
	"github.com/golang/glog"

	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
)

type httpErr int

func (herr httpErr) Error() string {
	return fmt.Sprintf("%d: %s", int(herr), http.StatusText(int(herr)))
}

func httpFunc(f func(w http.ResponseWriter, r *http.Request) error) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		err := f(w, r)
		if err != nil {
			var herr httpErr
			if errors.As(err, &herr) {
				http.Error(w, err.Error(), int(herr))
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
	}
}

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
	s.mux.HandleFunc("/statusat", httpFunc(s.serveStatusAt))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	glog.Infof("request %v", r.URL)
	s.mux.ServeHTTP(w, r)
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

// serveStatusAt returns the status at the given position, if it exists.
// Query args:
//
//	position: index of the status to load.
//
// JSON Response:
//
//	OpenPosition
func (s *Server) serveStatusAt(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	rawPosition := r.URL.Query().Get("position")
	if rawPosition == "" {
		return fmt.Errorf("missing 'position' argument: %w", httpErr(http.StatusBadRequest))
	}
	position, err := strconv.ParseInt(rawPosition, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid 'position' argument: %v; %w", err, httpErr(http.StatusBadRequest))
	}

	lid := int64(1)
	status, err := s.st.StatusAt(ctx, lid, position)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(status)
}
