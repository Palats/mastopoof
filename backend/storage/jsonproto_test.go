// This file contains tests for the conversion of storage from
// Go JSON structs to Proto JSON structs.
package storage

import (
	"encoding/json"
	"testing"

	stpb "github.com/Palats/mastopoof/proto/gen/mastopoof/storage"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/testing/protocmp"
)

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
	goWant := &OldAppRegState{
		Key:          "aaa",
		ServerAddr:   "bbb",
		Scopes:       "ccc",
		RedirectURI:  "ddd",
		ClientID:     "eee",
		ClientSecret: "fff",
		AuthURI:      "ggg",
	}

	protoWant := &stpb.AppRegState{
		Key:          "aaa",
		ServerAddr:   "bbb",
		Scopes:       "ccc",
		RedirectUri:  "ddd",
		ClientId:     "eee",
		ClientSecret: "fff",
		AuthUri:      "ggg",
	}

	// Check Go -> Proto
	data, err := json.Marshal(goWant)
	if err != nil {
		t.Fatal(err)
	}

	gotFromGo := &stpb.AppRegState{}
	if err := protojson.Unmarshal(data, gotFromGo); err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(protoWant, gotFromGo, protocmp.Transform()); diff != "" {
		t.Errorf("AppRegState mismatch (-want +got):\n%s", diff)
	}

	// Check Proto -> Go
	data, err = protojson.Marshal(protoWant)
	if err != nil {
		t.Fatal(err)
	}

	gotFromProto := &OldAppRegState{}
	if err := json.Unmarshal(data, gotFromProto); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(goWant, gotFromProto); diff != "" {
		t.Errorf("AppRegState mismatch (-want +got):\n%s", diff)
	}
}
