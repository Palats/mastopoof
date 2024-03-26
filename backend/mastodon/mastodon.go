package mastodon

import (
	"context"
	"fmt"
	"strings"

	gomastodon "github.com/mattn/go-mastodon"
)

type Status = gomastodon.Status
type ID = gomastodon.ID
type Client = gomastodon.Client
type Config = gomastodon.Config
type AppConfig = gomastodon.AppConfig
type Pagination = gomastodon.Pagination
type Application = gomastodon.Application

func NewClient(config *Config) *Client {
	return gomastodon.NewClient(config)
}

func RegisterApp(ctx context.Context, appConfig *AppConfig) (*Application, error) {
	return gomastodon.RegisterApp(ctx, appConfig)
}

// ValidateAddress verifies that a Mastodon server adress is vaguely looking good.
func ValidateAddress(addr string) error {
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		return fmt.Errorf("Mastodon server address should start with https:// or http:// ; got: %s", addr)
	}
	return nil
}