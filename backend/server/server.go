package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
