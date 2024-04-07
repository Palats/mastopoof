// Package testserver implements a barebone Mastodon server for testing.
package testserver

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strconv"

	"github.com/Palats/mastopoof/backend/mastodon"
	"github.com/golang/glog"
)

func cmpStatusID(s1 *mastodon.Status, s2 *mastodon.Status) int {
	// TODO: that's horribly inefficient, should be maintained with the status.
	id1, err := strconv.ParseInt(string(s1.ID), 10, 64)
	if err != nil {
		panic(err)
	}
	id2, err := strconv.ParseInt(string(s2.ID), 10, 64)
	if err != nil {
		panic(err)
	}
	if id1 < id2 {
		return -1
	}
	if id1 > id2 {
		return 1
	}
	return 0
}

func parseID(s *mastodon.Status) (int64, error) {
	id, err := strconv.ParseInt(string(s.ID), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse status ID %q: %w", s.ID, err)
	}
	return id, nil
}

type Item struct {
	ID     int64
	Status *mastodon.Status
}

func newItem(s *mastodon.Status) (*Item, error) {
	id, err := parseID(s)
	if err != nil {
		return nil, err
	}
	return &Item{
		ID:     id,
		Status: s,
	}, nil
}

func cmpItems(i1 *Item, i2 *Item) int {
	if i1.ID < i2.ID {
		return -1
	}
	if i1.ID > i2.ID {
		return 1
	}
	return 0
}

type Server struct {
	// Ordered list of Mastodon statuses to serve.
	items []*Item
}

func New() *Server {
	return &Server{}
}

func (s *Server) AddStatus(status *mastodon.Status) error {
	item, err := newItem(status)
	if err != nil {
		return err
	}
	idx, found := slices.BinarySearchFunc(s.items, item, cmpItems)
	if found {
		return fmt.Errorf("duplicate ID %d", item.ID)
	}
	s.items = slices.Insert(s.items, idx, item)
	return nil
}

func (s *Server) AddJSONStatuses(statusesFS fs.FS) error {
	filenames, err := fs.Glob(statusesFS, "*.json")
	if err != nil {
		return err
	}
	for _, filename := range filenames {
		glog.Infof("Testserver: including %s", filename)
		raw, err := fs.ReadFile(statusesFS, filename)
		if err != nil {
			return fmt.Errorf("unable to open %s: %w", filename, err)
		}
		status := &mastodon.Status{}
		if err := json.Unmarshal(raw, status); err != nil {
			return fmt.Errorf("unable to decode %s as status json: %w", filename, err)
		}

		if err := s.AddStatus(status); err != nil {
			return fmt.Errorf("unable to add status from file %s: %w", filename, err)
		}
	}
	return nil
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
	statuses := []*mastodon.Status{}
	for _, item := range s.items {
		statuses = append(statuses, item.Status)
	}
	s.returnJSON(w, req, statuses)
}
