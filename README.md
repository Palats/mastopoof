# Mastopoof

## About

A Mastodon web client, which:
 - Remembers what you've already seen.
 - Displays in reading order by default: on top are older statuses, and scrolling down goes to newer statuses.
 - In the future, allows stream ordering: hide already seen statuses, prioritize what one want to see vs content for idle scrolling, etc.

Supports Chrome & Firefox, on desktop & mobile.

Status:
 - Frontend & backend are functional and I've been using it as my main client for a while.
 - No hosted version, nor pre-built binaries.
 - It works for reading, but does not aim yet to be a full replacement - for posting and interactions, the Mastodon server needs to be used.

## Install

Mastopoof requires a running a backend, which takes care of communicating with Mastodon, keeping state and serving the frontend. It supports multiple users.

To install it, Go lang & npm are required. It relies on SQLite for state keeping.

Building:
```
git clone https://github.com/Palats/mastopoof.git
cd mastopoof
npm install
(cd proto; npm run gen)
(cd frontend; npm run build)
(cd backend; go build)
```

Running:
```
./backend/backend --self_url http://localhost:8080 --db mastopoof.db --port 8080 --invite_code somecode serve
```

where:
 - `--self_url` indicates the address under which a browser will access that server. Without it, OAuth authentication will have to rely on copy/pasting code.
 - `--db` specify where to store the SQLite database.
 - `--port` is the port on which to serve (both backend RPCs & serving frontend javascript/html).
 - `--invite_code` restricts who can use this instance - registration requires knowning the code. Optional.


## Development

### Setup

This assumes the repository has been cloned locally:

```
npm install
(cd proto; npm run gen)
(cd frontend; npm run build)
```

### Running

#### Split backend/frontend
During dev, it is convenient to run backend and frontend separately.

The backend:
```
cd backend && go run main.go --alsologtostderr --db [DBFILE] --self_url http://localhost:5173 serve
```

The frontend:
```
cd frontend && npm run dev
```
This frontend will automatically recompile and reload as needed.

#### Backend serving frontend
It is also possible to serve the frontend using the backend. In this case:

```
(cd frontend/ && npm run build)
cd backend && go run main.go --alsologtostderr serve --self_url http://localhost:8079
```

#### Fake Mastodon server
There is a backend with a fake Mastodon server; to run it:
```
cd backend && go run main.go --alsologtostderr testserve
```
The test server provides a small prompt to manipulate the content - e.g., adding fake statuses. This testserver will use any `.json` file in `backend/localtestdata` (flag: `--testdata`) to seed the stream content. The `.json` files must contain raw JSON dump of Mastodon statuses.

To have access from another host, in different terminals:

```
cd backend && go run main.go --alsologtostderr --insecure testserve
cd frontend && npm run dev -- --host 0.0.0.0
```


#### Tests
To run tests:
```
cd backend && go test ./...
```

Frontend:

```
cd frontend && npm run test|test-watch
```

Frontend tests relies on mocha (default of web-test-runner) and chai



### Updating go-mastodon

Mastopoof uses a fork of https://github.com/mattn/go-mastodon available at https://github.com/Palats/go-mastodon . The goal of this fork is to add missing APIs, while waiting to have them upstreamed.

Per Go conventions, the exact commit being used is hard-coded in go.mod; it can be updated by running:

```
cd backend && go mod edit --replace github.com/mattn/go-mastodon=github.com/Palats/go-mastodon@master && GONOPROXY='*' go mod tidy
```

The `GONOPROXY` is to allow it to see recent pushes to the other repository - see https://sum.golang.org/ FAQ.

To try with local modification of go-mastodon, clone the repository, and create a `go.work` similar to:

```
go 1.22.2

use ./backend

use ../go-mastodon/
```

### Release

```
(cd proto && npm run gen ) && (cd frontend && npm run build) && (cd backend && go build) && (cd backend && go test ./...) && cp -f backend/backend prod/backend
```

## Architecture & Notes

### Terminology

- User: usually a Mastopoof user, as opposed to a Mastodon user.
- Account: usually refers to a Mastopoof account.
- Stream: ordered list of items presented to the user. A stream is owned by a given Mastopoof user.
- Stream item: a reference to a given Mastodon status, with a position in the stream.
   - Items are never re-ordered - once an item has been added, it is in that position.
- Pool: Statuses from a Mastodon timeline in the Mastopoof database.
- Fetch: querying Mastodon for statuses and inserting them in a pool.
- Triaging: selecting statuses from the pool and adding them as items in the stream (or indicating they are to be skipped).
   - Inconsistent terminology: picking, inserting, etc.
- Untriaged statuses: entries of the pool which are not yet in the stream.
- Triaged statuses: entries of the pool which have been added to the stream or marked-as-hidden.-
- List: getting a list of items from a stream. That might trigger triaging.
- Last-read: a marker in the stream, pointing to the last item which has been read by the user. Lower or equal positions are read.

### High level

Goal is to present a stream of statuses to the user. The stream, once revealed,
is fixed - i.e., for a given user, there is a list of status that have been
selected and presented in this specific order. It is represented as an ordered
list, where the first status to ever have been shown to the user is on top, then
the next one is just below and so on.

Statuses are added to the stream in 2 steps. First, existing statuses from the
people the user follow are fetched and added to a pool. Those are the statuses
that the Mastodon UI would show in chronological order. Then, when the user
wants to see more content (aka, scrolls down), statuses are picked one after the
other from the pool based on user defined rules. Rules can be "picking up first
status from those users", "pick statuses from this users but not reblogs", "pick
the rest", and so on - incl. time based rules. Once a status has been picked, it
is added to the stream and won't move from there.

