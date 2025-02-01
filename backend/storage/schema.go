// Package storage manages Mastopoof persistence.
// This file is about the various structure representing the stored state.
package storage

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	mpdata "github.com/Palats/mastopoof/proto/data"
	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"
	settingspb "github.com/Palats/mastopoof/proto/gen/mastopoof/settings"
	"github.com/mattn/go-mastodon"
)

// SID is the type of status IDs in `statuses` and `streamcontent` databases.
type SID int64

type UID int64

func SettingListCount(s *settingspb.Settings) int64 {
	if s.GetListCount().GetOverride() {
		return s.GetListCount().GetValue()
	}
	return mpdata.SettingsInfo().GetListCount().GetDefault()
}

func SettingSeenReblogs(s *settingspb.Settings) settingspb.SettingSeenReblogs_Values {
	if s.GetSeenReblogs().GetOverride() {
		return s.GetSeenReblogs().Value
	}
	return mpdata.SettingsInfo().GetSeenReblogs().Default
}

type ASID int64

// AccountState represents information about a mastodon account in the DB.
type AccountState struct {
	// AccountState ASID within storage. Just an arbitrary number for primary key.
	ASID ASID `json:"asid"`

	// The Mastodon server this account is part of.
	// E.g., `https://mastodon.social`
	ServerAddr string `json:"server_addr"`
	// The Mastodon account ID on the server.
	// E.g., `123456789765432132`
	AccountID mastodon.ID `json:"account_id"`
	// The Mastodon username
	// E.g., `foobar`
	Username string `json:"username"`

	AccessToken string `json:"access_token"`

	// The user using this mastodon account.
	UID UID `json:"uid"`
	// Last home status ID fetched.
	LastHomeStatusID mastodon.ID `json:"last_home_status_id"`
}

// Scan implements the [Scanner] interface.
func (a *AccountState) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for AccountState json, got %T", src)
	}
	return json.Unmarshal([]byte(s), a)
}

// Value implements the [driver.Valuer] interface.
func (a *AccountState) Value() (driver.Value, error) {
	data, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

func (accountState *AccountState) ToAccountProto() *pb.Account {
	return &pb.Account{
		ServerAddr: accountState.ServerAddr,
		AccountId:  string(accountState.AccountID),
		Username:   accountState.Username,
	}
}

// StatusMeta represent metadata about a status - for now only filter state
type StatusMeta struct {
	Filters []FilterStateMatch `json:"filters"`
}

// FilterStateMatch represents whether a filter matches a given status at the time it is fetched
type FilterStateMatch struct {
	// ID of the filter
	ID string `json:"id"`
	// Whether the filter matched the status
	Matched bool `json:"matched"`
}

// Scan implements the [Scanner] interface.
func (ss *StatusMeta) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for AppRegState json, got %T", src)
	}
	return json.Unmarshal([]byte(s), ss)
}

// Value implements the [driver.Valuer] interface.
func (ss *StatusMeta) Value() (driver.Value, error) {
	data, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

func (ss *StatusMeta) ToStatusMetaProto() *pb.StatusMeta {
	var filters []*pb.FilterStateMatch
	for _, filter := range ss.Filters {
		filters = append(filters, &pb.FilterStateMatch{
			Desc:    filter.ID,
			Matched: filter.Matched,
		})
	}
	return &pb.StatusMeta{
		Filterstate: filters,
	}
}

// AppRegInfo is what identify a given app registration on a Mastodon server.
// It is used to identify which app registration is needed when interacting with a Mastodon server.
// It is not serialized - see AppRegState for that. AppRegState is a strict superset.
type AppRegInfo struct {
	// Mastodon server address.
	ServerAddr string `json:"server_addr"`
	// Scopes used when registering the app.
	Scopes string `json:"scopes"`
	// Where the oauth should redirect - incl. /_redirect.
	RedirectURI string `json:"redirect_uri"`
}

// Key computes a string key for that entry, for indexing.
// It is unique for a given AppRegKey content.
func (nfo *AppRegInfo) Key() string {
	return nfo.ServerAddr + "--" + nfo.RedirectURI + "--" + nfo.Scopes
}

type StID int64

// StreamState is the state of a single stream, stored as JSON.
type StreamState struct {
	// Stream ID.
	StID StID `json:"stid"`
	// User ID this stream belongs to.
	UID UID `json:"uid"`
	// Position of the latest read status in this stream.
	LastRead int64 `json:"last_read"`
	// Position of the first status, if any. Usually == 1.
	// 0 if there is no status yet in the stream.
	// TODO: using 0 is a bit risky as it would be very easy to accidentely end up with
	// first status to be at zero. Should either have cleaner semantic of LastPosition
	// (e.g., have it not include the last status - thus having a diff when there are status)
	// or have an explicit signal.
	FirstPosition int64 `json:"first_position"`
	// Position of the last status, if any.
	LastPosition int64 `json:"last_position"`
	// Remaining statuses in the pool which are not yet added in the stream.
	Remaining int64 `json:"remaining"`

	// Last time a fetch from mastodon finished, as unix timestamp in seconds.
	LastFetchSecs int64 `json:"last_fetch_secs"`

	NotificationsState pb.StreamInfo_NotificationsState `json:"notifications_state"`
	// Number of unread notifications
	NotificationsCount int64 `json:"notifications_count"`
}

// Scan implements the [Scanner] interface.
func (ss *StreamState) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for StreamState json, got %T", src)
	}
	return json.Unmarshal([]byte(s), ss)
}

