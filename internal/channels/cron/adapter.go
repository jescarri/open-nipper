// Package cron implements the ChannelAdapter for scheduled/headless jobs.
//
// The cron adapter is unique among channel adapters: it has no external
// transport. Instead it runs an internal scheduler (robfig/cron) that fires
// NipperMessages into the message pipeline at configured intervals. Responses
// are delivered via broadcast to the job's notifyChannels — the cron adapter
// itself never delivers responses outbound.
package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
)

// Adapter implements channels.ChannelAdapter for the cron channel.
type Adapter struct {
	cfg         config.CronChannelConfig
	scheduler   *Scheduler
	logger      *zap.Logger
	validator   UserValidator
	initialJobs []config.CronJob // if set, Start loads these instead of cfg.Jobs (e.g. from DB)
}

// AdapterDeps bundles the dependencies for constructing a Cron Adapter.
type AdapterDeps struct {
	Config    config.CronChannelConfig
	Logger    *zap.Logger
	Validator UserValidator
}

// NewAdapter creates a cron adapter with an internal scheduler.
func NewAdapter(deps AdapterDeps) *Adapter {
	return &Adapter{
		cfg:       deps.Config,
		scheduler: NewScheduler(deps.Logger),
		logger:    deps.Logger,
		validator: deps.Validator,
	}
}

// ChannelType returns ChannelTypeCron.
func (a *Adapter) ChannelType() models.ChannelType {
	return models.ChannelTypeCron
}

// Start loads jobs (from SetInitialJobs if set, else config), validates user IDs, and starts the scheduler.
func (a *Adapter) Start(ctx context.Context) error {
	jobs := a.cfg.Jobs
	if len(a.initialJobs) > 0 {
		jobs = a.initialJobs
	}
	if err := a.scheduler.LoadJobs(ctx, jobs, a.validator); err != nil {
		return fmt.Errorf("cron: loading jobs: %w", err)
	}
	a.scheduler.Start()
	a.logger.Info("cron adapter started", zap.Int("jobs", a.scheduler.JobCount()))
	return nil
}

// Stop gracefully shuts down the cron scheduler.
func (a *Adapter) Stop(_ context.Context) error {
	stopCtx := a.scheduler.Stop()
	<-stopCtx.Done()
	a.logger.Info("cron adapter stopped")
	return nil
}

// HealthCheck returns nil when the scheduler has at least one loaded job, or
// when no jobs were configured (valid state). Uses initial job count when initialJobs was set.
func (a *Adapter) HealthCheck(_ context.Context) error {
	expected := len(a.cfg.Jobs)
	if len(a.initialJobs) > 0 {
		expected = len(a.initialJobs)
	}
	if expected > 0 && a.scheduler.JobCount() == 0 {
		return fmt.Errorf("cron: all jobs failed validation")
	}
	return nil
}

