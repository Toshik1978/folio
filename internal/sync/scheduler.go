package sync

import (
	"context"
	"fmt"
	"log/slog"
	stdsync "sync"
	"time"

	"github.com/go-co-op/gocron/v2"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// scheduler owns the gocron scheduler and the per-library interval job set. It
// fires its trigger callback (wired to Engine.TriggerLibrary) when a library's
// timer elapses. Its mutex guards the job maps, so the engine's queue lock (e.mu)
// no longer serializes job reconciliation.
type scheduler struct {
	log     *slog.Logger
	sched   gocron.Scheduler
	trigger func(int64)

	mu    stdsync.Mutex
	jobs  map[int64]gocron.Job
	jobIv map[int64]int64 // library ID -> scheduled interval (to detect changes)
}

// newScheduler creates the gocron scheduler and the empty job set. trigger is
// invoked with a library ID when its interval elapses.
func newScheduler(log *slog.Logger, trigger func(int64)) (*scheduler, error) {
	sched, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("create scheduler: %w", err)
	}

	return &scheduler{
		log:     log,
		sched:   sched,
		trigger: trigger,
		jobs:    make(map[int64]gocron.Job),
		jobIv:   make(map[int64]int64),
	}, nil
}

// start begins firing scheduled jobs.
func (s *scheduler) start() { s.sched.Start() }

// shutdown stops the scheduler and waits for running jobs to return.
func (s *scheduler) shutdown() error {
	if err := s.sched.Shutdown(); err != nil {
		return fmt.Errorf("failed to shutdown: %w", err)
	}

	return nil
}

// every registers a recurring task on the scheduler (used for the purge sweep).
func (s *scheduler) every(d time.Duration, task func()) error {
	if _, err := s.sched.NewJob(gocron.DurationJob(d), gocron.NewTask(task)); err != nil {
		return fmt.Errorf("schedule recurring task: %w", err)
	}

	return nil
}

// reconcile brings the job set in line with want (library ID -> interval seconds):
// jobs no longer wanted or whose interval changed are removed, and missing jobs are
// added. It owns s.mu.
func (s *scheduler) reconcile(want map[int64]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(want)
	s.add(want)
}

// prune removes scheduled jobs that are no longer wanted or whose interval
// changed. The caller must hold s.mu.
func (s *scheduler) prune(want map[int64]int64) {
	for id, job := range s.jobs {
		if iv, ok := want[id]; ok && s.jobIv[id] == iv {
			continue
		}
		_ = s.sched.RemoveJob(job.ID())
		delete(s.jobs, id)
		delete(s.jobIv, id)
	}
}

// add schedules a DurationJob for every wanted library that is not already
// scheduled. The caller must hold s.mu.
func (s *scheduler) add(want map[int64]int64) {
	for id, iv := range want {
		if _, ok := s.jobs[id]; ok {
			continue
		}
		libraryID := id
		job, err := s.sched.NewJob(
			gocron.DurationJob(time.Duration(iv)*time.Second),
			gocron.NewTask(func() { s.trigger(libraryID) }),
		)
		if err != nil {
			s.log.Error("schedule library", slog.Int64("library", libraryID), slog.Any("error", err))
			continue
		}
		s.jobs[id] = job
		s.jobIv[id] = iv
	}
}

// Reschedule reconciles the gocron job set and the folder watches with the
// libraries table: each active library gets a DurationJob firing TriggerLibrary on
// its interval; libraries that vanished, entered pending_purge, or changed interval
// have their jobs removed (and re-added with the new interval). Call it once at
// startup and whenever libraries change.
func (e *Engine) Reschedule(ctx context.Context) error {
	srcs, err := dbq.New(e.db).ListLibraries(ctx)
	if err != nil {
		return fmt.Errorf("list libraries: %w", err)
	}

	e.scheduler.reconcile(schedulableIntervals(srcs))

	// Reconcile folder-library watches outside the scheduler lock: a directory walk
	// must not block job reconciliation.
	e.reconcileWatches(srcs)

	return nil
}

// schedulableIntervals maps each library that should run on a timer to its
// interval in seconds, skipping libraries awaiting deletion or with no interval.
func schedulableIntervals(srcs []dbq.Library) map[int64]int64 {
	want := make(map[int64]int64, len(srcs))
	for i := range srcs {
		if srcs[i].Status == statusPendingPurge || srcs[i].SyncIntervalSeconds <= 0 {
			continue
		}
		want[srcs[i].ID] = srcs[i].SyncIntervalSeconds
	}

	return want
}
