/*
Copyright 2026 Gluesys FlexA Inc.
*/

package driver

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// FlexA 는 NFS v3 만 켜 둔다. v3 마운트는 파일 잠금(NLM)을 위해 클라이언트 쪽 rpc.statd 가
// 필요하다. node 플러그인 컨테이너에는 바이너리(nfs-utils)만 있고 데몬이 없어서, 마운트가
// "rpc.statd is not running" 으로 멈췄다. node 모드에서 rpcbind·rpc.statd 를 띄우고 지켜본다.
// hostNetwork 라 노드에 이미 떠 있으면 그것을 그대로 쓴다.

const (
	rpcbindAddr       = "127.0.0.1:111"
	nfsLockCheckEvery = 30 * time.Second
	statdStateDir     = "/var/lib/nfs/statd"
)

type nfsLockOps struct {
	rpcbindUp func() bool
	statdUp   func() bool
	run       func(name string, args ...string) error
	mkdirAll  func(path string, perm os.FileMode) error
}

func defaultNfsLockOps() nfsLockOps {
	return nfsLockOps{
		rpcbindUp: func() bool {
			c, err := net.DialTimeout("tcp", rpcbindAddr, time.Second)
			if err != nil {
				return false
			}
			_ = c.Close()
			return true
		},
		statdUp: func() bool {
			// rpcinfo 로 status(100024) 프로그램 등록 여부를 본다.
			out, err := exec.Command("rpcinfo", "-p", "127.0.0.1").CombinedOutput()
			return err == nil && strings.Contains(string(out), "status")
		},
		run: func(name string, args ...string) error {
			out, err := exec.Command(name, args...).CombinedOutput()
			if err != nil {
				log.Errorf("%s %v failed: %v: %s", name, args, err, strings.TrimSpace(string(out)))
			}
			return err
		},
		mkdirAll: os.MkdirAll,
	}
}

// ensureNfsLockDaemons 는 rpcbind 와 rpc.statd 가 없으면 띄운다. 둘 다 스스로 데몬이 된다.
// 돌려주는 값: 이번에 띄운 데몬 이름.
func ensureNfsLockDaemons(ops nfsLockOps) []string {
	var started []string
	if !ops.rpcbindUp() {
		_ = ops.mkdirAll("/run/rpcbind", 0755)
		if ops.run("rpcbind", "-w") == nil {
			started = append(started, "rpcbind")
		}
	}
	if !ops.statdUp() {
		for _, d := range []string{statdStateDir + "/sm", statdStateDir + "/sm.bak"} {
			_ = ops.mkdirAll(d, 0700)
		}
		if ops.run("rpc.statd") == nil {
			started = append(started, "rpc.statd")
		}
	}
	return started
}

// StartNfsLockDaemons 는 node 모드에서 한 번 부른다. 처음 한 번 확인한 뒤 주기적으로 다시 본다.
func StartNfsLockDaemons() {
	ops := defaultNfsLockOps()
	if s := ensureNfsLockDaemons(ops); len(s) > 0 {
		log.Infof("NFS lock daemons started: %v", s)
	} else {
		log.Infof("NFS lock daemons already running (rpcbind, rpc.statd)")
	}
	go func() {
		t := time.NewTicker(nfsLockCheckEvery)
		defer t.Stop()
		for range t.C {
			if s := ensureNfsLockDaemons(ops); len(s) > 0 {
				log.Warnf("NFS lock daemons restarted: %v", s)
			}
		}
	}()
}
