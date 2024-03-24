package mastodon

import (
	"context"

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
