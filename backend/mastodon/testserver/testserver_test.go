// Package testserver tests the Mastodon testserver implementation.
package testserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/mattn/go-mastodon"
	"golang.org/x/net/publicsuffix"
)

type TestEnv struct {
	// Provided after Init.
	client         *http.Client
	addr           string
	httpServer     *httptest.Server
	mastodonServer *Server
}

func (env *TestEnv) Init(t testing.TB) *TestEnv {
	t.Helper()
	mux := http.NewServeMux()

	// Create Mastodon server.
	env.mastodonServer = New()
	env.mastodonServer.RegisterOn(mux)

	// Create the http server
	env.httpServer = httptest.NewTLSServer(&LoggingHandler{T: t, Handler: mux})
	env.addr = env.httpServer.URL

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		t.Fatal(err)
	}

	env.client = env.httpServer.Client()
	env.client.Jar = jar

	return env
}

func (env *TestEnv) Close() {
	if env.httpServer != nil {
		env.httpServer.Close()
	}
}

func (env *TestEnv) EmptyPost(t testing.TB, path string) (*http.Response, []byte) {
	httpResp, err := env.client.Post(env.httpServer.URL+path, "application/json", (&bytes.Buffer{}))
	if err != nil {
		t.Fatalf("failed to issue Post request: %v", err)
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		t.Fatalf("unable to read http resp body: %v", err)
	}
	httpResp.Body.Close()
	return httpResp, body
}

func TestFavourite(t *testing.T) {
	env := (&TestEnv{}).Init(t)

	// Add a status.
	refStatus, err := env.mastodonServer.AddFakeStatus()
	if err != nil {
		t.Fatal(err)
	}

	// Try an unknown status.
	httpResp, body := env.EmptyPost(t, "/api/v1/statuses/42/favourite")
	if got, want := httpResp.StatusCode, http.StatusNotFound; got != want {
		t.Fatalf("got status %v [%s], want %v; body=%s", got, httpResp.Status, want, string(body))
	}

	// Try the status we just added.
	httpResp, body = env.EmptyPost(t, fmt.Sprintf("/api/v1/statuses/%s/favourite", refStatus.ID))
	if got, want := httpResp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("got status %v [%s], want %v; body=%s", got, httpResp.Status, want, string(body))
	}
	var gotStatus mastodon.Status
	if err := json.Unmarshal(body, &gotStatus); err != nil {
		t.Fatalf("unable to decode json: %v", err)
	}
	if got, want := gotStatus.Favourited, true; got != want {
		t.Errorf("got favourited %v, wanted %v", got, want)
	}

	// Check through another mean that the status is indeed favourited.
	httpResp, body = env.EmptyPost(t, fmt.Sprintf("/api/v1/statuses/%s", refStatus.ID))
	if got, want := httpResp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("got status %v [%s], want %v; body=%s", got, httpResp.Status, want, string(body))
	}
	if err := json.Unmarshal(body, &gotStatus); err != nil {
		t.Fatalf("unable to decode json: %v", err)
	}
	if got, want := gotStatus.Favourited, true; got != want {
		t.Errorf("got favourited %v, wanted %v", got, want)
	}

	// Try unfavorite, on unknown status
	httpResp, body = env.EmptyPost(t, "/api/v1/statuses/42/unfavourite")
	if got, want := httpResp.StatusCode, http.StatusNotFound; got != want {
		t.Fatalf("got status %v [%s], want %v; body=%s", got, httpResp.Status, want, string(body))
	}

	// Try unfavorite on the status we just favorited.
	httpResp, body = env.EmptyPost(t, fmt.Sprintf("/api/v1/statuses/%s/unfavourite", refStatus.ID))
	if got, want := httpResp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("got status %v [%s], want %v; body=%s", got, httpResp.Status, want, string(body))
	}
	if err := json.Unmarshal(body, &gotStatus); err != nil {
		t.Fatalf("unable to decode json: %v", err)
	}
	if got, want := gotStatus.Favourited, false; got != want {
		t.Errorf("got favourited %v, wanted %v", got, want)
	}
}
