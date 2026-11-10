package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gluesys/flexa-csi/pkg/flexa/webapi"
)

func TestOutcomeUnknown(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"no answer", errors.New("EOF"), true},
		{"5xx", &webapi.APIError{Status: 503}, true},
		{"already exists", &webapi.APIError{Status: 409, Msg: "Volume(v) already exists"}, true},
		{"refused 409", &webapi.APIError{Status: 409, Msg: "Pool(p) does not exist"}, false},
		{"refused 400", &webapi.APIError{Status: 400}, false},
		{"not sent", fmt.Errorf("ca holds no PEM certificate (%w)", webapi.ErrNotSent), false},
	}
	for _, tc := range cases {
		if got := outcomeUnknown(tc.err); got != tc.want {
			t.Errorf("%s: outcomeUnknown = %t, want %t", tc.name, got, tc.want)
		}
	}
	if err := markUnfinished(&webapi.APIError{Status: 500}, true); !errors.Is(err, ErrCreateUnfinished) {
		t.Fatalf("markUnfinished lost the mark: %v", err)
	} else if got := (*webapi.APIError)(nil); !errors.As(err, &got) || got.Status != 500 {
		t.Fatalf("markUnfinished hid the proxy error: %v", err)
	}
}
