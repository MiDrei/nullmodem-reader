package sched

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNextDelayHonoursTheInterval(t *testing.T) {
	if got := nextDelay(10*time.Minute, 0, nil); got != 10*time.Minute {
		t.Fatalf("nextDelay = %v, want the configured interval", got)
	}
}

// A reader hammering a hobby board every few seconds is a
// misconfiguration, not a preference.
func TestNextDelayRaisesTooShortAnInterval(t *testing.T) {
	if got := nextDelay(time.Second, 0, nil); got != MinDelay {
		t.Fatalf("nextDelay = %v, want it raised to %v", got, MinDelay)
	}
}

func TestNextDelayDoublesPerConsecutiveFailure(t *testing.T) {
	interval := time.Minute
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{0, time.Minute},
		{1, 2 * time.Minute},
		{2, 4 * time.Minute},
		{3, 8 * time.Minute},
	} {
		if got := nextDelay(interval, tc.failures, nil); got != tc.want {
			t.Errorf("nextDelay(%v, %d) = %v, want %v", interval, tc.failures, got, tc.want)
		}
	}
}

// A board that has been down for a day should be polled hourly, not
// once a fortnight.
func TestNextDelayIsCappedAtMaxBackoff(t *testing.T) {
	if got := nextDelay(10*time.Minute, 50, nil); got != MaxBackoff {
		t.Fatalf("nextDelay = %v, want it capped at %v", got, MaxBackoff)
	}
}

func TestJitterStaysWithinItsFraction(t *testing.T) {
	const d = time.Hour
	limit := time.Duration(float64(d) * JitterFraction)

	var sawBelow, sawAbove bool
	for i := 0; i < 500; i++ {
		got := nextDelay(d, 0, randomJitter)
		if got < d-limit || got > d+limit {
			t.Fatalf("nextDelay = %v, outside %v ± %v", got, d, limit)
		}
		if got < d {
			sawBelow = true
		}
		if got > d {
			sawAbove = true
		}
	}
	// Jitter that only ever ran one way would not spread anything.
	if !sawBelow || !sawAbove {
		t.Fatal("jitter never went in both directions")
	}
}

// Jitter must never pull a delay below the floor that protects the
// board.
func TestJitterCannotBreachTheMinimum(t *testing.T) {
	huge := func(time.Duration) time.Duration { return -time.Hour }
	if got := nextDelay(MinDelay, 0, huge); got != MinDelay {
		t.Fatalf("nextDelay = %v, want it held at %v", got, MinDelay)
	}
}

// ---------------------------------------------------------- the loop

// runner drives a scheduler for a fixed number of attempts and
// collects what it did, without any real waiting.
type runner struct {
	mu       sync.Mutex
	delays   []time.Duration
	attempts int
	cancel   context.CancelFunc
	limit    int
}

func (r *runner) sleep(ctx context.Context, d time.Duration) error {
	r.mu.Lock()
	r.delays = append(r.delays, d)
	r.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func (r *runner) count() {
	r.mu.Lock()
	r.attempts++
	if r.attempts >= r.limit {
		r.cancel()
	}
	r.mu.Unlock()
}

// drive runs one job until it has been attempted limit times.
func drive(t *testing.T, interval time.Duration, limit int, exchange func(int) (Outcome, error)) ([]Event, []time.Duration) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &runner{cancel: cancel, limit: limit}

	events := make(chan Event, limit*2)
	s := (&Scheduler{
		Events: events,
		Sleep:  r.sleep,
		Jobs: []Job{{
			ID:       "TEST",
			Interval: interval,
			Exchange: func(context.Context) (Outcome, error) {
				r.mu.Lock()
				n := r.attempts
				r.mu.Unlock()
				out, err := exchange(n)
				r.count()
				return out, err
			},
		}},
	}).WithoutJitter()

	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler did not stop when its context was cancelled")
	}
	close(events)

	var got []Event
	for e := range events {
		got = append(got, e)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return got, r.delays
}

// Someone who starts the scheduler wants their mail now, not in half
// an hour.
func TestSchedulerExchangesBeforeItWaits(t *testing.T) {
	events, delays := drive(t, 10*time.Minute, 2, func(int) (Outcome, error) {
		return Outcome{Received: 3}, nil
	})

	first := firstOfKind(t, events, Exchanged)
	if first.Received != 3 {
		t.Fatalf("Received = %d, want 3", first.Received)
	}
	if first.Next.IsZero() {
		t.Fatal("the event does not say when the next attempt is due")
	}
	if len(delays) == 0 {
		t.Fatal("the scheduler never scheduled a next attempt")
	}
	if delays[0] != 10*time.Minute {
		t.Fatalf("first delay = %v, want the interval", delays[0])
	}
}

