package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Palats/mastopoof/backend/storage"
	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"
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
	s.mux.HandleFunc("/list", httpFunc(s.serveList))
	s.mux.HandleFunc("/opened", httpFunc(s.serveOpened))
	s.mux.HandleFunc("/statusat", httpFunc(s.serveStatusAt))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	glog.Infof("request %v", r.URL)
	s.mux.ServeHTTP(w, r)
}

func (s *Server) serveList(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	rows, err := s.st.DB.QueryContext(ctx, `SELECT status FROM statuses WHERE uid = ?`, s.authInfo.UID)
	if err != nil {
		return err
	}

	data := []any{}
	for rows.Next() {
		var jsonString string
		if err := rows.Scan(&jsonString); err != nil {
			return err
		}
		var status mastodon.Status
		if err := json.Unmarshal([]byte(jsonString), &status); err != nil {
			return err
		}

		data = append(data, status)
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(data)
}

type OpenedResponse struct {
	LastRead int64                 `json:"last_read"`
	Statuses []*storage.OpenStatus `json:"statuses"`
}

// serveOpened returns the list of status currently opened for the user.
func (s *Server) serveOpened(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var resp OpenedResponse

	lid := int64(1)

	opened, err := s.st.Opened(ctx, lid)
	if err != nil {
		return err
	}
	resp.Statuses = opened

	lstate, err := s.st.ListingState(ctx, s.st.DB, lid)
	if err != nil {
		return err
	}
	resp.LastRead = lstate.LastRead

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(resp)
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
