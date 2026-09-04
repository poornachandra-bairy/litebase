// Package scheduler runs Litebase's periodic background work: due backups and
// housekeeping such as expiring sessions.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Job is one unit of recurring work.
type Job struct {
	// Name identifies the job in logs.
	Name string
	// Interval is how often the job runs.
	Interval time.Duration
	// Run performs the work. A returned error is logged; it does not stop the
	// scheduler.
	Run func(context.Context) error
}

// Scheduler runs jobs on a fixed cadence until stopped.
//
// It is deliberately simple: one goroutine per job, a shared cancellable
// context, and a WaitGroup so shutdown waits for in-flight work. A cron
// expression parser would add a dependency for no benefit, since the intervals
// here are all plain durations.
type Scheduler struct {
	log  *slog.Logger
	jobs []Job

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
}

// New builds a scheduler.
func New(log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{log: log}
}

// Add registers a job. It must be called before Start.
func (s *Scheduler) Add(j Job) {
	if j.Interval <= 0 || j.Run == nil {
		return
	}
	s.jobs = append(s.jobs, j)
}

// Start begins running the registered jobs.
func (s *Scheduler) Start(parent context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}

	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.running = true

	for _, job := range s.jobs {
		s.wg.Add(1)
		go s.runJob(ctx, job)
	}
	s.log.Info("scheduler started", "jobs", len(s.jobs))
}

func (s *Scheduler) runJob(ctx context.Context, job Job) {
	defer s.wg.Done()

	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.execute(ctx, job)
		}
	}
}

// execute runs one tick, isolating panics so a faulty job cannot bring down the
// process or silently kill its own goroutine.
func (s *Scheduler) execute(ctx context.Context, job Job) {
	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("scheduled job panicked", "job", job.Name, "panic", rec)
		}
	}()

	start := time.Now()
	if err := job.Run(ctx); err != nil {
		// A cancelled context during shutdown is expected, not a failure.
		if ctx.Err() != nil {
			return
		}
		s.log.Error("scheduled job failed", "job", job.Name, "error", err,
			"duration_ms", time.Since(start).Milliseconds())
		return
	}
	s.log.Debug("scheduled job completed", "job", job.Name,
		"duration_ms", time.Since(start).Milliseconds())
}

// Stop cancels the jobs and waits for any in-flight run to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	wasRunning := s.running
	s.running = false
	s.mu.Unlock()

	if !wasRunning || cancel == nil {
		return
	}
	cancel()
	s.wg.Wait()
	s.log.Info("scheduler stopped")
}
