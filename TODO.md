### Missing for personal use
 - Allow-list for mastodon accounts

### UI style
 - Better title menu & logout button
 - Design auth pages
 - Sanitize html from statuses
 - Fix width on narrow screen on status with long URLs

### UI features
 - Automated loading as scrolling
 - Maybe a drop down menu for extra per-post actions
 - Adjust “already read” position on load
 - Link to user profile
 - Fetching from Mastodon from the UI
 - Display the time of posts
 - Have proper auth UI for copy/pasting code vs automatic redirect.
 - Info when fetching
 - Automated loading on scroll down.
 - Allow configuration of automated loading
 - Notifications

### Frontend
 - Split each element class

### Status Ordering
 - Remove redundant reblogs
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

### Tests
- Test creation of empty DB
- Test initial login flow of first user

### Others
 - Fix model of stream/user/account/pool
    - E.g., it seems that the pool is not related to the stream in "fetch"?
 - Support fetch in the past, to get older stuff.

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