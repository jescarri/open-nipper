package cron

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
)

// cronJobKey returns a unique key for a job (userID:jobID) for multi-tenant entry map.
func cronJobKey(job config.CronJob) string {
	return job.UserID + ":" + job.ID
}

// atJobKey returns a unique key for an at job (userID:jobID).
func atJobKey(job config.AtJob) string {
	return job.UserID + ":" + job.ID
}

// MessageHandler is called when a cron job fires. The adapter registers the
// router's HandleMessage pipeline (or a wrapper) so the scheduler never
// imports the gateway package.
type MessageHandler func(ctx context.Context, msg *models.NipperMessage) error

// AtJobCleanup is called after an at job fires to remove it from the DB.
type AtJobCleanup func(ctx context.Context, id, userID string) error

// UserValidator checks whether a userId exists and is enabled in the datastore.
type UserValidator func(ctx context.Context, userID string) (bool, error)

// Scheduler wraps robfig/cron to fire jobs according to cron expressions.
// Each job produces a NipperMessage and hands it to the registered handler.
// It also supports one-shot "at" jobs that fire once at a specific time and auto-delete.
type Scheduler struct {
	cron    *cron.Cron
	logger  *zap.Logger
	handler MessageHandler

	mu       sync.RWMutex
	entryIDs map[string]cron.EntryID // cronJobKey(job) → cron entry
	jobs     []config.CronJob

	atTimers  map[string]*time.Timer // atJobKey(job) → timer
	atJobs    []config.AtJob
	atCleanup AtJobCleanup
}

// NewScheduler creates a Scheduler. Jobs are not started until Start is called.
// loc sets the timezone for cron expressions; if nil, time.UTC is used.
func NewScheduler(logger *zap.Logger, loc *time.Location) *Scheduler {
	if loc == nil {
		loc = time.UTC
	}
	return &Scheduler{
		cron:     cron.New(cron.WithSeconds(), cron.WithLocation(loc)),
		logger:   logger,
		entryIDs: make(map[string]cron.EntryID),
		atTimers: make(map[string]*time.Timer),
	}
}

// SetHandler sets the function called when a job fires. Must be called before Start.
func (s *Scheduler) SetHandler(h MessageHandler) {
	s.handler = h
}

// LoadJobs validates job configs against the datastore and registers valid
// jobs with the cron scheduler. Invalid jobs are logged and skipped.
func (s *Scheduler) LoadJobs(ctx context.Context, jobs []config.CronJob, validator UserValidator) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.jobs = nil

	for _, job := range jobs {
		if job.ID == "" {
			s.logger.Error("cron job skipped: missing id")
			continue
		}
		if job.Schedule == "" {
			s.logger.Error("cron job skipped: missing schedule", zap.String("jobId", job.ID))
			continue
		}
		if job.UserID == "" {
			s.logger.Error("cron job skipped: missing user_id", zap.String("jobId", job.ID))
			continue
		}
		if job.Prompt == "" {
			s.logger.Error("cron job skipped: missing prompt", zap.String("jobId", job.ID))
			continue
		}

		if validator != nil {
			ok, err := validator(ctx, job.UserID)
			if err != nil {
				s.logger.Error("cron job skipped: user validation failed",
					zap.String("jobId", job.ID),
					zap.String("userId", job.UserID),
					zap.Error(err),
				)
				continue
			}
			if !ok {
				s.logger.Error("cron job skipped: user not found or disabled",
					zap.String("jobId", job.ID),
					zap.String("userId", job.UserID),
				)
				continue
			}
		}

		j := job // capture for closure
		entryID, err := s.cron.AddFunc(job.Schedule, func() {
			s.fireJob(j)
		})
		if err != nil {
			s.logger.Error("cron job skipped: invalid schedule expression",
				zap.String("jobId", job.ID),
				zap.String("schedule", job.Schedule),
				zap.Error(err),
			)
			continue
		}

		s.entryIDs[cronJobKey(job)] = entryID
		s.jobs = append(s.jobs, job)

		s.logger.Info("cron job registered",
			zap.String("jobId", job.ID),
			zap.String("schedule", job.Schedule),
			zap.String("userId", job.UserID),
		)
	}

	return nil
}

// Start begins the cron scheduler in a background goroutine.
func (s *Scheduler) Start() {
	s.cron.Start()
	s.logger.Info("cron scheduler started", zap.Int("jobCount", len(s.jobs)))
}

// Stop gracefully stops the cron scheduler, waiting for running jobs to finish.
func (s *Scheduler) Stop() context.Context {
	s.logger.Info("cron scheduler stopping")
	return s.cron.Stop()
}