// Value implements the [driver.Valuer] interface.
func (ss *StreamState) Value() (driver.Value, error) {
	data, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

func (ss *StreamState) ToStreamInfo() *pb.StreamInfo {
	return &pb.StreamInfo{
		Stid:               int64(ss.StID),
		LastRead:           ss.LastRead,
		FirstPosition:      ss.FirstPosition,
		LastPosition:       ss.LastPosition,
		RemainingPool:      ss.Remaining,
		LastFetchSecs:      ss.LastFetchSecs,
		NotificationState:  ss.NotificationsState,
		NotificationsCount: ss.NotificationsCount,
	}
}

// StreamState is the state of a single stream, stored as JSON.
type StreamStatusState struct {
	AlreadySeen StreamStatusState_AlreadySeen `json:"already_seen"`
}

func (sss *StreamStatusState) ToProto() *pb.StreamStatusState {
	return &pb.StreamStatusState{
		AlreadySeen: sss.AlreadySeen.ToProto(),
	}
}

// Scan implements the [Scanner] interface.
func (sss *StreamStatusState) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for StreamStatusState json, got %T", src)
	}
	return json.Unmarshal([]byte(s), sss)
}

// Value implements the [driver.Valuer] interface.
func (sss *StreamStatusState) Value() (driver.Value, error) {
	data, err := json.Marshal(sss)
	if err != nil {
		return nil, err
	}
	return string(data), err
}

type StreamStatusState_AlreadySeen int

func (v StreamStatusState_AlreadySeen) ToProto() pb.StreamStatusState_AlreadySeen {
	switch v {
	case StreamStatusState_AlreadySeen_Unknown:
		return pb.StreamStatusState_UNKNOWN
	case StreamStatusState_AlreadySeen_Yes:
		return pb.StreamStatusState_YES
	case StreamStatusState_AlreadySeen_No:
		return pb.StreamStatusState_NO
	default:
		return pb.StreamStatusState_UNKNOWN
	}
}

// Setting to detect "already seen" was not enabled.
const StreamStatusState_AlreadySeen_Unknown StreamStatusState_AlreadySeen = 0

// That status is a reblog of a status that was already triaged in the stream.
const StreamStatusState_AlreadySeen_Yes StreamStatusState_AlreadySeen = 1

// That status is either not a reblog, or a reblog of something never seen before.
const StreamStatusState_AlreadySeen_No StreamStatusState_AlreadySeen = 2

// sqlStatus encapsulate a mastodon status to allow for easier SQL
// serialization, as it is not possible to add it on the original type
// on the Mastodon library.
type sqlStatus struct {
	mastodon.Status
}

// Scan implements the [Scanner] interface.
func (ss *sqlStatus) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for Status json, got %T", src)
	}
	return json.Unmarshal([]byte(s), ss)
}

// Value implements the [driver.Valuer] interface.
func (ss *sqlStatus) Value() (driver.Value, error) {
	data, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}
	return string(data), err
}