// firstOfKind returns the first event of a kind, failing the test
// when there is none.
func firstOfKind(t *testing.T, events []Event, kind EventKind) Event {
	t.Helper()
	for _, e := range events {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no event of kind %d in %+v", kind, events)
	return Event{}
}

// An attempt interrupted by the shutdown is still reported -- a
// packet that landed before someone pressed Ctrl-C still landed --
// but it names no next attempt, because there will not be one.
func TestTheLastAttemptIsReportedWithoutANextTime(t *testing.T) {
	events, _ := drive(t, time.Minute, 1, func(int) (Outcome, error) {
		return Outcome{Received: 2}, nil
	})

	last := firstOfKind(t, events, Exchanged)
	if last.Received != 2 {
		t.Fatalf("Received = %d, want the fetch reported", last.Received)
	}
	if !last.Next.IsZero() {
		t.Fatalf("Next = %v, want none: the scheduler is stopping", last.Next)
	}
}

func TestSchedulerBacksOffAfterFailuresAndRecovers(t *testing.T) {
	_, delays := drive(t, time.Minute, 4, func(n int) (Outcome, error) {
		if n < 2 {
			return Outcome{}, errors.New("connection refused")
		}
		return Outcome{}, nil
	})

	if len(delays) < 3 {
		t.Fatalf("delays = %v, want one per attempt", delays)
	}
	if delays[0] != 2*time.Minute {
		t.Fatalf("after one failure the delay was %v, want it doubled", delays[0])
	}
	if delays[1] != 4*time.Minute {
		t.Fatalf("after two failures the delay was %v, want it doubled again", delays[1])
	}
	// A success must reset the backoff, not merely stop increasing it.
	if delays[2] != time.Minute {
		t.Fatalf("after a success the delay was %v, want the interval back", delays[2])
	}
}

// The lock doing its job is not a failure, and backing off because
// someone else is already fetching the same mail would be wrong.
func TestSkippedDoesNotCountAsAFailure(t *testing.T) {
	events, delays := drive(t, time.Minute, 2, func(int) (Outcome, error) {
		return Outcome{}, ErrSkipped
	})

	for _, d := range delays {
		if d != time.Minute {
			t.Fatalf("delay = %v, want the interval unchanged by a skip", d)
		}
	}
	var skipped int
	for _, e := range events {
		if e.Kind == Skipped {
			skipped++
		}
		if e.Kind == Failed {
			t.Fatalf("a skip was reported as a failure: %+v", e)
		}
	}
	if skipped == 0 {
		t.Fatalf("no Skipped event in %+v", events)
	}
}

func TestNoNewMailIsReportedSeparately(t *testing.T) {
	events, _ := drive(t, time.Minute, 2, func(int) (Outcome, error) {
		return Outcome{NoNewMail: true}, nil
	})

	if e := firstOfKind(t, events, NoNewMail); e.Next.IsZero() {
		t.Fatal("the event does not say when the next attempt is due")
	}
}

// Sending something counts as an exchange even when nothing came
// back.
func TestSendingWithNothingToFetchIsAnExchange(t *testing.T) {
	events, _ := drive(t, time.Minute, 2, func(int) (Outcome, error) {
		return Outcome{Sent: 2, NoNewMail: true}, nil
	})

	for _, e := range events {
		if e.Kind == NoNewMail {
			t.Fatalf("two messages went out and it was reported as nothing happening: %+v", e)
		}
	}
}

// A consumer that stops reading must not stop the mail.
func TestAFullEventChannelDoesNotStallTheScheduler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	attempts := 0
	var mu sync.Mutex
	events := make(chan Event, 1) // deliberately tiny, never drained

	s := (&Scheduler{
		Events: events,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			return ctx.Err()
		},
		Jobs: []Job{{
			ID:       "TEST",
			Interval: time.Minute,
			Exchange: func(context.Context) (Outcome, error) {
				mu.Lock()
				attempts++
				if attempts >= 5 {
					cancel()
				}
				mu.Unlock()
				return Outcome{}, nil
			},
		}},
	}).WithoutJitter()

	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler blocked on a full event channel")
	}
}

func TestEachJobKeepsItsOwnSchedule(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	seen := map[string]int{}

	exchange := func(id string) func(context.Context) (Outcome, error) {
		return func(context.Context) (Outcome, error) {
			mu.Lock()
			seen[id]++
			total := seen["A"] + seen["B"]
			mu.Unlock()
			if total >= 4 {
				cancel()
			}
			if id == "B" {
				return Outcome{}, errors.New("B is down")
			}
			return Outcome{}, nil
		}
	}

	s := (&Scheduler{
		Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		Jobs: []Job{
			{ID: "A", Interval: time.Minute, Exchange: exchange("A")},
			{ID: "B", Interval: time.Minute, Exchange: exchange("B")},
		},
	}).WithoutJitter()

	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler did not stop")
	}

	mu.Lock()
	defer mu.Unlock()
	// One board being down must not stop the other from being polled.
	if seen["A"] == 0 {
		t.Fatalf("A was never polled while B was failing: %v", seen)
	}
}
