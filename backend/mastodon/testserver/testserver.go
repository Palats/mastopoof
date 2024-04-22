// Package testserver implements a barebone Mastodon server for testing.
package testserver

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Palats/mastopoof/backend/mastodon"
	"github.com/golang/glog"
)

func parseID(statusID mastodon.ID) (int64, error) {
	id, err := strconv.ParseInt(string(statusID), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse status ID %q: %w", statusID, err)
	}
	return id, nil
}

func NewFakeStatus(statusID mastodon.ID, accountID mastodon.ID) *mastodon.Status {
	username := fmt.Sprintf("fakeuser-%s", accountID)
	status := &mastodon.Status{
		ID:  statusID,
		URI: fmt.Sprintf("https://example.com/users/%s/statuses/%s", username, statusID),
		URL: fmt.Sprintf("https://example.com/@%s/%s", username, statusID),
		Account: mastodon.Account{
			ID:             accountID,
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
		Content:    fmt.Sprintf("Status content for user %s, status %s", username, statusID),
		Visibility: "public",
	}
	return status
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
	// If present, block on list request.
	// When a request arrives, it will send a `chan struct{}` over the provided channel.
	// The receiver must then close the sent channel to indicate that the test server
	// can continue serving.
	// Must be set before any request is started.
	TestBlockList chan chan struct{}

	m sync.Mutex
	// Ordered list of Mastodon statuses to serve.
	// The list is ordered by increase status.ID - thus meaning oldest status first.
	items []*Item
	// To differentiate between each fake status being added.
	fakeCounter int64
	// Introduce a waiting delay before answering listing statuses.
	listDelay time.Duration
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

func (s *Server) addStatus(status *mastodon.Status) error {
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

func (s *Server) SetListDelay(delay time.Duration) {
	s.m.Lock()
	s.listDelay = delay
	s.m.Unlock()
}

func (s *Server) AddJSONStatuses(statusesFS fs.FS) error {
	s.m.Lock()
	defer s.m.Unlock()

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

		if err := s.addStatus(status); err != nil {
			return fmt.Errorf("unable to add status from file %s: %w", filename, err)
		}
	}
	return nil
}

func (s *Server) AddFakeStatus() error {
	s.m.Lock()
	defer s.m.Unlock()

	id := "1"
	if len(s.items) > 0 {
		id = strconv.FormatInt(s.items[len(s.items)-1].ID+1, 10)
	}
	gen := s.fakeCounter
	s.fakeCounter += 1

	status := NewFakeStatus(mastodon.ID(id), mastodon.ID(strconv.FormatInt(gen, 10)))
	return s.addStatus(status)
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
	s.m.Lock()
	defer s.m.Unlock()

	s.returnJSON(w, req, map[string]any{
		"access_token": "ZA-Yj3aBD8U8Cm7lKUp-lm9O9BmDgdhHzDeqsY8tlL0",
		"token_type":   "Bearer",
		"scope":        "read write follow push",
		"created_at":   1573979017,
	})
}

// https://docs.joinmastodon.org/methods/apps/#create
func (s *Server) serveAPIApps(w http.ResponseWriter, req *http.Request) {
	s.m.Lock()
	defer s.m.Unlock()

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
	s.m.Lock()
	defer s.m.Unlock()

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
	ctx := req.Context()

	if s.TestBlockList != nil {
		ch := make(chan struct{})
		s.TestBlockList <- ch
		select {
		case <-ch:
		case <-ctx.Done():
			http.Error(w, "interrupted", http.StatusInternalServerError)
			return
		}
	}

	s.m.Lock()
	delay := s.listDelay
	s.m.Unlock()
	if delay > 0 {
		glog.Infof("list delay: %v", delay)
		time.Sleep(delay)
	}

	s.m.Lock()
	defer s.m.Unlock()

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
	// Return statuses from highest ID to lowest - i.e., in reverse chronological order,
	// which is likely similar to Mastodon.
	for i := lastIdx - 1; i >= firstIdx; i-- {
		statuses = append(statuses, s.items[i].Status)
	}

	// See https://docs.joinmastodon.org/api/guidelines/#pagination for how `Link` header
	// is used by Mastodon. 2 links can be provided:
	//  - `next` should get older results. Older results means lower ID. It seems that only `max_id` is expected on this URL.
	//  - `prev` should get new results. This means higher ID. It seems that `since_id` and `min_id` are used on this URL.
	// The idea seems to be that Mastodon returns results in reverse
	// chronological order - i.e., from most recent to oldest. In turn, it means
	// that next gives older results.
	var linkEntries []string

	// 'next' returns older results - thus results with smaller Status ID, which means lower
	// index in the s.items array. The result is clamped by `max_id`. The `max_id` is excluded
	// from the result, so that can be directly the oldest status returned here.
	if firstIdx >= 0 && firstIdx < len(s.items) && firstIdx < lastIdx {
		var uNext url.URL
		uNext.Scheme = "https"
		uNext.Host = "localhost"
		uNext.Path = "/api/v1/timelines/home"
		q := uNext.Query()
		q.Set("max_id", string(s.items[firstIdx].Status.ID))
		uNext.RawQuery = q.Encode()
		linkEntries = append(linkEntries, fmt.Sprintf("<%s>; rel=\"next\"", uNext.String()))
	}

	// 'prev' returns newer results - thus results with higher status ID, which means
	// higher index in the sorted s.items array.
	// `min_id` is excluded from the results, so it can be directly the most recent
	// status which is returned there. Same for `since_id`.
	// Note that `lastIdx` is excluded from result.
	if lastIdx-1 >= 0 && lastIdx-1 < len(s.items) && firstIdx < lastIdx {
		var uPrev url.URL
		uPrev.Scheme = "https"
		uPrev.Host = "localhost"
		uPrev.Path = "/api/v1/timelines/home"

		id := string(s.items[lastIdx-1].Status.ID)
		q := uPrev.Query()
		q.Set("min_id", id)
		// Don't set `since_id`, like official Mastodon server.
		uPrev.RawQuery = q.Encode()
		linkEntries = append(linkEntries, fmt.Sprintf("<%s>; rel=\"prev\"", uPrev.String()))
	}

	if len(linkEntries) > 0 {
		w.Header().Set("Link", strings.Join(linkEntries, ", "))
	}

	s.returnJSON(w, req, statuses)
}
