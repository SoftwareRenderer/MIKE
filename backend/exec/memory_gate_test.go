package exec

import (
	"context"
	"testing"
	"time"

	"mike/backend/types"
)

// TestMemoryGateAdmission holds the admission rule itself — MIKE's charge plus the reservations plus the
// candidate must stay inside the ceiling — at its edges: exact capacity fits, one megabyte more waits, a
// request that books nothing is never refused, releasing more than the ledger holds cannot drive it
// negative, and a zero capacity means there is no budget to enforce.
func TestMemoryGateAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		capacity  int64
		held      []int64
		request   int64
		wantErr   error
		wantUsed  int64
		checkUsed bool
	}{
		{name: "a zero capacity disables the gate", capacity: 0, request: 999999},
		{name: "a request that fits is booked", capacity: 1000, request: 500, wantUsed: 500, checkUsed: true},
		{name: "exactly the capacity fits", capacity: 1000, request: 1000, wantUsed: 1000, checkUsed: true},
		{name: "one megabyte over the capacity waits", capacity: 1000, held: []int64{1000}, request: 1, wantErr: context.DeadlineExceeded, wantUsed: 1000, checkUsed: true},
		{name: "a request that books nothing is admitted", capacity: 100, request: 0, wantUsed: 0, checkUsed: true},
		{name: "a negative request is admitted", capacity: 100, request: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &MemoryGate{}
			g.SetCapacity(tc.capacity)
			for _, amount := range tc.held {
				if err := g.Acquire(context.Background(), amount, nil); err != nil {
					t.Fatalf("acquire %d: %v", amount, err)
				}
			}

			ctx := context.Background()
			if tc.wantErr != nil {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			if err := g.Acquire(ctx, tc.request, nil); err != tc.wantErr {
				t.Errorf("Acquire(%d) = %v, want %v", tc.request, err, tc.wantErr)
			}
			if tc.checkUsed && g.Budget().ReservationsMB != tc.wantUsed {
				t.Errorf("reservations = %d, want %d", g.Budget().ReservationsMB, tc.wantUsed)
			}
		})
	}

	t.Run("releasing more than the ledger holds clamps it to zero", func(t *testing.T) {
		g := &MemoryGate{}
		g.SetCapacity(100)
		g.Release(200)
		if g.Budget().ReservationsMB != 0 {
			t.Errorf("used must clamp to 0, got %d", g.Budget().ReservationsMB)
		}
	})
}

func TestMemoryGateReleaseUnblocksWaiter(t *testing.T) {
	t.Parallel()
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
	if g.Budget().ReservationsMB != 600 {
		t.Errorf("expected used=600 after handoff, got %d", g.Budget().ReservationsMB)
	}
}

func TestMemoryGateCancelCleansUp(t *testing.T) {
	t.Parallel()
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
	if g.Budget().ReservationsMB != 500 {
		t.Errorf("cancel must not change used; got %d", g.Budget().ReservationsMB)
	}
}

func TestMemoryGateFIFONoJumping(t *testing.T) {
	t.Parallel()
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

// granted starts an Acquire in the background and closes the channel when it returns.
func granted(g *MemoryGate, amountMB int64) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		if err := g.Acquire(context.Background(), amountMB, nil); err != nil {
			return
		}
		close(done)
	}()
	return done
}

func staysBlocked(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("acquire was granted while the budget had no room")
	case <-time.After(2 * memoryGatePollInterval):
	}
}

func isGranted(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(4 * memoryGatePollInterval):
		t.Fatalf("acquire was still blocked after the budget made room")
	}
}

// The ceiling is MIKE's share of the machine: a host with nothing left admits a task while MIKE's own
// charge fits, and a task waits only once MIKE has spent the ceiling.
func TestMemoryGateAdmitsOnMIKEsOwnCharge(t *testing.T) {
	t.Parallel()
	g := &MemoryGate{}
	g.SetCapacity(1000)
	g.SetMeasurements(0, 0, 1000, 15000) // someone else's work fills the host; MIKE holds nothing

	isGranted(t, granted(g, 900))

	// MIKE's own charge now fills the ceiling, so the next task waits for MIKE to give memory back.
	g.SetMeasurements(1000, 1000, 1000, 15000)
	waiting := granted(g, 1)
	staysBlocked(t, waiting)

	g.SetMeasurements(99, 99, 1000, 15000) // the first grant still holds its 900 MB reservation
	isGranted(t, waiting)
}