There is also a notion of "already read" - i.e., the position in the stream to
open the app at. Statuses which are already in the stream but below that line
(i.e., more recent) are considered "opened".

Each status in the stream has a unique position. The first one to be added is at
1 - 0 is kept for other purposes. Value of position is increased as new statuses
are added.

### Frontend config

Frontend (typescript) has access to a global variable named `mastopoofConfig`
containing various bits of config. This is always available, as it is loaded
before main typescript is executed.

This is obtained by loading `/_config` from the `index.html`:
 - When running the frontend from backend binary, this hits a special end point
   which provides (see `server.go`) a javascript snippet with the config.
 - When running from the dev frontend (through npm), the dev proxy simply
   redirects the requests to the backend.

### Backend/Frontend RPCs

Usage of:
 - https://buf.build/ for protobuf management
 - https://connectrpc.com/ for backend/frontend communication

Example manual RPC call:

```
curl --header 'Content-Type: application/json' --data '{"msg": "plop"}' http://localhost:8079/_rpc/mastopoof.Mastopoof/Ping
```

### Database

See `backend/storage/schema.go` for database schema information.

### Auth

 - On load: issue `Mastopoof.Login` to see if frontend is logged in (aka, cookie)

#### Login flow
FE = Mastodon frontend, BE = Mastodon backend

 - FE gets request initial request:
    - Detects user not logged
    - Shows Auth box asking for Mastodon server + Invite code
    - User fills in Mastodon server + invite code, click Sign-in
 - FE -> BE `Mastopoof.Authorize` ; server addr + invite code
 - BE:
    - register app on server (if necessary) ; `POST /api/v1/apps`
    - Construct oauth URI locally (targeting `/oauth/authorize`)
 - BE -> FE: Send back auth uri
 - FE: Show second auth box
 - If user follows Auth URI:
    - User gets prompt from the Mastodon server, approve
    - Mastodon redirects to the URI provided by Mastopoof when constructing the oauth URI
    - BE: receive request on `/_redirect`
    - BE: calls `Mastopoof.Token` on itself
 - If using `urn:ietf:wg:oauth:2.0:oob` instead of redirect:
    - User gets prompt from the Mastodon server, approve
    - User gets a token
    - User copy/paste token on FE auth box
    - User validates, which calls `Mastopoof.Token` on BE
 - BE got called on `Mastopoof.Token` with auth Token from Mastodon
 - BE register App on Mastodon server if necessary (it should not be)
 - BE authenticate against Mastodon server with the auth token (`AuthenticateToken`, `POST /oauth/token`)
 - BE creates Mastopoof user, get identity info from Mastodon, etc.
 - BE->FE : Set http cookie, send back user info (default stream ID)

### Links

On infinite scrolling:
 - https://adrianfaciu.dev/posts/observables-litelement/
 - https://github.com/lit/lit/tree/main/packages/labs/virtualizer#readme
 - https://stackoverflow.com/questions/60678734/insert-elements-on-top-of-something-without-changing-visible-scrolled-content

Favicons sources:
 - https://commons.wikimedia.org/wiki/File:Duck-293474_white_background.jpg


### Mastodon protocol

- https://docs.joinmastodon.org/methods/
- https://docs.joinmastodon.org/entities/

There are 3 server / domains potentially involved:
 - user domain: The domain of the mastodon account used to access mastodon - i.e., "your account".
 - status domain: The domain of the person having created the status.
 - reblogged domain: The domain of the person being reblogged.

About Status:
 - `id`: only makes sense within the context of the user domainserver; abstract ID, no info about username or server.
 - `uri`: identifier of that status. Made to be globally unique it seems, incl. across server. On the status domain; e.g., `https://mastodon.cloud/users/slashdot/statuses/112121802622153992`, and on the reblogged domain for the `reblog` part.
 - `url`: optional - can and will be sometimes empty; link to "HTML" version, according to doc. On the status domain for the containing status, on the reblog domain for the reblogged part.

Account, from the one included in status:
 - `id`: only within context of server hosting the status? (i.e., by opposition to the server of the account?)
 - `username`: short username, without the domain.
 - `acct`: From docs: "The Webfinger account URI. Equal to username for local users, or username@domain for remote users.".

Media attachments
 - `url`: "The location of the original full-size attachment". Can be a cache link on the user domain.
 - `preview_url`: "The location of a scaled-down preview of the attachment". Can be a cache link on the user domain.
   - For `gifv` (and probably others), `preview_url` can be a link to an image
 - `remote_url`: "The location of the full-size original attachment on the remote website". Can be empty. Probably fairly divers - but can be a link on the status domain when, I think, the media (e.g image) was uploaded there. Or giphy (for example) if the file came from there.
 - `meta`: for at least video/gifv/image, `small` and `original` keys can contain `width`, `height`, `size` (e.g., "1920x1080"), `aspect` (e.g., 1.7777)


### Dev principles

 - Predictability - reduce user surprise.
    - Stream stays in the same order once visualized once.
 - Light tech stack
    - Try to keep dependencies under control.
    - No complex features papering over something non critical. E.g., try to rely on browser scrolling and not re-inventing my own just to fix some minor issue.
 - Covering all Mastodon features is not a priority - it is fine to rely falling back on the regular Mastodon UI. Nevertheless, eventually more features will be added.