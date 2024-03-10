# mastopoof
A Mastodon client


## Dev setup

One time:
- Run `npm install` in `frontend/` and in `proto/`
- Run `npm run gen` to regenerate protobuf modules

To run:
- Start `go run main.go --alsologtostderr serve` in `backend/` ; `--redirect_url http://localhost:5173` to have auth redirection.
- Start `npm run dev` in `frontend/`

To use built-in frontend:
- Run `npm run build` in `frontend/`
- Start `go run main.go --alsologtostderr serve --redirect_url http://localhost:8079` in `backend/`

### Comms

Usage of:
 - https://buf.build/ for protobuf management
 - https://connectrpc.com/ for backend/frontend communication

To regen:

```
cd proto/ && buf generate
```

Example call:

```
curl --header 'Content-Type: application/json' --data '{"msg": "plop"}' http://localhost:8079/_rpc/mastopoof.Mastopoof/Ping
```

## Structure

### Database

- serverstate
   - Per mastodon server info.
   - key: server_addr
   - Keyed by mastodon server address + redirect URI
   - Not linked to a specific account (neither mastodon nor mastopoof)
- accountstate
   - Mastodon account state.
   - key: asid (AccountStateID)  [unique integer]
   - Keyed by mastodon server address + account ID on the mastodon server
   - Attached to a given user (UID)
- userstate
   - Mastopoof user
   - key: uid [unique integer]
   - Has default stream ID
- streamstate
   - A configured stream of statuses.
   - key: stid [unique integer]
   - For now: a single stream per mastopoof user.
- streamcontent
   - Position of statuses in a stream
   - In practice: (stid, sid) -> position
- statuses
   - Content of statuses fetched from Mastodon
   - key: sid [unique integer]
   - Also attached to a specific UID


### Auth

 - On load: issue [Mastopoof.Login] to see if frontend is logged in (aka, cookie)

#### Full login flow
 - FE gets mastodon server address
 - FE -> BE [Mastopoof.Authorize] ; server addr
 - BE: register app on server, gets auth URI
 - BE->FE: auth uri
 - FE: go on auth uri and get auth code
 - FE->BE [Mastopoof.Token] ; auth code
 - BE: get token from server, create user if needed.
 - BE->FE : Set cookie, send back user info (default stream ID)

### Notes

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

Also listing == stream.

#### Links

On infinite scrolling:
 - https://adrianfaciu.dev/posts/observables-litelement/
 - https://github.com/lit/lit/tree/main/packages/labs/virtualizer#readme
 - https://stackoverflow.com/questions/60678734/insert-elements-on-top-of-something-without-changing-visible-scrolled-content

Favicos sources:
 - https://commons.wikimedia.org/wiki/File:Duck-293474_white_background.jpg
