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
		{"neither", map[string]string{}, map[string]string{}, ""},
		{"trimmed", map[string]string{key: " off "}, nil, "off"},
	}
	for _, c := range cases {
		if got := volumeOption(c.annotations, c.scParams, key); got != c.want {
			t.Errorf("%s: volumeOption = %q, want %q", c.name, got, c.want)
		}
	}
}
