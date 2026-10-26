/*
Copyright 2026 Gluesys FlexA Inc.
*/

package driver

import (
	"os"
	"reflect"
	"testing"
)

func fakeNfsLockOps(rpcbind, statd bool, calls *[][]string) nfsLockOps {
	return nfsLockOps{
		rpcbindUp: func() bool { return rpcbind },
		statdUp:   func() bool { return statd },
		run: func(name string, args ...string) error {
			*calls = append(*calls, append([]string{name}, args...))
			return nil
		},
		mkdirAll: func(string, os.FileMode) error { return nil },
	}
}

func TestEnsureNfsLockDaemonsStartsMissing(t *testing.T) {
	var calls [][]string
	got := ensureNfsLockDaemons(fakeNfsLockOps(false, false, &calls))
	if !reflect.DeepEqual(got, []string{"rpcbind", "rpc.statd"}) {
		t.Fatalf("started = %v", got)
	}
	want := [][]string{{"rpcbind", "-w"}, {"rpc.statd"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestEnsureNfsLockDaemonsReusesRunning(t *testing.T) {
	var calls [][]string
	if got := ensureNfsLockDaemons(fakeNfsLockOps(true, true, &calls)); len(got) != 0 || len(calls) != 0 {
		t.Fatalf("started=%v calls=%v, want none", got, calls)
	}
	calls = nil
	got := ensureNfsLockDaemons(fakeNfsLockOps(true, false, &calls))
	if !reflect.DeepEqual(got, []string{"rpc.statd"}) {
		t.Fatalf("started = %v, want only rpc.statd", got)
	}
}