// The two rules answer different questions and neither vetoes the other: a host with 2 % of its RAM
// free still admits a task whose charge fits MIKE's ceiling. What that leads to is the supervisor
// pausing someone (HostMemoryTight), not the gate refusing.
func TestMemoryGateAdmitsWhileTheHostIsTight(t *testing.T) {
	t.Parallel()
	g := &MemoryGate{}
	g.SetCapacity(1000)
	g.SetMeasurements(200, 200, 300, 15000)
	if !g.HostMemoryTight() {
		t.Fatal("fixture: 300 MB free of 15 GB must read as the host being nearly out")
	}
	isGranted(t, granted(g, 200))
}

// A container is charged max(held, limit) by the managers, so an idle sandbox capped at 800 MB leaves
// 200 MB of a 1000 MB ceiling — not the few megabytes it happens to hold.
func TestMemoryGateChargeAlreadyCarriesTheCap(t *testing.T) {
	t.Parallel()
	g := &MemoryGate{}
	g.SetCapacity(1000)
	g.SetMeasurements(800, 40, 14100, 15000) // one container capped at 800, holding 40

	done := granted(g, 300)
	staysBlocked(t, done)

	// The same task fits once the container's charge falls to what it holds.
	g.SetMeasurements(100, 100, 14100, 15000)
	isGranted(t, done)
}

// An uncapped task is admitted on measured room alone, and waits when there is none.
func TestMemoryGateUncappedTaskWaitsOnlyWhenThereIsNoRoom(t *testing.T) {
	t.Parallel()
	g := &MemoryGate{}
	g.SetCapacity(1000)
	if err := g.Acquire(context.Background(), 0, nil); err != nil {
		t.Fatalf("an unmeasured gate must admit immediately, got %v", err)
	}

	g.SetMeasurements(1000, 1000, 14000, 15000)
	done := granted(g, 0)
	staysBlocked(t, done)

	g.SetMeasurements(600, 600, 14400, 15000)
	isGranted(t, done)
}

// A host that cannot be read is not treated as an empty one: the budget falls back to its
// reservations.
func TestMemoryGateUnmeasuredFallsBackToLedger(t *testing.T) {
	t.Parallel()
	g := &MemoryGate{}
	g.SetCapacity(1000)
	if err := g.Acquire(context.Background(), 1000, nil); err != nil {
		t.Fatal(err)
	}
	if g.InUse() != 1000 {
		t.Errorf("InUse = %d, want 1000 (reservations only)", g.InUse())
	}
	done := granted(g, 1)
	staysBlocked(t, done)
}

// TestMemoryGateHostMemoryTight separates a host that is nearly out of RAM from one that was never
// read.
func TestMemoryGateHostMemoryTight(t *testing.T) {
	t.Parallel()
	g := &MemoryGate{}
	g.SetCapacity(1000)
	if g.HostMemoryTight() {
		t.Error("an unmeasured host is never tight — it is unread")
	}

	g.SetMeasurements(0, 0, 14800, 15000) // 99 % free
	if g.HostMemoryTight() {
		t.Error("a host with 14.8 GB free is not tight")
	}

	g.SetMeasurements(0, 0, 50, 15000) // under 5 % free
	if !g.HostMemoryTight() {
		t.Error("50 MB free of 15 GB must read as tight")
	}
}

// A non-sandboxed run books the reservation its sandbox would have taken, because its agent CLI runs on
// the host and no cgroup will ever report it. The booking survives confirmMemReservation — there is no
// container to hand it to — and comes back when the run ends.
func TestMemSlotReservationCoversAHostRun(t *testing.T) {
	t.Parallel()
	te, setup := memSlotFixture(t, false, 512)

	if !te.acquireMemSlot(setup) {
		t.Fatal("acquireMemSlot refused a run that fits under the capacity")
	}
	if got := te.memGate.Budget().ReservationsMB; got != 512 {
		t.Errorf("reservations = %d MB, want 512 for a host run on a project limited to 512", got)
	}

	te.confirmMemReservation(setup)
	if got := te.memGate.Budget().ReservationsMB; got != 512 {
		t.Errorf("reservations = %d MB after confirm, want 512: a host run has no container to be counted instead", got)
	}

	te.releaseMemSlot(setup)
	if got := te.memGate.Budget().ReservationsMB; got != 0 {
		t.Errorf("reservations = %d MB after the run ended, want 0", got)
	}
	if setup.memAcquiredMB != 0 {
		t.Error("releaseMemSlot must clear the booking so a later release cannot double-refund it")
	}
}

