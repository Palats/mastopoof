-- This is the database schema as if it was created from scratch. This is
-- used only for comparison with an actual schema, for consistency checking. In
-- practice, the database is setup using prepareDB, which set things up
-- progressively, reflecting the evolution of the DB schema - and this schema
-- is ignored for that purpose.

-- Mastopoof user information.
CREATE TABLE userstate (
  -- A unique id for that user.
  uid INTEGER PRIMARY KEY,
  -- Serialized JSON UserState
  state TEXT NOT NULL
) STRICT;

-- State of a Mastodon account.
CREATE TABLE accountstate (
  -- Unique id.
  asid INTEGER PRIMARY KEY,
  -- Serialized JSON AccountState
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
  -- Serialized AppRegState
  state TEXT NOT NULL
) STRICT;

-- Information about a stream.
-- A stream is a series of statuses, attached to a mastopoof user.
-- This table contains info about the stream, not the statuses
-- themselves, nor the ordering.
CREATE TABLE "streamstate" (
  -- Unique id for this stream.
  stid INTEGER PRIMARY KEY,
  -- Serialized StreamState JSON.
  state TEXT NOT NULL
) STRICT;

-- Statuses which were obtained from Mastodon.
CREATE TABLE statuses (
  -- A unique ID.
  sid INTEGER PRIMARY KEY AUTOINCREMENT,
  -- The Mastopoof account that got that status.
  asid INTEGER NOT NULL,
  -- The status, serialized as JSON.
  status TEXT NOT NULL,
  -- metadata/state about a status (e.g.: filters applied to it)
  statusstate TEXT NOT NULL DEFAULT "{}",

  FOREIGN KEY(asid) REFERENCES accountstate(asid)
) STRICT;

-- The actual content of a stream. In practice, this links position in the stream to a specific status.
CREATE TABLE "streamcontent" (
  stid INTEGER NOT NULL,
  sid INTEGER NOT NULL,
  position INTEGER,

  PRIMARY KEY (stid, sid),
  FOREIGN KEY(stid) REFERENCES streamstate(stid),
  FOREIGN KEY(sid) REFERENCES statuses(sid)
) STRICT;
