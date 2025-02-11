module github.com/Palats/mastopoof/backend

go 1.23

require (
	connectrpc.com/connect v1.16.2
	github.com/Palats/mastopoof/frontend v0.0.0
	github.com/Palats/mastopoof/proto v0.0.0
	github.com/alexedwards/scs/sqlite3store v0.0.0-20240316134038-7e11d57e8885
	github.com/alexedwards/scs/v2 v2.8.0
	github.com/c-bata/go-prompt v0.2.6
	github.com/davecgh/go-spew v1.1.1
	github.com/golang/glog v1.2.1
	github.com/google/go-cmp v0.6.0
	github.com/mattn/go-mastodon v0.0.8
	github.com/mattn/go-sqlite3 v1.14.22
	github.com/prometheus/client_golang v1.20.5
	github.com/spf13/cobra v1.8.0
	github.com/spf13/pflag v1.0.5
	golang.org/x/net v0.30.0
	google.golang.org/protobuf v1.34.2
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.15 // indirect
	github.com/mattn/go-tty v0.0.5 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pkg/term v1.2.0-beta.2 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/tomnomnom/linkheader v0.0.0-20180905144013-02ca5825eb80 // indirect
	golang.org/x/sys v0.26.0 // indirect
	golang.org/x/text v0.19.0 // indirect
)

replace github.com/Palats/mastopoof/proto v0.0.0 => ../proto

replace github.com/Palats/mastopoof/frontend v0.0.0 => ../frontend

replace github.com/mattn/go-mastodon => github.com/Palats/go-mastodon v0.0.0-20250205204958-1d0588503fd1
