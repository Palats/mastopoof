### Bugs
 - https://hachyderm.io/@lcamtuf@infosec.exchange/112127045987137202 does not show; missing support for "type": "gifv"


### UI style
 - Better title menu & logout button
 - Design auth pages
 - Sanitize html from statuses

### UI features
 - Automated loading as scrolling
 - Adjust “already read” position on load
 - Link to user profile
 - Have proper auth UI for copy/pasting code vs automatic redirect.
 - Info when fetching
 - Show last fetch time
 - Automated loading on scroll down.
 - Allow configuration of automated loading
 - Notifications
 - Missing preview of URLs (when no other media in status)
 - Show polls
 - Improve URL look (removing https://, etc.)
 - Threading/replies
 - See full size picture
 - Link to post on own server to allow for +1
 - "scroll to unread"
 - Refresh timestamp (i.e., does not stay at "less then a minute ago")
 - Have a way to link to somewhere specific in the stream (for re-loading context)
 - Improve display of multiple attachments for a post (i.e., not a dumb column)


### Frontend
 - Split each element class

### Status Ordering
 - Remove redundant reblogs
 - Show thread info
 - Display replies together with initial post.
 - Prioritize specific users
 - Support ignoring some status
      - What do they become in the stream?

### CLI
- Auth & fetch from CLI
- Have mechanism to have CLI use server internally

### Backend
- Reuse mastodon clients
- Find a better mechanism to manage the various redirect URI and getting a client when no redirect is needed anyway.
- Have fetch as server RPC
- Fix management of stream "maintained" metadata - like number of remaining statuses and the like.
- Rework auth workflow, esp. naming

### Tests
- Test creation of empty DB
- Test initial login flow of first user

### Others
 - Fix model of stream/user/account/pool
    - E.g., it seems that the pool is not related to the stream in "fetch"?
 - Support fetch in the past, to get older stuff.
 - PWA
 - What's the impact of editing statuses, given that Mastopoof caches them?
 - GC of old & viewed statuses (requires some notion of refetching)
 - Detect advance of unread in other device
 - Show button to scroll down to current unread
 - No-connect mode: do not allow any mastodon server query

### Done
 - header/footer are confusing compared to the posts
 - Separate usual buttons on post from the rest
 - Replace local name by fully qualified (‘arstechnica’ -> ‘arctechnica@mastodon.social’)
 - Access to original post on server
 - Display emojis [e.g., in account info, there is a "emojis" field]
 - List users
 - Keep track of the actual username somewhere
 - RPCs security
 - Actually load the right stream instead of always stream 1.
 - Add storage for session tokens
 - Allow for redirect-style auth
 - Fix spacing with a common value (.1rem -> xx px, css class)
 - Fix serverstate updating
 - Better “state of what’s remaining”
 - Serve static content.
 - Stop hardcoding localhost:5173
 - Show number of replies and the like on each status.
 - Show date
 - Fix fresh DB init
 - Dumb invite code system
 - Link to mentions to own server (instead of user original server)
 - Show reblog timestamp
 - Fix width on narrow screen on status with long URLs
