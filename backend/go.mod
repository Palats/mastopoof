module github.com/Palats/mastopoof/backend

go 1.21.5

require (
	connectrpc.com/connect v1.14.0
	github.com/Palats/mastopoof/proto v0.0.0
	github.com/alexedwards/scs/sqlite3store v0.0.0-20240203174419-a38e822451b6
	github.com/alexedwards/scs/v2 v2.7.0
	github.com/davecgh/go-spew v1.1.1
	github.com/golang/glog v1.1.2
	github.com/mattn/go-mastodon v0.0.6
	github.com/mattn/go-sqlite3 v1.14.17
	github.com/spf13/cobra v1.7.0
	github.com/spf13/pflag v1.0.5
	golang.org/x/net v0.17.0
)

require (
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/tomnomnom/linkheader v0.0.0-20180905144013-02ca5825eb80 // indirect
	golang.org/x/text v0.13.0 // indirect
	google.golang.org/protobuf v1.32.0 // indirect
)

replace github.com/Palats/mastopoof/proto v0.0.0 => ../proto
