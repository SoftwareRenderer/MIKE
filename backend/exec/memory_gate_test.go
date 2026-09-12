package exec

import (
	"context"
	"testing"
	"time"
)

func TestMemoryGateZeroCapacityDisablesGate(t *testing.T) {
	g := &MemoryGate{}
	if err := g.Acquire(context.Background(), 999999, nil); err != nil {
		t.Fatalf("disabled gate must succeed immediately, got %v", err)
	}
}

func TestMemoryGateAcquireFitsImmediately(t *testing.T) {
	g := &MemoryGate{}
	g.SetCapacity(1000)
	if err := g.Acquire(context.Background(), 500, nil); err != nil {
		t.Fatal(err)
	}
	if g.Used() != 500 {
		t.Errorf("expected used=500, got %d", g.Used())
	}
}

func TestMemoryGateAcquireExactCapacity(t *testing.T) {
	g := &MemoryGate{}
	g.SetCapacity(1000)
	if err := g.Acquire(context.Background(), 1000, nil); err != nil {
		t.Fatalf("acquire at exact capacity must succeed, got %v", err)
	}
}

func TestMemoryGateAcquireExceedsCapacity(t *testing.T) {
	g := &MemoryGate{}
	g.SetCapacity(1000)
	if err := g.Acquire(context.Background(), 1000, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := g.Acquire(ctx, 1, nil); err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestMemoryGateReleaseUnblocksWaiter(t *testing.T) {
	g := &MemoryGate{}
	g.SetCapacity(1000)
	if err := g.Acquire(context.Background(), 600, nil); err != nil {
		t.Fatal(err)
	}

	acquired := make(chan error, 1)
	go func() { acquired <- g.Acquire(context.Background(), 600, nil) }()
	time.Sleep(50 * time.Millisecond) // let the waiter enqueue before releasing

	g.Release(600)

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not served after release")
	}
	if g.Used() != 600 {
		t.Errorf("expected used=600 after handoff, got %d", g.Used())
	}
}

func TestMemoryGateCancelCleansUp(t *testing.T) {
	g := &MemoryGate{}
	g.SetCapacity(500)
	if err := g.Acquire(context.Background(), 500, nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Acquire(ctx, 200, nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Acquire did not return")
	}
	if g.Used() != 500 {
		t.Errorf("cancel must not change used; got %d", g.Used())
	}
}

func TestMemoryGateFIFONoJumping(t *testing.T) {
	// B must not receive a grant while A is ahead of it in the queue,
	// even when B alone would fit after a release.
	g := &MemoryGate{}
	g.SetCapacity(1000)
	if err := g.Acquire(context.Background(), 1000, nil); err != nil {
		t.Fatal(err)
	}

	aReady := make(chan struct{})
	aResult := make(chan error, 1)
	go func() {
		aResult <- g.Acquire(context.Background(), 400, func(w *memWaiter) { close(aReady) })
	}()
	<-aReady

	ctxB, cancelB := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelB()
	errB := g.Acquire(ctxB, 600, nil)
	if errB != context.DeadlineExceeded {
		t.Fatalf("B must not be served while A is ahead; got %v", errB)
	}

	g.Release(400) // frees room for A; A is served synchronously by Release

	select {
	case err := <-aResult:
		if err != nil {
			t.Fatalf("A should have been granted, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("A not served after release")
	}
}

func TestMemoryGateNegativeAmountSucceeds(t *testing.T) {
	g := &MemoryGate{}
	g.SetCapacity(100)
	if err := g.Acquire(context.Background(), 0, nil); err != nil {
		t.Errorf("zero amount must succeed, got %v", err)
	}
	if err := g.Acquire(context.Background(), -1, nil); err != nil {
		t.Errorf("negative amount must succeed, got %v", err)
	}
}

func TestMemoryGateReleaseClampsZero(t *testing.T) {
	g := &MemoryGate{}
	g.SetCapacity(100)
	g.Release(200) // release more than held
	if g.Used() != 0 {
		t.Errorf("used must clamp to 0, got %d", g.Used())
	}
}
