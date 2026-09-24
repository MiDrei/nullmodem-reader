// Package sched runs mail exchanges unattended, on a schedule.
package sched

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// Backoff bounds. A board that is down stays down for a while, so
// retrying every few minutes achieves nothing but noise in its log;
// an hour is long enough to stop hammering and short enough that a
// board fixed over lunch is picked up the same afternoon.
const (
	MinDelay   = 30 * time.Second
	MaxBackoff = time.Hour
	// JitterFraction is how much of a delay is randomised, in each
	// direction.
	JitterFraction = 0.1
)

// nextDelay is how long to wait before the next attempt.
//
// After a failure the interval doubles per consecutive failure, up to
// MaxBackoff. Jitter is applied either way: without it, a machine
// polling several boards configured with the same interval would hit
// them all in the same second forever, and every reader that started
// at boot would hit one board together.
func nextDelay(interval time.Duration, failures int, jitter func(time.Duration) time.Duration) time.Duration {
	d := interval
	if d < MinDelay {
		d = MinDelay
	}
	for i := 0; i < failures && d < MaxBackoff; i++ {
		d *= 2
	}
	if d > MaxBackoff {
		d = MaxBackoff
	}
	if jitter != nil {
		d += jitter(d)
	}
	if d < MinDelay {
		d = MinDelay
	}
	return d
}

// randomJitter spreads a delay by up to JitterFraction either way.
func randomJitter(d time.Duration) time.Duration {
	spread := float64(d) * JitterFraction
	return time.Duration((rand.Float64()*2 - 1) * spread)
}

// ErrSkipped means the exchange did not run because another one held
// the lock.
//
// The scheduler treats it as a normal outcome rather than a failure:
// it means the locking worked, and backing off because someone else
// is already fetching the same mail would be exactly wrong.
var ErrSkipped = errors.New("sched: another exchange holds the lock")

// EventKind says what happened.
type EventKind int

const (
	// Polling is emitted before an attempt, so a log shows the reader
	// is alive between hours of nothing.
	Polling EventKind = iota
	// NoNewMail means the board had nothing waiting.
	NoNewMail
	// Exchanged means something moved in one direction or both.
	Exchanged
	// Skipped means another exchange held the lock.
	Skipped
	// Failed means the attempt errored; Err says why.
	Failed
)

// Event is one thing the scheduler did.
type Event struct {
	Kind   EventKind
	System string
	Time   time.Time
	// Sent and Received count messages, for Exchanged.
	Sent, Received int
	// Next is when the scheduler will try again.
	Next time.Time
	Err  error
}

// Job is one system to poll.
type Job struct {
	// ID names the system in events.
	ID string
	// Interval is how often to poll. Below MinDelay it is raised to
	// it: a reader hammering a hobby board every few seconds is a
	// misconfiguration, not a preference.
	Interval time.Duration
	// Exchange performs one exchange. Returning ErrSkipped means
	// another exchange held the lock, which is a normal outcome
	// rather than a failure worth backing off over.
	Exchange func(context.Context) (Outcome, error)
}

// Outcome is what one exchange moved.
type Outcome struct {
	Sent, Received int
	NoNewMail      bool
}

// Scheduler polls a set of systems until its context is cancelled.
type Scheduler struct {
	Jobs []Job
	// Events receives what happened. A nil channel discards them; a
	// full one drops rather than blocking, because a stalled consumer
	// must not stop mail from being fetched.
	Events chan<- Event
	// Now and Sleep exist so the timing can be tested without
	// waiting. Both default to the real thing.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	// Jitter defaults to randomJitter; WithoutJitter turns it off.
	Jitter   func(time.Duration) time.Duration
	noJitter bool
}

// WithoutJitter makes delays exact, for tests and for anyone who
// would rather their exchanges happened on the minute.
func (s *Scheduler) WithoutJitter() *Scheduler {
	s.noJitter = true
	return s
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) sleep(ctx context.Context, d time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s *Scheduler) jitter() func(time.Duration) time.Duration {
	if s.noJitter {
		return nil
	}
	if s.Jitter != nil {
		return s.Jitter
	}
	return randomJitter
}

func (s *Scheduler) emit(e Event) {
	if s.Events == nil {
		return
	}
	select {
	case s.Events <- e:
	default: // a stalled consumer must not stop the mail
	}
}

// Run polls every job until ctx is cancelled, then returns nil.
//
// Each job runs in its own goroutine with its own schedule and its
// own backoff, so one unreachable board does not delay the others.
func (s *Scheduler) Run(ctx context.Context) error {
	done := make(chan struct{}, len(s.Jobs))
	for _, job := range s.Jobs {
		go func(j Job) {
			defer func() { done <- struct{}{} }()
			s.runJob(ctx, j)
		}(job)
	}
	for range s.Jobs {
		<-done
	}
	return nil
}

// runJob polls one system until the context is cancelled.
//
// It exchanges first and waits afterwards: someone who starts the
// scheduler wants their mail now, not in half an hour.
func (s *Scheduler) runJob(ctx context.Context, j Job) {
	failures := 0
	for {
		s.emit(Event{Kind: Polling, System: j.ID, Time: s.now()})

		out, err := j.Exchange(ctx)

		// An exchange cut short by the shutdown itself is not an
		// outcome worth reporting, and must not count as a failure to
		// back off over.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}

		// One event per attempt, carrying both what happened and when
		// the next one is due -- a consumer that had to correlate two
		// events to show "nothing new, next at 14:30" would be doing
		// the scheduler's bookkeeping for it.
		ev := Event{System: j.ID, Time: s.now()}
		switch {
		case errors.Is(err, ErrSkipped):
			ev.Kind = Skipped
		case err != nil:
			failures++
			ev.Kind, ev.Err = Failed, err
		default:
			failures = 0
			ev.Kind, ev.Sent, ev.Received = Exchanged, out.Sent, out.Received
			if out.NoNewMail && out.Sent == 0 {
				ev.Kind = NoNewMail
			}
		}

		// The attempt is reported even when the shutdown arrived while
		// it ran: a packet that landed before someone pressed Ctrl-C
		// still landed, and dropping that event would lose the only
		// record of it. Next stays zero in that case, since there
		// will not be one.
		stopping := ctx.Err() != nil
		var delay time.Duration
		if !stopping {
			delay = nextDelay(j.Interval, failures, s.jitter())
			ev.Next = ev.Time.Add(delay)
		}
		s.emit(ev)
		if stopping {
			return
		}

		if err := s.sleep(ctx, delay); err != nil {
			return
		}
	}
}
