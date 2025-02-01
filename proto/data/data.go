// Package data embeds some constant data as protobuf.
package data

import (
	_ "embed"

	settingspb "github.com/Palats/mastopoof/proto/gen/mastopoof/settings"

	"google.golang.org/protobuf/encoding/prototext"
)

//go:embed settings.textproto
var textSettingsInfo []byte

var settingsInfo settingspb.SettingsInfo

func init() {
	if err := prototext.Unmarshal(textSettingsInfo, &settingsInfo); err != nil {
		panic(err)
	}
}

func SettingsInfo() *settingspb.SettingsInfo {
	return &settingsInfo
}
