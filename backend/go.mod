module github.com/Palats/mastopoof/backend

go 1.21.5

require (
	github.com/davecgh/go-spew v1.1.1
	github.com/golang/glog v1.1.2
	github.com/mattn/go-mastodon v0.0.6
	github.com/mattn/go-sqlite3 v1.14.17
	github.com/spf13/cobra v1.7.0
	github.com/spf13/pflag v1.0.5
)

require (
	github.com/Palats/mastopoof/proto v0.0.0-00010101000000-000000000000 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/tomnomnom/linkheader v0.0.0-20180905144013-02ca5825eb80 // indirect
)

replace github.com/Palats/mastopoof/proto => ../proto
