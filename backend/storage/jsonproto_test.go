// This file contains tests for the conversion of storage from
// Go JSON structs to Proto JSON structs.
package storage

import (
	"encoding/json"
	"testing"

	settingspb "github.com/Palats/mastopoof/proto/gen/mastopoof/settings"
	stpb "github.com/Palats/mastopoof/proto/gen/mastopoof/storage"
	"github.com/google/go-cmp/cmp"
	"github.com/mattn/go-mastodon"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

type TPointerMessage[T any] interface {
	*T
	proto.Message
}

func testGoToProto[TGo any, TGoPointer interface{ *TGo }, TProto any, TProtoPointer TPointerMessage[TProto]](t *testing.T, goRef TGoPointer, protoRef TProtoPointer) {
	data, err := json.Marshal(goRef)
	if err != nil {
		t.Fatal(err)
	}

	gotFromGo := TProtoPointer(new(TProto))
	if err := protojson.Unmarshal(data, gotFromGo); err != nil {
		t.Fatalf("%v; encoded data:\n%s", err, string(data))
	}

	if diff := cmp.Diff(protoRef, gotFromGo, protocmp.Transform()); diff != "" {
		t.Errorf("%T -> %T (Go->Proto) mismatch (-want +got):\n%s", goRef, gotFromGo, diff)
	}
}

func testProtoToGo[TGo any, TGoPointer interface{ *TGo }, TProto any, TProtoPointer TPointerMessage[TProto]](t *testing.T, goRef TGoPointer, protoRef TProtoPointer) {
	data, err := protojson.Marshal(protoRef)
	if err != nil {
		t.Fatal(err)
	}

	gotFromProto := TGoPointer(new(TGo))
	if err := json.Unmarshal(data, gotFromProto); err != nil {
		t.Fatalf("%v; encoded data:\n%s", err, string(data))
	}
	if diff := cmp.Diff(goRef, gotFromProto); diff != "" {
		t.Errorf("%T -> %T (Proto->Go) mismatch (-want +got):\n%s", protoRef, gotFromProto, diff)
	}
}

func testProtoAndGo[TGo any, TGoPointer interface{ *TGo }, TProto any, TProtoPointer TPointerMessage[TProto]](t *testing.T, goRef TGoPointer, protoRef TProtoPointer) {
	testGoToProto(t, goRef, protoRef)
	testProtoToGo(t, goRef, protoRef)
}

type OldAppRegState struct {
	Key          string `json:"key"`
	ServerAddr   string `json:"server_addr"`
	Scopes       string `json:"scopes"`
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
}

func TestAppRegStateConv(t *testing.T) {
	testProtoAndGo(t, &OldAppRegState{
		Key:          "aaa",
		ServerAddr:   "bbb",
		Scopes:       "ccc",
		RedirectURI:  "ddd",
		ClientID:     "eee",
		ClientSecret: "fff",
		AuthURI:      "ggg",
	}, &stpb.AppRegState{
		Key:          "aaa",
		ServerAddr:   "bbb",
		Scopes:       "ccc",
		RedirectUri:  "ddd",
		ClientId:     "eee",
		ClientSecret: "fff",
		AuthUri:      "ggg",
	})
}

type OldUserState struct {
	UID         UID                  `json:"uid"`
	DefaultStID StID                 `json:"default_stid"`
	Settings    *settingspb.Settings `json:"settings"`
}

func TestUserStateConv(t *testing.T) {
	// Do not test proto->go. Int64 are encoded to string in protojson, which is not readable by json.Unmarshal.
	// However, protojson.Unmarshal is able to use integers in json fields for proto int64.
	testGoToProto(t, &OldUserState{
		UID:         12,
		DefaultStID: 13,
		Settings: &settingspb.Settings{
			ListCount: &settingspb.SettingInt64{Override: true, Value: 14},
		},
	}, &stpb.UserState{
		Uid:         12,
		DefaultStid: 13,
		Settings: &settingspb.Settings{
			ListCount: &settingspb.SettingInt64{Override: true, Value: 14},
		},
	})
}

type OldAccountState struct {
	ASID             ASID        `json:"asid"`
	ServerAddr       string      `json:"server_addr"`
	AccountID        mastodon.ID `json:"account_id"`
	Username         string      `json:"username"`
	AccessToken      string      `json:"access_token"`
	UID              UID         `json:"uid"`
	LastHomeStatusID mastodon.ID `json:"last_home_status_id"`
}

func TestAccountStateConv(t *testing.T) {
	// Do not test proto->go. Int64 are encoded to string in protojson, which is not readable by json.Unmarshal.
	// However, protojson.Unmarshal is able to use integers in json fields for proto int64.
	testGoToProto(t, &OldAccountState{
		ASID:             11,
		ServerAddr:       "aaa",
		AccountID:        "bbb",
		Username:         "ccc",
		AccessToken:      "ddd",
		UID:              12,
		LastHomeStatusID: "eee",
	}, &stpb.AccountState{
		Asid:             11,
		ServerAddr:       "aaa",
		AccountId:        "bbb",
		Username:         "ccc",
		AccessToken:      "ddd",
		Uid:              12,
		LastHomeStatusId: "eee",
	})
}
