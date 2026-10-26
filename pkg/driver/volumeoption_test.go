/*
Copyright 2026 Gluesys FlexA Inc.
*/

package driver

import "testing"

func TestVolumeOption(t *testing.T) {
	const key = "flexa.io/optionISS"
	cases := []struct {
		name        string
		annotations map[string]string
		scParams    map[string]string
		want        string
	}{
		{"pvc wins", map[string]string{key: "off"}, map[string]string{key: "on"}, "off"},
		{"sc default", nil, map[string]string{key: "on"}, "on"},
		{"blank pvc falls back", map[string]string{key: "  "}, map[string]string{key: "on"}, "on"},
		{"neither: proxy default", map[string]string{}, map[string]string{}, "on"},
		{"trimmed", map[string]string{key: " off "}, nil, "off"},
	}
	for _, c := range cases {
		if got := volumeOption(c.annotations, c.scParams, key); got != c.want {
			t.Errorf("%s: volumeOption = %q, want %q", c.name, got, c.want)
		}
	}
	for k, want := range map[string]string{"flexa.io/optionSVS": "off", "flexa.io/optionComp": "off",
		"flexa.io/optionDedup": "off", "flexa.io/nfsAccess": "RW", "flexa.io/nfsNoRootSquashing": "off",
		"flexa.io/nfsInsecure": "off", "flexa.io/secureAddress": ""} {
		if got := volumeOption(nil, nil, k); got != want {
			t.Errorf("default %s = %q, want %q", k, got, want)
		}
	}
}
