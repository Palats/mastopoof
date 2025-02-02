-- This is the database schema as if it was created from scratch. This is
-- used only for comparison with an actual schema, for consistency checking. In
-- practice, the database is setup using prepareDB, which set things up
-- progressively, reflecting the evolution of the DB schema - and this schema
-- is ignored for that purpose.

-- Mastopoof user information.
CREATE TABLE userstate (
  -- A unique id for that user.
  uid INTEGER PRIMARY KEY,
  -- Protobuf mastpoof.storage.UserState as JSON
  state TEXT NOT NULL
) STRICT;

-- State of a Mastodon account.
CREATE TABLE accountstate (
  -- Unique id.
  asid INTEGER PRIMARY KEY,
  -- Protobuf mastopoof.storage.AccountState as JSON
  state TEXT NOT NULL,
  -- The user which owns this account.
  -- Immutable - even if another user ends up configuring that account,
  -- a new account state would be created.
  uid TEXT NOT NULL,

  FOREIGN KEY(uid) REFERENCES userstate(uid)
) STRICT;

-- Info about app registration on Mastodon servers.
CREATE TABLE appregstate (
  -- A unique key for the appregstate.
  -- Made of hash of redirect URI & scopes requested, as each of those
  -- require a different Mastodon app registration.
  key TEXT NOT NULL,
  -- Protobuf mastopoof.storage.AppRegState as JSON
  state TEXT NOT NULL
) STRICT;

-- Information about a stream.
-- A stream is a series of statuses, attached to a mastopoof user.
-- This table contains info about the stream, not the statuses
-- themselves, nor the ordering.
CREATE TABLE "streamstate" (
  -- Unique id for this stream.
  stid INTEGER PRIMARY KEY,
  -- Protobuf mastopoof.storage.StreamState as JSON
  state TEXT NOT NULL
) STRICT;

-- Statuses which were obtained from Mastodon.
CREATE TABLE statuses (
  -- A unique ID.
  sid INTEGER PRIMARY KEY AUTOINCREMENT,
  -- The Mastodon account that got that status.
  asid INTEGER NOT NULL,
  -- The status, serialized as JSON.
  status TEXT NOT NULL,
  -- metadata/state about a status (e.g.: filters applied to it)
  -- Protobuf mastopoof.storage.StatusMeta as JSON
  status_meta TEXT NOT NULL DEFAULT "{}",

   -- Keep the status ID readily available to find the status again easily.
  status_id TEXT NOT NULL GENERATED ALWAYS AS (json_extract(status, '$.id')) STORED,
  status_reblog_id TEXT GENERATED ALWAYS AS (json_extract(status, '$.reblog.id')) STORED,

  FOREIGN KEY(asid) REFERENCES accountstate(asid)
) STRICT;

-- Index to help UpdateStatus, which looks up based on account+ID of the status
-- to update.
CREATE INDEX statuses_asid_status_id ON statuses(asid, status_id);

-- Index to help find statuses which have been seen before.
CREATE INDEX statuses_status_id ON statuses(status_id);
CREATE INDEX statuses_status_reblog_id ON statuses(status_reblog_id);

-- The actual content of a stream. In practice, this links position in the stream to a specific status.
-- This is kept separate from table statuses. There are 2 reasons:
--   - statuses table is more of a cache of status info from Mastodon than a Mastopoof user state.
--   - Eventually, it should be possible to have multiple stream even with a single Mastodon account.
-- While "statuses as cache" is not possible right now (e.g., status ID management), the hope
-- of not having a 1:1 mapping between account and stream is important - thus not merging
-- both tables (statuses & streamcontent).
CREATE TABLE "streamcontent" (
  stid INTEGER NOT NULL,
  sid INTEGER NOT NULL,

  -- When a new status is fetched from Mastodon, it is inserted in both `statuses`
  -- and in `streamcontent`. As long as the status is not triaged, position is NULL.
  position INTEGER,

  -- A copy of some of the status information to facilitate
  -- sorting and operation on the stream.
  status_id TEXT NOT NULL,
  status_reblog_id TEXT,
  status_in_reply_to_id TEXT,

  -- Information about that status with the stream - e.g., tags.
  -- Protobuf mastopoof.storage.StreamStatusState as JSON
  stream_status_state TEXT NOT NULL DEFAULT "{}",

  PRIMARY KEY (stid, sid),
  FOREIGN KEY(stid) REFERENCES streamstate(stid),
  FOREIGN KEY(sid) REFERENCES statuses(sid)
) STRICT;

-- No index on `stid`, as there are many entry for a single stid value.
CREATE INDEX streamcontent_sid ON streamcontent(sid);
CREATE INDEX streamcontent_position ON streamcontent(position);
CREATE INDEX streamcontent_status_id ON streamcontent(status_id);
CREATE INDEX streamcontent_status_reblog_id ON streamcontent(status_reblog_id);