// Jobs returns a copy of the currently loaded jobs.
func (s *Scheduler) Jobs() []config.CronJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]config.CronJob, len(s.jobs))
	copy(out, s.jobs)
	return out
}

// JobCount returns the number of registered jobs.
func (s *Scheduler) JobCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.jobs)
}

// AddJob registers a single job with the cron scheduler (no user validation).
// Used when adding jobs at runtime via the agent API. Caller must ensure user exists.
func (s *Scheduler) AddJob(job config.CronJob) error {
	if job.ID == "" || job.Schedule == "" || job.UserID == "" || job.Prompt == "" {
		return fmt.Errorf("cron job missing required field (id, schedule, user_id, prompt)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := cronJobKey(job)
	if _, exists := s.entryIDs[key]; exists {
		return fmt.Errorf("cron job already registered: %s", key)
	}
	j := job
	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		s.fireJob(j)
	})
	if err != nil {
		return fmt.Errorf("invalid schedule %q: %w", job.Schedule, err)
	}
	s.entryIDs[key] = entryID
	s.jobs = append(s.jobs, job)
	s.logger.Info("cron job added",
		zap.String("jobId", job.ID),
		zap.String("userId", job.UserID),
		zap.String("schedule", job.Schedule),
	)
	return nil
}

// RemoveJob removes a job by userID and id. Returns true if a job was removed.
func (s *Scheduler) RemoveJob(userID, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := userID + ":" + id
	entryID, ok := s.entryIDs[key]
	if !ok {
		return false
	}
	s.cron.Remove(entryID)
	delete(s.entryIDs, key)
	for i, j := range s.jobs {
		if j.UserID == userID && j.ID == id {
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			break
		}
	}
	s.logger.Info("cron job removed", zap.String("userId", userID), zap.String("jobId", id))
	return true
}

// SetAtCleanup sets the function called to remove at jobs from the DB after firing.
func (s *Scheduler) SetAtCleanup(fn AtJobCleanup) {
	s.atCleanup = fn
}

// LoadAtJobs registers at jobs that haven't expired yet. Expired jobs are cleaned up.
func (s *Scheduler) LoadAtJobs(ctx context.Context, jobs []config.AtJob, validator UserValidator) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, job := range jobs {
		runAt, err := time.Parse(time.RFC3339, job.RunAt)
		if err != nil {
			s.logger.Error("at job skipped: invalid run_at", zap.String("jobId", job.ID), zap.Error(err))
			if s.atCleanup != nil {
				_ = s.atCleanup(ctx, job.ID, job.UserID)
			}
			continue
		}
		if runAt.Before(now) {
			s.logger.Info("at job expired, removing", zap.String("jobId", job.ID), zap.String("userId", job.UserID))
			if s.atCleanup != nil {
				_ = s.atCleanup(ctx, job.ID, job.UserID)
			}
			continue
		}
		if validator != nil {
			ok, err := validator(ctx, job.UserID)
			if err != nil || !ok {
				s.logger.Error("at job skipped: user not found or disabled",
					zap.String("jobId", job.ID), zap.String("userId", job.UserID))
				continue
			}
		}
		s.scheduleAtJob(job, runAt)
	}
}

// AddAtJob schedules a one-shot job. The job fires once at run_at and is then removed.
func (s *Scheduler) AddAtJob(job config.AtJob) error {
	if job.ID == "" || job.RunAt == "" || job.UserID == "" || job.Prompt == "" {
		return fmt.Errorf("at job missing required field (id, run_at, user_id, prompt)")
	}
	runAt, err := time.Parse(time.RFC3339, job.RunAt)
	if err != nil {
		return fmt.Errorf("invalid run_at %q: %w", job.RunAt, err)
	}
	if runAt.Before(time.Now()) {
		return fmt.Errorf("run_at is in the past")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := atJobKey(job)
	if _, exists := s.atTimers[key]; exists {
		return fmt.Errorf("at job already registered: %s", key)
	}
	s.scheduleAtJob(job, runAt)
	s.logger.Info("at job added",
		zap.String("jobId", job.ID),
		zap.String("userId", job.UserID),
		zap.String("runAt", job.RunAt),
	)
	return nil
}

// RemoveAtJob cancels and removes a pending at job. Returns true if removed.
func (s *Scheduler) RemoveAtJob(userID, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := userID + ":" + id
	timer, ok := s.atTimers[key]
	if !ok {
		return false
	}
	timer.Stop()
	delete(s.atTimers, key)
	for i, j := range s.atJobs {
		if j.UserID == userID && j.ID == id {
			s.atJobs = append(s.atJobs[:i], s.atJobs[i+1:]...)
			break
		}
	}
	s.logger.Info("at job removed", zap.String("userId", userID), zap.String("jobId", id))
	return true
}

// AtJobs returns a copy of the currently pending at jobs.
func (s *Scheduler) AtJobs() []config.AtJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]config.AtJob, len(s.atJobs))
	copy(out, s.atJobs)
	return out
}