// A sandboxed run books the same number and hands it over at container creation: from then the budget
// counts the container, so holding the reservation too would charge it twice.
func TestMemSlotReservationIsSpentAtContainerCreation(t *testing.T) {
	t.Parallel()
	te, setup := memSlotFixture(t, true, 512)

	if !te.acquireMemSlot(setup) {
		t.Fatal("acquireMemSlot refused a run that fits under the capacity")
	}
	te.confirmMemReservation(setup)
	if got := te.memGate.Budget().ReservationsMB; got != 0 {
		t.Errorf("reservations = %d MB after the container exists, want 0", got)
	}
}

// An uncapped project reserves 0, so it is admitted on room alone rather than blocking on a number it
// never chose. The booking is still returned, which is the same call a capped run makes.
func TestMemSlotReservationOfAnUncappedProjectIsNothing(t *testing.T) {
	t.Parallel()
	te, setup := memSlotFixture(t, false, 0)

	if !te.acquireMemSlot(setup) {
		t.Fatal("acquireMemSlot refused a run that books nothing")
	}
	if got := te.memGate.Budget().ReservationsMB; got != 0 {
		t.Errorf("reservations = %d MB, want 0 for a project with no memory limit", got)
	}
}

// What the containers hold is reported beside what they cost, because the gap between the two is the
// number an operator needs to read a paused task's log: a container capped at 800 MB and holding 40
// occupies 800 MB of the ceiling.
func TestMemoryGateBudgetReportsHeldBesideCharged(t *testing.T) {
	t.Parallel()
	g := &MemoryGate{}
	g.SetCapacity(1000)
	g.SetMeasurements(800, 40, 14100, 15000)
	if err := g.Acquire(context.Background(), 150, nil); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	b := g.Budget()
	if b.MikeMB != 800 || b.HeldMB != 40 || b.ReservationsMB != 150 || b.InUseMB != 950 {
		t.Errorf("budget = %+v, want Mike 800 / Held 40 / Reservations 150 / InUse 950", b)
	}
	if !b.Measured || b.CapacityMB != 1000 || b.AvailableMB != 14100 {
		t.Errorf("budget = %+v, want Measured true, Capacity 1000, Available 14100", b)
	}
}

// memSlotFixture builds an executor with a 1 GB budget and a setup for one task on a project with the
// given memory limit, sandboxed or not.
func memSlotFixture(t *testing.T, sandboxed bool, limitMB int) (*TaskExecutor, *taskExecSetup) {
	t.Helper()
	s := testStore(t)
	te := newTestExecutorWith(t, Deps{Store: s})
	te.memGate.SetCapacity(1024)

	now := time.Now().UTC()
	project := &types.Project{
		ID: "memslot", Name: "memslot", Path: t.TempDir(),
		SandboxEnabled: sandboxed, SandboxMemoryLimit: limitMB,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateProject(project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if limitMB == 0 {
		// A fresh row is capped; setting the limit to 0 afterwards is the only way it runs uncapped.
		if err := s.UpdateProjectSandboxMemoryLimit(project.ID, 0); err != nil {
			t.Fatalf("clear the memory limit: %v", err)
		}
	}
	// The executor reads the row, so the setup works from it.
	project, err := s.GetProject(project.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	task := &types.Task{
		ID: "memslot-task", ProjectID: project.ID, Title: "memslot", Prompt: "p",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	stored, err := s.GetTaskByID(task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	setup := &taskExecSetup{
		task: stored, project: project, projectID: project.ID,
		sandboxEnabled: sandboxed, ctx: context.Background(),
	}
	setup.sandboxOpts.MemoryLimit = project.SandboxMemoryLimit
	return te, setup
}
