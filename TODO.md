

### UI style
 - Better title menu & logout button
 - Better “state of what’s remaining”
 - Fix spacing with a common value (.1rem -> xx px, css class)
 - Design auth pages
 - Display emojis [e.g., in account info, there is a "emojis" field]
 - Sanitize html from statuses

### UI features
 - Automated loading as scrolling
 - Maybe a drop down menu for extra per-post actions
 - Adjust “already read” position on load
 - Support ignoring some status
    - What do they become in the stream?
 - Link to user profile
 - Fetching from Mastodon from the UI
 - Display the time of posts

### Frontend
 - Split each element class

### Backend
 - Remove redundant reblogs

### Others
 - Auth & fetch from CLI
 - Tests
 - Add storage for session tokens

### Done
 - header/footer are confusing compared to the posts
 - Separate usual buttons on post from the rest
 - Replace local name by fully qualified (‘arstechnica’ -> ‘arctechnica@mastodon.social’)
 - Access to original post on server
