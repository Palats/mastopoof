// Package data embeds some constant data as protobuf.
package data

import (
	_ "embed"

	pb "github.com/Palats/mastopoof/proto/gen/mastopoof"

	"google.golang.org/protobuf/encoding/prototext"
)

//go:embed settings.textproto
var textSettingsInfo []byte

var settingsInfo pb.SettingsInfo

func init() {
	if err := prototext.Unmarshal(textSettingsInfo, &settingsInfo); err != nil {
		panic(err)
	}
}

func SettingsInfo() *pb.SettingsInfo {
	return &settingsInfo
}
