// Package testserver implements a barebone Mastodon server for testing.
package testserver

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strconv"
	"time"

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

func parseID(statusID mastodon.ID) (int64, error) {
	id, err := strconv.ParseInt(string(statusID), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse status ID %q: %w", statusID, err)
	}
	return id, nil
}

type Item struct {
	ID     int64
	Status *mastodon.Status
}

func newItem(s *mastodon.Status) (*Item, error) {
	id, err := parseID(s.ID)
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
	// To differentiate between each fake status being added.
	fakeCounter int64
}

func New() *Server {
	return &Server{}
}

func (s *Server) itemIdx(key string) (int, error) {
	if key == "" {
		return -1, nil
	}
	id, err := parseID(mastodon.ID(key))
	if err != nil {
		return -1, fmt.Errorf("%q is not a valid status ID: %w", key, err)
	}

	item := &Item{ID: id}
	idx, found := slices.BinarySearchFunc(s.items, item, cmpItems)
	if !found {
		return -1, nil
	}
	return idx, nil
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

func (s *Server) AddFakeStatus() error {
	id := "1"
	if len(s.items) > 0 {
		id = strconv.FormatInt(s.items[len(s.items)-1].ID+1, 10)
	}
	gen := s.fakeCounter
	s.fakeCounter += 1

	username := fmt.Sprintf("fakeuser%d", gen)
	status := &mastodon.Status{
		ID:  mastodon.ID(id),
		URI: fmt.Sprintf("https://example.com/users/%s/statuses/%s", username, id),
		URL: fmt.Sprintf("https://example.com/@%s/%s", username, id),
		Account: mastodon.Account{
			ID:             mastodon.ID(strconv.FormatInt(gen, 10)),
			Username:       username,
			Acct:           fmt.Sprintf("%s@example.com", username),
			URL:            fmt.Sprintf("https://example.com/%s", username),
			DisplayName:    fmt.Sprintf("Account of user %s", username),
			Note:           "Fake user",
			Avatar:         "http://www.gravatar.com/avatar/?d=mp",
			AvatarStatic:   "http://www.gravatar.com/avatar/?d=mp",
			CreatedAt:      time.Now(),
			StatusesCount:  1,
			FollowersCount: 1,
			FollowingCount: 1,
		},
		CreatedAt:  time.Now(),
		Content:    fmt.Sprintf("Status content for fake status %d", gen),
		Visibility: "public",
	}

	return s.AddStatus(status)
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
	maxIDidx, err := s.itemIdx(req.URL.Query().Get("max_id"))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid max_id parameter: %v", err), http.StatusBadRequest)
		return
	}

	sinceIDidx, err := s.itemIdx(req.URL.Query().Get("since_id"))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid since_id parameter: %v", err), http.StatusBadRequest)
		return
	}

	minIDidx, err := s.itemIdx(req.URL.Query().Get("min_id"))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid min_id parameter: %v", err), http.StatusBadRequest)
		return
	}

	limit := 20
	sLimit := req.URL.Query().Get("limit")
	if sLimit != "" {
		l64, err := strconv.ParseInt(sLimit, 10, strconv.IntSize)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid limit parameter: %v", err), http.StatusBadRequest)
			return
		}
		limit = int(l64)
	}

	firstIdx := 0           // Included
	lastIdx := len(s.items) // Not included
	if minIDidx >= 0 {
		firstIdx = minIDidx
		if sinceIDidx >= 0 && sinceIDidx > firstIdx {
			firstIdx = sinceIDidx
		}
		// min_id and since_id are not included when set.
		firstIdx++

		lastIdx = firstIdx + limit
		if maxIDidx >= 0 && maxIDidx <= lastIdx {
			lastIdx = maxIDidx
		}
		if lastIdx > len(s.items) {
			lastIdx = len(s.items)
		}
	} else {
		// min_idx is not set, so we go backward from recent statuses.
		lastIdx = len(s.items)
		if maxIDidx >= 0 && maxIDidx <= lastIdx {
			lastIdx = maxIDidx
		}

		firstIdx = lastIdx - limit
		if sinceIDidx >= 0 && sinceIDidx >= firstIdx {
			firstIdx = sinceIDidx + 1
		}
		if firstIdx < 0 {
			firstIdx = 0
		}
	}

	statuses := []*mastodon.Status{}
	for i := firstIdx; i < lastIdx; i++ {
		statuses = append(statuses, s.items[i].Status)
	}
	s.returnJSON(w, req, statuses)
}
