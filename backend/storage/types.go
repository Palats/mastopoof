// Package storage manages Mastopoof persistence.
// This file is about some types helpers.
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
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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

// SQLProto encapsulate a protobuf message to make suitable as value
// of SQL queries - both as source data and as destination data.
// E.g.,:  SQLProto{myMsg}
type SQLProto struct {
	proto.Message
}

// Scan implements the [sql.Scanner] interface.
func (m SQLProto) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("expected a string for proto json, got %T", src)
	}
	return protojson.Unmarshal([]byte(s), m)
}

// Value implements the [driver.Valuer] interface.
func (m SQLProto) Value() (driver.Value, error) {
	data, err := protojson.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(data), err
}
