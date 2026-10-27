package driver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func withLustreMountExec(t *testing.T, fn func(ctx context.Context, source, target string, options []string) error) {
	oldExec, oldSlots, oldDelay := lustreMountExec, lustreMountSlots, lustreMountRetryDelay
	lustreMountExec = fn
	lustreMountRetryDelay = time.Millisecond
	t.Cleanup(func() { lustreMountExec, lustreMountSlots, lustreMountRetryDelay = oldExec, oldSlots, oldDelay })
}

func TestLustreMountLimitsConcurrency(t *testing.T) {
	var running, peak int32
	withLustreMountExec(t, func(context.Context, string, string, []string) error {
		n := atomic.AddInt32(&running, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		return nil
	})
	SetLustreMountConcurrency(2)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := lustreMount(context.Background(), "src", "dst", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak != 2 {
		t.Fatalf("peak concurrent mounts = %d, want 2", peak)
	}
}

func TestLustreMountRetriesEIO(t *testing.T) {
	calls := 0
	withLustreMountExec(t, func(context.Context, string, string, []string) error {
		calls++
		if calls < 3 {
			return errors.New("nsenter lustre mount failed: mount.lustre: mount x at y failed: Input/output error")
		}
		return nil
	})
	if err := lustreMount(context.Background(), "src", "dst", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestLustreMountGivesUpAfterRetries(t *testing.T) {
	calls := 0
	withLustreMountExec(t, func(context.Context, string, string, []string) error {
		calls++
		return errors.New("mount.lustre: Input/output error")
	})
	if err := lustreMount(context.Background(), "src", "dst", nil); err == nil {
		t.Fatal("want error")
	}
	if calls != lustreMountRetries+1 {
		t.Fatalf("calls = %d, want %d", calls, lustreMountRetries+1)
	}
}

func TestLustreMountDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	withLustreMountExec(t, func(context.Context, string, string, []string) error {
		calls++
		return errors.New("mount.lustre: No such file or directory")
	})
	if err := lustreMount(context.Background(), "src", "dst", nil); err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want error after 1 call", err, calls)
	}
}

func TestLustreMountWaitsForSlotUntilContextDone(t *testing.T) {
	withLustreMountExec(t, func(context.Context, string, string, []string) error { return nil })
	SetLustreMountConcurrency(1)
	lustreMountSlots <- struct{}{} // 다른 마운트가 슬롯을 잡고 있다
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := lustreMount(ctx, "src", "dst", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}