// NormalizeInbound converts a raw JSON payload into a NipperMessage.
// In normal operation the scheduler produces NipperMessages directly, so this
// method is primarily used for testing and manual job triggering via the admin API.
func (a *Adapter) NormalizeInbound(_ context.Context, raw []byte) (*models.NipperMessage, error) {
	var payload struct {
		JobID  string `json:"jobId"`
		UserID string `json:"userId"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("cron normalise: %w", err)
	}
	if payload.JobID == "" || payload.UserID == "" || payload.Prompt == "" {
		return nil, fmt.Errorf("cron normalise: jobId, userId, and prompt are required")
	}

	now := time.Now().UTC()

	var notifyChannels []string
	for _, j := range a.scheduler.Jobs() {
		if j.ID == payload.JobID && j.UserID == payload.UserID {
			notifyChannels = j.NotifyChannels
			break
		}
	}
	if notifyChannels == nil {
		for _, j := range a.cfg.Jobs {
			if j.ID == payload.JobID {
				notifyChannels = j.NotifyChannels
				break
			}
		}
	}

	return &models.NipperMessage{
		MessageID:       uuid.New().String(),
		OriginMessageID: fmt.Sprintf("cron:%s:%d", payload.JobID, now.UnixMilli()),
		UserID:          payload.UserID,
		ChannelType:     models.ChannelTypeCron,
		ChannelIdentity: fmt.Sprintf("cron:%s", payload.JobID),
		Content: models.MessageContent{
			Text: payload.Prompt,
		},
		DeliveryContext: models.DeliveryContext{
			ChannelType:    models.ChannelTypeCron,
			ChannelID:      payload.JobID,
			ReplyMode:      "broadcast",
			NotifyChannels: notifyChannels,
			Capabilities:   models.CronCapabilities(),
		},
		Meta: models.CronMeta{
			JobID:      payload.JobID,
			ScheduleAt: now.Format(time.RFC3339),
		},
		Timestamp: now,
	}, nil
}

// DeliverResponse is a no-op for the cron channel. Cron jobs are headless;
// responses are broadcast to notifyChannels by the dispatcher, not by this adapter.
func (a *Adapter) DeliverResponse(_ context.Context, _ *models.NipperResponse) error {
	return nil
}

// DeliverEvent is a no-op for the cron channel (no streaming support).
func (a *Adapter) DeliverEvent(_ context.Context, _ *models.NipperEvent) error {
	return nil
}

// SetHandler sets the message handler on the underlying scheduler. This must
// be called before Start.
func (a *Adapter) SetHandler(h MessageHandler) {
	a.scheduler.SetHandler(h)
}

// SetInitialJobs sets the job list to load on Start instead of config. Used when
// loading jobs from the datastore at gateway startup. Must be called before Start.
func (a *Adapter) SetInitialJobs(jobs []config.CronJob) {
	a.initialJobs = jobs
}

// AddJob registers a single job at runtime (e.g. via agent API). No user validation.
func (a *Adapter) AddJob(_ context.Context, job config.CronJob) error {
	return a.scheduler.AddJob(job)
}

// RemoveJob removes a job by userID and id. Returns true if a job was removed.
func (a *Adapter) RemoveJob(_ context.Context, userID, id string) bool {
	return a.scheduler.RemoveJob(userID, id)
}

// SetAtCleanup sets the function called to remove at jobs from the DB after firing.
func (a *Adapter) SetAtCleanup(fn AtJobCleanup) {
	a.scheduler.SetAtCleanup(fn)
}

// LoadAtJobs loads one-shot at jobs from the DB into the scheduler.
func (a *Adapter) LoadAtJobs(ctx context.Context, jobs []config.AtJob) {
	a.scheduler.LoadAtJobs(ctx, jobs, a.validator)
	a.logger.Info("at jobs loaded", zap.Int("count", len(a.scheduler.AtJobs())))
}

// AddAtJob registers a one-shot at job at runtime.
func (a *Adapter) AddAtJob(_ context.Context, job config.AtJob) error {
	return a.scheduler.AddAtJob(job)
}

// RemoveAtJob cancels and removes a pending at job. Returns true if removed.
func (a *Adapter) RemoveAtJob(_ context.Context, userID, id string) bool {
	return a.scheduler.RemoveAtJob(userID, id)
}

// Scheduler returns the underlying scheduler (for testing).
func (a *Adapter) Scheduler() *Scheduler {
	return a.scheduler
}

// Validate checks that the cron channel configuration is syntactically correct.
func Validate(cfg config.CronChannelConfig) error {
	seen := make(map[string]bool)
	for _, job := range cfg.Jobs {
		if job.ID == "" {
			return fmt.Errorf("cron: job has empty id")
		}
		if seen[job.ID] {
			return fmt.Errorf("cron: duplicate job id %q", job.ID)
		}
		seen[job.ID] = true
		if job.Schedule == "" {
			return fmt.Errorf("cron: job %q has empty schedule", job.ID)
		}
		if job.UserID == "" {
			return fmt.Errorf("cron: job %q has empty user_id", job.ID)
		}
		if job.Prompt == "" {
			return fmt.Errorf("cron: job %q has empty prompt", job.ID)
		}
	}
	return nil
}
