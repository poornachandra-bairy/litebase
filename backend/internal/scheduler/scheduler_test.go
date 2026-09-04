package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSchedulerRunsJobsOnInterval(t *testing.T) {
	s := New(quietLogger())

	var runs atomic.Int32
	s.Add(Job{
		Name:     "counter",
		Interval: 10 * time.Millisecond,
		Run: func(context.Context) error {
			runs.Add(1)
			return nil
		},
	})

	s.Start(context.Background())
	time.Sleep(120 * time.Millisecond)
	s.Stop()

	if got := runs.Load(); got < 3 {
		t.Errorf("job ran %d times in 120ms at a 10ms interval, want at least 3", got)
	}
}

func TestSchedulerStopWaitsForInFlightWork(t *testing.T) {
	s := New(quietLogger())

	var finished atomic.Bool
	started := make(chan struct{})
	s.Add(Job{
		Name:     "slow",
		Interval: 10 * time.Millisecond,
		Run: func(context.Context) error {
			select {
			case started <- struct{}{}:
			default:
			}
			// A backup in progress must not be abandoned mid-write.
			time.Sleep(60 * time.Millisecond)
			finished.Store(true)
			return nil
		},
	})

	s.Start(context.Background())
	<-started
	s.Stop()

	if !finished.Load() {
		t.Error("Stop returned while a job was still running")
	}
}

func TestSchedulerSurvivesJobFailure(t *testing.T) {
	s := New(quietLogger())

	var runs atomic.Int32
	s.Add(Job{
		Name:     "failing",
		Interval: 10 * time.Millisecond,
		Run: func(context.Context) error {
			runs.Add(1)
			return errors.New("expected failure")
		},
	})

	s.Start(context.Background())
	time.Sleep(80 * time.Millisecond)
	s.Stop()

	// A failing job must keep being retried rather than silently stopping.
	if got := runs.Load(); got < 2 {
		t.Errorf("failing job ran %d times, want it to keep running", got)
	}
}

func TestSchedulerSurvivesPanic(t *testing.T) {
	s := New(quietLogger())

	var runs atomic.Int32
	s.Add(Job{
		Name:     "panicking",
		Interval: 10 * time.Millisecond,
		Run: func(context.Context) error {
			runs.Add(1)
			panic("boom")
		},
	})

	s.Start(context.Background())
	time.Sleep(80 * time.Millisecond)
	s.Stop()

	// A panic in one tick must not kill the job's goroutine or the process.
	if got := runs.Load(); got < 2 {
		t.Errorf("job ran %d times after panicking, want it to continue", got)
	}
}

func TestSchedulerStopsOnContextCancel(t *testing.T) {
	s := New(quietLogger())

	var runs atomic.Int32
	s.Add(Job{
		Name:     "counter",
		Interval: 10 * time.Millisecond,
		Run: func(context.Context) error {
			runs.Add(1)
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	time.Sleep(40 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	before := runs.Load()
	time.Sleep(60 * time.Millisecond)
	if after := runs.Load(); after != before {
		t.Errorf("job kept running after the context was cancelled (%d -> %d)", before, after)
	}
	s.Stop()
}

func TestSchedulerIgnoresInvalidJobs(t *testing.T) {
	s := New(quietLogger())

	// A job with no interval or no function would otherwise spin or panic.
	s.Add(Job{Name: "no-interval", Run: func(context.Context) error { return nil }})
	s.Add(Job{Name: "no-func", Interval: time.Second})

	if len(s.jobs) != 0 {
		t.Errorf("registered %d invalid jobs, want 0", len(s.jobs))
	}
}

func TestStopIsSafeWithoutStart(t *testing.T) {
	s := New(quietLogger())
	// Shutdown runs unconditionally, so this must not panic or block.
	s.Stop()
	s.Stop()
}
