# mastopoof
A Mastodon client


## Backend
Authentication is kept in the DB.

Initial auth:

```
go run main.go --server https://mastodon.social auth
```

Reauth:

```
go run main.go --server https://mastodon.social --clear_app --clear_auth auth
```

## Frontend

Initial setup:

```
npm install
```

Run:

```
npm run dev
```

## Comms

Usage of:
 - https://buf.build/ for protobuf management
 - https://connectrpc.com/ for backend/frontend communication

To regen:

```
cd proto/ && buf generate
```

## Notes

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


