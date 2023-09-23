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

