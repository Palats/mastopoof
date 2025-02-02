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
	stpb "github.com/Palats/mastopoof/proto/gen/mastopoof/storage"
	"github.com/mattn/go-mastodon"
)

// SID is the type of status IDs in `statuses` and `streamcontent` databases.
type SID int64

// UID is a UserState ID.
type UID int64

// ASID is an AccountState ID.
type ASID int64

// StID is a StreamState ID.
type StID int64

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

func AccountStateToAccountProto(accountState *stpb.AccountState) *pb.Account {
	return &pb.Account{
		ServerAddr: accountState.ServerAddr,
		AccountId:  string(accountState.AccountId),
		Username:   accountState.Username,
	}
}

func StatusMetaToStatusMetaProto(ss *stpb.StatusMeta) *pb.StatusMeta {
	var filters []*pb.FilterStateMatch
	for _, filter := range ss.Filters {
		filters = append(filters, &pb.FilterStateMatch{
			Desc:    filter.Id,
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

func StreamStateToStreamInfo(ss *stpb.StreamState) *pb.StreamInfo {
	return &pb.StreamInfo{
		Stid:               ss.Stid,
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
