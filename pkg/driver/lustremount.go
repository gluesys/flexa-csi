/*
Copyright 2026 Gluesys FlexA Inc.
*/

package driver

import (
	"context"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// 한 노드에서 Lustre 마운트를 동시에 여러 개 하면, 클라이언트가 노드에 하나뿐인 MGC 로 설정
// 로그를 함께 읽다가 요청을 보내기 전에 실패(LNetPut -ENOENT)시키고 MGC 연결을 끊는다. 그때
// 진행 중이던 마운트가 모두 EIO 로 실패한다(2026-10-27 부하 시험: 동시 4개 이상에서 약 5~10%).
// 연결이 끊긴 사이에 시작한 마운트는 ESHUTDOWN 으로 실패한다. 그래서 노드의 동시 마운트 수를
// 제한하고, 두 오류는 잠시 뒤 다시 시도한다.

// DefaultLustreMountConcurrency 는 노드에서 동시에 진행하는 Lustre 마운트 수 기본값이다.
const DefaultLustreMountConcurrency = 2

var (
	lustreMountSlots      = make(chan struct{}, DefaultLustreMountConcurrency)
	lustreMountRetries    = 3
	lustreMountRetryDelay = 2 * time.Second
	// lustreMountExec is replaced in tests.
	lustreMountExec = lustreMountViaHost
)

// SetLustreMountConcurrency 는 시작할 때 한 번 부른다(1 미만이면 1).
func SetLustreMountConcurrency(n int) {
	if n < 1 {
		n = 1
	}
	lustreMountSlots = make(chan struct{}, n)
}

// lustreMount 는 동시 마운트 수를 지키며 마운트하고, EIO·ESHUTDOWN 이면 슬롯을 내려놓고 기다렸다가
// 다시 시도한다.
func lustreMount(ctx context.Context, source, target string, options []string) error {
	slots := lustreMountSlots
	for attempt := 0; ; attempt++ {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		err := lustreMountExec(ctx, source, target, options)
		<-slots
		if err == nil || attempt >= lustreMountRetries || !isLustreTransient(err) {
			return err
		}
		log.Warnf("lustreMount: %s: %v; retry %d/%d in %s", target, err, attempt+1, lustreMountRetries, lustreMountRetryDelay)
		select {
		case <-time.After(lustreMountRetryDelay):
		case <-ctx.Done():
			return err
		}
	}
}

// isLustreTransient 는 MGC 연결이 잠시 끊겨 난 마운트 실패인지 본다.
func isLustreTransient(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Input/output error") ||
		strings.Contains(msg, "Cannot send after transport endpoint shutdown")
}
