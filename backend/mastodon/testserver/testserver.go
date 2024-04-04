// Package testserver implements a barebone Mastodon server for testing.
package testserver

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"slices"

	"github.com/Palats/mastopoof/backend/mastodon"
	"github.com/golang/glog"
)

type Server struct {
	// Ordered list of Mastodon statuses to serve.
	statuses []*mastodon.Status
}

func New(statusesFS fs.FS) (*Server, error) {
	filenames, err := fs.Glob(statusesFS, "*.json")
	if err != nil {
		return nil, err
	}

	var statuses []*mastodon.Status

	for _, filename := range filenames {
		glog.Infof("Testserver: including %s", filename)
		raw, err := fs.ReadFile(statusesFS, filename)
		if err != nil {
			return nil, fmt.Errorf("unable to open %s: %w", filename, err)
		}
		status := &mastodon.Status{}
		if err := json.Unmarshal(raw, status); err != nil {
			return nil, fmt.Errorf("unable to decode %s as status json: %w", filename, err)
		}
		statuses = append(statuses, status)
	}

	slices.SortFunc(statuses, func(a, b *mastodon.Status) int {
		// TODO: parse into integer
		return cmp.Compare(a.ID, a.ID)
	})

	s := &Server{
		statuses: statuses,
	}
	return s, nil
}

func (s *Server) RegisterOn(mux *http.ServeMux) {
	mux.HandleFunc("/oauth/token", s.serveOAuthToken)
	mux.HandleFunc("/api/v1/apps", s.serveAPIApps)
	mux.HandleFunc("/api/v1/accounts/verify_credentials", s.serverAPIAccountsVerifyCredentials)
	mux.HandleFunc("/api/v1/timelines/home", s.serveAPITimelinesHome)
}

func (s *Server) returnJSON(w http.ResponseWriter, _ *http.Request, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	w.Write(raw)
}

// https://docs.joinmastodon.org/methods/oauth/#token
func (s *Server) serveOAuthToken(w http.ResponseWriter, req *http.Request) {
	s.returnJSON(w, req, map[string]any{
		"access_token": "ZA-Yj3aBD8U8Cm7lKUp-lm9O9BmDgdhHzDeqsY8tlL0",
		"token_type":   "Bearer",
		"scope":        "read write follow push",
		"created_at":   1573979017,
	})
}

// https://docs.joinmastodon.org/methods/apps/#create
func (s *Server) serveAPIApps(w http.ResponseWriter, req *http.Request) {
	s.returnJSON(w, req, map[string]any{
		"id":            "1234",
		"name":          "test app",
		"website":       "",
		"redirect_uri":  "urn:ietf:wg:oauth:2.0:oob",
		"client_id":     "TWhM-tNSuncnqN7DBJmoyeLnk6K3iJJ71KKXxgL1hPM",
		"client_secret": "ZEaFUFmF0umgBX1qKJDjaU99Q31lDkOU8NutzTOoliw",
	})
}

// https://docs.joinmastodon.org/methods/accounts/#verify_credentials
func (s *Server) serverAPIAccountsVerifyCredentials(w http.ResponseWriter, req *http.Request) {
	s.returnJSON(w, req, map[string]any{
		"id":              "14715",
		"username":        "testuser1",
		"acct":            "testuser1",
		"display_name":    "Test User 1",
		"locked":          false,
		"bot":             false,
		"created_at":      "2016-11-24T10:02:12.085Z",
		"note":            "Plenty of notes",
		"url":             "https://mastodon.social/@trwnh",
		"avatar":          "https://files.mastodon.social/accounts/avatars/000/014/715/original/34aa222f4ae2e0a9.png",
		"avatar_static":   "https://files.mastodon.social/accounts/avatars/000/014/715/original/34aa222f4ae2e0a9.png",
		"header":          "https://files.mastodon.social/accounts/headers/000/014/715/original/5c6fc24edb3bb873.jpg",
		"header_static":   "https://files.mastodon.social/accounts/headers/000/014/715/original/5c6fc24edb3bb873.jpg",
		"followers_count": 821,
		"following_count": 178,
		"statuses_count":  33120,
		"last_status_at":  "2019-11-24T15:49:42.251Z",
	})
}

// https://docs.joinmastodon.org/methods/timelines/#home
func (s *Server) serveAPITimelinesHome(w http.ResponseWriter, req *http.Request) {
	s.returnJSON(w, req, s.statuses)
}
