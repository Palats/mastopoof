package mastodon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	gomastodon "github.com/mattn/go-mastodon"
)

type Status = gomastodon.Status
type Account = gomastodon.Account
type ID = gomastodon.ID
type Client = gomastodon.Client
type Config = gomastodon.Config
type AppConfig = gomastodon.AppConfig
type Pagination = gomastodon.Pagination
type Application = gomastodon.Application
type Filter = gomastodon.Filter
type Tag = gomastodon.Tag
type Notification = gomastodon.Notification

func NewClient(config *Config) *Client {
	return gomastodon.NewClient(config)
}

func RegisterApp(ctx context.Context, appConfig *AppConfig) (*Application, error) {
	return gomastodon.RegisterApp(ctx, appConfig)
}

// GetMarkers gets current marker position.
// https://docs.joinmastodon.org/methods/markers/#get
// Returns a map from timeline name to marker.
// TODO: this is not supported by go-mastodon. Move off go-mastodon.
func GetMarkers(ctx context.Context, c *Client, timelines []string) (map[string]*Marker, error) {
	u, err := url.Parse(c.Config.Server)
	if err != nil {
		return nil, err
	}

	method := "/api/v1/markers"
	u.Path = path.Join(u.Path, "/api/v1/markers")

	values := url.Values{}
	for _, t := range timelines {
		values.Add("timeline[]", t)
	}
	u.RawQuery = values.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+c.Config.AccessToken)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	// TODO: do backoff
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("unable to read response body for %s: %w", method, err)
	}

	if resp.StatusCode != http.StatusOK {
		// TODO: parse error
		return nil, fmt.Errorf("reading %s failed: %s", method, string(body))
	}

	markers := map[string]*Marker{}
	if err := json.Unmarshal(body, &markers); err != nil {
		return nil, fmt.Errorf("unable to parse %s result: %w; body: %s", method, err, string(body))
	}

	return markers, nil
}

type Marker struct {
	LastReadID ID    `json:"last_read_id"`
	Version    int64 `json:"version"`
	// TODO: parse as timestamp
	UpdatedAt string `json:"updated_at"`
}