// scheduleAtJob sets up a timer for a one-shot job. Must be called with mu held.
func (s *Scheduler) scheduleAtJob(job config.AtJob, runAt time.Time) {
	delay := time.Until(runAt)
	j := job
	key := atJobKey(job)
	timer := time.AfterFunc(delay, func() {
		s.fireAtJob(j)
		// Auto-cleanup from scheduler state.
		s.mu.Lock()
		delete(s.atTimers, key)
		for i, aj := range s.atJobs {
			if aj.UserID == j.UserID && aj.ID == j.ID {
				s.atJobs = append(s.atJobs[:i], s.atJobs[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		// Remove from DB.
		if s.atCleanup != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.atCleanup(ctx, j.ID, j.UserID); err != nil {
				s.logger.Error("at job DB cleanup failed", zap.String("jobId", j.ID), zap.Error(err))
			}
		}
	})
	s.atTimers[key] = timer
	s.atJobs = append(s.atJobs, job)
}

// fireAtJob constructs a NipperMessage for a one-shot at job and calls the handler.
func (s *Scheduler) fireAtJob(job config.AtJob) {
	if s.handler == nil {
		s.logger.Error("at job fired but no handler registered", zap.String("jobId", job.ID))
		return
	}

	now := time.Now().UTC()
	msg := &models.NipperMessage{
		MessageID:       uuid.New().String(),
		OriginMessageID: fmt.Sprintf("at:%s:%d", job.ID, now.UnixMilli()),
		UserID:          job.UserID,
		ChannelType:     models.ChannelTypeCron,
		ChannelIdentity: fmt.Sprintf("at:%s", job.ID),
		Content: models.MessageContent{
			Text: job.Prompt,
		},
		DeliveryContext: models.DeliveryContext{
			ChannelType:    models.ChannelTypeCron,
			ChannelID:      job.ID,
			ReplyMode:      "broadcast",
			NotifyChannels: job.NotifyChannels,
			Capabilities:   models.CronCapabilities(),
		},
		Meta: models.CronMeta{
			JobID:      job.ID,
			ScheduleAt: now.Format(time.RFC3339),
		},
		Timestamp: now,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.handler(ctx, msg); err != nil {
		s.logger.Error("at job handler failed",
			zap.String("jobId", job.ID),
			zap.String("userId", job.UserID),
			zap.Error(err),
		)
	} else {
		s.logger.Info("at job fired",
			zap.String("jobId", job.ID),
			zap.String("userId", job.UserID),
			zap.String("messageId", msg.MessageID),
		)
	}
}

// fireJob constructs a NipperMessage and calls the registered handler.
func (s *Scheduler) fireJob(job config.CronJob) {
	if s.handler == nil {
		s.logger.Error("cron job fired but no handler registered", zap.String("jobId", job.ID))
		return
	}

	now := time.Now().UTC()
	msg := &models.NipperMessage{
		MessageID:       uuid.New().String(),
		OriginMessageID: fmt.Sprintf("cron:%s:%d", job.ID, now.UnixMilli()),
		UserID:          job.UserID,
		ChannelType:     models.ChannelTypeCron,
		ChannelIdentity: fmt.Sprintf("cron:%s", job.ID),
		Content: models.MessageContent{
			Text: job.Prompt,
		},
		DeliveryContext: models.DeliveryContext{
			ChannelType:    models.ChannelTypeCron,
			ChannelID:      job.ID,
			ReplyMode:      "broadcast",
			NotifyChannels: job.NotifyChannels,
			Capabilities:   models.CronCapabilities(),
		},
		Meta: models.CronMeta{
			JobID:      job.ID,
			ScheduleAt: now.Format(time.RFC3339),
		},
		Timestamp: now,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.handler(ctx, msg); err != nil {
		s.logger.Error("cron job handler failed",
			zap.String("jobId", job.ID),
			zap.String("userId", job.UserID),
			zap.Error(err),
		)
	} else {
		s.logger.Info("cron job fired",
			zap.String("jobId", job.ID),
			zap.String("userId", job.UserID),
			zap.String("messageId", msg.MessageID),
		)
	}
}
