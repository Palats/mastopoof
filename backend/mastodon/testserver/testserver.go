// Package testserver implements a barebone Mastodon server for testing.
package testserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/mattn/go-mastodon"
)

func NewFakeAccount(accountID mastodon.ID, username string) mastodon.Account {
	return mastodon.Account{
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
	}
}

func NewFakeStatus(statusID mastodon.ID, accountID mastodon.ID) *mastodon.Status {
	username := fmt.Sprintf("fakeuser-%s", accountID)
	status := &mastodon.Status{
		ID:         statusID,
		URI:        fmt.Sprintf("https://example.com/users/%s/statuses/%s", username, statusID),
		URL:        fmt.Sprintf("https://example.com/@%s/%s", username, statusID),
		Account:    NewFakeAccount(accountID, username),
		CreatedAt:  time.Now(),
		Content:    fmt.Sprintf("Status content for user %s, status %s", username, statusID),
		Visibility: "public",
	}
	return status
}

func NewFakeNotification(notifID mastodon.ID, notifType string, accountSrcID mastodon.ID, status *mastodon.Status) *mastodon.Notification {
	srcUsername := fmt.Sprintf("fakenotifier-%s", accountSrcID)
	notif := &mastodon.Notification{
		ID:        notifID,
		Type:      notifType,
		CreatedAt: time.Now(),
		Account:   NewFakeAccount(accountSrcID, srcUsername),
		Status:    status,
		// Report & RelationshipSeveranceEvent missing from go-mastodon.
	}
	return notif
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
	statuses EntityList[*mastodon.Status]
	// To differentiate between each fake status being added.
	fakeCounter int64
	// Introduce a waiting delay before answering listing statuses.
	listDelay time.Duration

	notifications EntityList[*mastodon.Notification]
}

func New() *Server {
	return &Server{}
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

		if err := s.statuses.Insert(status, string(status.ID)); err != nil {
			return fmt.Errorf("unable to add status from file %s: %w", filename, err)
		}
	}
	return nil
}

func (s *Server) AddFakeStatus() error {
	s.m.Lock()
	defer s.m.Unlock()

	id := s.statuses.CreateNextID()
	gen := s.fakeCounter
	s.fakeCounter += 1

	status := NewFakeStatus(mastodon.ID(id), mastodon.ID(strconv.FormatInt(gen, 10)))
	return s.statuses.Insert(status, string(status.ID))
}

func (s *Server) AddFakeNotification() error {
	s.m.Lock()
	defer s.m.Unlock()

	if len(s.statuses.entities) == 0 {
		return errors.New("no status to notify about")
	}
	status := s.statuses.entities[len(s.statuses.entities)-1].Value
	id := s.notifications.CreateNextID()
	notif := NewFakeNotification(mastodon.ID(id), "favourite", "987", status)
	return s.notifications.Insert(notif, string(notif.ID))
}

func (s *Server) ClearNotifications() {
	s.m.Lock()
	defer s.m.Unlock()
	s.notifications.Clear()
}

func (s *Server) RegisterOn(mux *http.ServeMux) {
	mux.HandleFunc("/oauth/token", s.serveOAuthToken)
	mux.HandleFunc("/api/v1/apps", s.serveAPIApps)
	mux.HandleFunc("/api/v1/accounts/verify_credentials", s.serverAPIAccountsVerifyCredentials)
	mux.HandleFunc("/api/v1/timelines/home", s.serveAPITimelinesHome)
	mux.HandleFunc("/api/v1/filters", s.serveAPIFilters)
	mux.HandleFunc("/api/v1/notifications", s.serveAPINotifications)
	mux.HandleFunc("/api/v1/markers", s.serverAPIMarkers)
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

func (s *Server) serveAPIFilters(w http.ResponseWriter, req *http.Request) {
	s.m.Lock()
	defer s.m.Unlock()

	filters := []map[string]any{
		{
			"id":           "6191",
			"phrase":       ":eurovision2019:",
			"context":      []string{"home"},
			"whole_word":   true,
			"expires_at":   "2019-05-21T13:47:31.333Z",
			"irreversible": false,
		},
		{
			"id":     "5580",
			"phrase": "@twitter.com",
			"context": []string{
				"home",
				"notifications",
				"public",
				"thread",
			},
			"whole_word":   false,
			"expires_at":   nil,
			"irreversible": true,
		}}

	s.returnJSON(w, req, filters)
}

// https://docs.joinmastodon.org/methods/timelines/#home
func (s *Server) serveAPITimelinesHome(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	if s.TestBlockList != nil {
		blockCh := make(chan struct{})
		// Tell test that method has been called
		select {
		case s.TestBlockList <- blockCh:
		case <-ctx.Done():
			http.Error(w, "interrupted", http.StatusInternalServerError)
			return
		}
		// And wait for test to unblock
		select {
		case <-blockCh:
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

	statuses, link, err := s.statuses.List(req, "/api/v1/timelines/home")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if link != "" {
		w.Header().Set("Link", link)
	}

	s.returnJSON(w, req, statuses)
}

// https://docs.joinmastodon.org/methods/notifications/#get
func (s *Server) serveAPINotifications(w http.ResponseWriter, req *http.Request) {
	s.m.Lock()
	defer s.m.Unlock()

	notifs, link, err := s.notifications.List(req, "/api/v1/notifications")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if link != "" {
		w.Header().Set("Link", link)
	}

	s.returnJSON(w, req, notifs)
}

// https://docs.joinmastodon.org/methods/markers/#get
func (s *Server) serverAPIMarkers(w http.ResponseWriter, req *http.Request) {
	s.m.Lock()
	defer s.m.Unlock()

	markers := map[string]*mastodon.Marker{}
	v, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		http.Error(w, fmt.Sprintf("unable to parse query; got: %s", req.URL.RawQuery), http.StatusBadRequest)
		return
	}
	timelines := v["timeline[]"]
	if len(timelines) == 0 {
		http.Error(w, fmt.Sprintf("no timeline specified; request: %s", req.URL.String()), http.StatusBadRequest)
		return
	}
	for _, timeline := range timelines {
		if timeline != "notifications" {
			http.Error(w, fmt.Sprintf("unsupported timeline %q", timeline), http.StatusBadRequest)
			return
		}

		markers[timeline] = &mastodon.Marker{
			LastReadID: "",
			Version:    1,
			UpdatedAt:  time.Now(),
		}
	}

	s.returnJSON(w, req, markers)
}
