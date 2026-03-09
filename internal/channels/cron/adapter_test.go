package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	channelpkg "github.com/jescarri/open-nipper/internal/channels"
	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
)

// Compile-time check: Adapter implements ChannelAdapter.
var _ channelpkg.ChannelAdapter = (*Adapter)(nil)

func validatorAlwaysOK(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func validatorAlwaysFail(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func validatorError(_ context.Context, _ string) (bool, error) {
	return false, fmt.Errorf("db error")
}

func newTestAdapter(t *testing.T, jobs []config.CronJob) *Adapter {
	t.Helper()
	return NewAdapter(AdapterDeps{
		Config: config.CronChannelConfig{
			Enabled: true,
			Jobs:    jobs,
		},
		Logger:    zap.NewNop(),
		Validator: validatorAlwaysOK,
	})
}

// --------------- Adapter interface tests ---------------

func TestAdapter_ChannelType(t *testing.T) {
	a := newTestAdapter(t, nil)
	if a.ChannelType() != models.ChannelTypeCron {
		t.Fatalf("expected cron, got %s", a.ChannelType())
	}
}

func TestAdapter_InterfaceCompliance(t *testing.T) {
	var _ channelpkg.ChannelAdapter = &Adapter{}
}

func TestAdapter_Start_NoJobs(t *testing.T) {
	a := newTestAdapter(t, nil)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	a.Stop(context.Background())
}

func TestAdapter_Start_WithValidJobs(t *testing.T) {
	jobs := []config.CronJob{
		{ID: "daily-report", Schedule: "0 0 9 * * *", UserID: "alice", Prompt: "Generate daily report", NotifyChannels: []string{"slack:C0789"}},
	}
	a := newTestAdapter(t, jobs)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer a.Stop(context.Background())

	if a.Scheduler().JobCount() != 1 {
		t.Fatalf("expected 1 job, got %d", a.Scheduler().JobCount())
	}
}

func TestAdapter_HealthCheck_NoJobs_OK(t *testing.T) {
	a := newTestAdapter(t, nil)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("expected nil error with no jobs configured, got: %v", err)
	}
}

func TestAdapter_HealthCheck_AllJobsFailed(t *testing.T) {
	jobs := []config.CronJob{
		{ID: "bad-schedule", Schedule: "not-a-cron", UserID: "alice", Prompt: "test"},
	}
	a := NewAdapter(AdapterDeps{
		Config:    config.CronChannelConfig{Enabled: true, Jobs: jobs},
		Logger:    zap.NewNop(),
		Validator: validatorAlwaysOK,
	})
	_ = a.Start(context.Background())
	defer a.Stop(context.Background())

	if err := a.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected health check error when all jobs fail validation")
	}
}

func TestAdapter_DeliverResponse_NoOp(t *testing.T) {
	a := newTestAdapter(t, nil)
	if err := a.DeliverResponse(context.Background(), &models.NipperResponse{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAdapter_DeliverEvent_NoOp(t *testing.T) {
	a := newTestAdapter(t, nil)
	if err := a.DeliverEvent(context.Background(), &models.NipperEvent{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --------------- NormalizeInbound tests ---------------

func TestAdapter_NormalizeInbound_Valid(t *testing.T) {
	jobs := []config.CronJob{
		{ID: "test-job", Schedule: "0 * * * * *", UserID: "alice", Prompt: "test", NotifyChannels: []string{"slack:C123"}},
	}
	a := newTestAdapter(t, jobs)

	raw, _ := json.Marshal(map[string]string{
		"jobId":  "test-job",
		"userId": "alice",
		"prompt": "run the test",
	})

	msg, err := a.NormalizeInbound(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if msg.ChannelType != models.ChannelTypeCron {
		t.Errorf("expected channelType cron, got %s", msg.ChannelType)
	}
	if msg.UserID != "alice" {
		t.Errorf("expected userId alice, got %s", msg.UserID)
	}
	if msg.Content.Text != "run the test" {
		t.Errorf("expected prompt text, got %q", msg.Content.Text)
	}
	if msg.DeliveryContext.ReplyMode != "broadcast" {
		t.Errorf("expected broadcast reply mode, got %s", msg.DeliveryContext.ReplyMode)
	}
	if len(msg.DeliveryContext.NotifyChannels) != 1 || msg.DeliveryContext.NotifyChannels[0] != "slack:C123" {
		t.Errorf("expected notifyChannels [slack:C123], got %v", msg.DeliveryContext.NotifyChannels)
	}
	meta, ok := msg.Meta.(models.CronMeta)
	if !ok {
		t.Fatal("expected CronMeta")
	}
	if meta.JobID != "test-job" {
		t.Errorf("expected jobId test-job, got %s", meta.JobID)
	}
}

func TestAdapter_NormalizeInbound_UnknownJob(t *testing.T) {
	a := newTestAdapter(t, nil) // no jobs configured

	raw, _ := json.Marshal(map[string]string{
		"jobId":  "unknown",
		"userId": "alice",
		"prompt": "hi",
	})

	msg, err := a.NormalizeInbound(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msg.DeliveryContext.NotifyChannels) != 0 {
		t.Errorf("expected empty notifyChannels for unknown job, got %v", msg.DeliveryContext.NotifyChannels)
	}
}

func TestAdapter_NormalizeInbound_MissingFields(t *testing.T) {
	a := newTestAdapter(t, nil)

	cases := []struct {
		name string
		data map[string]string
	}{
		{"missing jobId", map[string]string{"userId": "a", "prompt": "p"}},
		{"missing userId", map[string]string{"jobId": "j", "prompt": "p"}},
		{"missing prompt", map[string]string{"jobId": "j", "userId": "a"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.data)
			_, err := a.NormalizeInbound(context.Background(), raw)
			if err == nil {
				t.Fatal("expected error for missing fields")
			}
		})
	}
}

func TestAdapter_NormalizeInbound_InvalidJSON(t *testing.T) {
	a := newTestAdapter(t, nil)
	_, err := a.NormalizeInbound(context.Background(), []byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// --------------- Scheduler tests ---------------

func TestScheduler_LoadJobs_ValidJob(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "j1", Schedule: "0 * * * * *", UserID: "alice", Prompt: "hi"},
	}
	err := s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.JobCount() != 1 {
		t.Fatalf("expected 1 job, got %d", s.JobCount())
	}
}

func TestScheduler_LoadJobs_InvalidSchedule(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "bad", Schedule: "not-valid", UserID: "alice", Prompt: "hi"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)
	if s.JobCount() != 0 {
		t.Fatalf("expected 0 jobs for invalid schedule, got %d", s.JobCount())
	}
}

func TestScheduler_LoadJobs_MissingID(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "", Schedule: "0 * * * * *", UserID: "alice", Prompt: "hi"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)
	if s.JobCount() != 0 {
		t.Fatalf("expected 0 jobs for missing ID, got %d", s.JobCount())
	}
}

func TestScheduler_LoadJobs_MissingSchedule(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "j1", Schedule: "", UserID: "alice", Prompt: "hi"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)
	if s.JobCount() != 0 {
		t.Fatalf("expected 0 jobs for missing schedule, got %d", s.JobCount())
	}
}

func TestScheduler_LoadJobs_MissingUserID(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "j1", Schedule: "0 * * * * *", UserID: "", Prompt: "hi"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)
	if s.JobCount() != 0 {
		t.Fatalf("expected 0 jobs for missing userId, got %d", s.JobCount())
	}
}

func TestScheduler_LoadJobs_MissingPrompt(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "j1", Schedule: "0 * * * * *", UserID: "alice", Prompt: ""},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)
	if s.JobCount() != 0 {
		t.Fatalf("expected 0 jobs for missing prompt, got %d", s.JobCount())
	}
}

func TestScheduler_LoadJobs_UserValidationFails(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "j1", Schedule: "0 * * * * *", UserID: "ghost", Prompt: "hi"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysFail)
	if s.JobCount() != 0 {
		t.Fatalf("expected 0 jobs when user validation fails, got %d", s.JobCount())
	}
}

func TestScheduler_LoadJobs_UserValidationError(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "j1", Schedule: "0 * * * * *", UserID: "alice", Prompt: "hi"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorError)
	if s.JobCount() != 0 {
		t.Fatalf("expected 0 jobs when user validation errors, got %d", s.JobCount())
	}
}

func TestScheduler_LoadJobs_NilValidator(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "j1", Schedule: "0 * * * * *", UserID: "alice", Prompt: "hi"},
	}
	_ = s.LoadJobs(context.Background(), jobs, nil)
	if s.JobCount() != 1 {
		t.Fatalf("expected 1 job with nil validator, got %d", s.JobCount())
	}
}

func TestScheduler_LoadJobs_MultipleJobs_PartialFailure(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "good", Schedule: "0 * * * * *", UserID: "alice", Prompt: "hi"},
		{ID: "bad", Schedule: "invalid", UserID: "alice", Prompt: "hi"},
		{ID: "good2", Schedule: "0 */5 * * * *", UserID: "bob", Prompt: "hey"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)
	if s.JobCount() != 2 {
		t.Fatalf("expected 2 valid jobs, got %d", s.JobCount())
	}
}

func TestScheduler_Jobs_ReturnsCopy(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "j1", Schedule: "0 * * * * *", UserID: "alice", Prompt: "hi"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)

	copy1 := s.Jobs()
	copy2 := s.Jobs()
	copy1[0].ID = "modified"
	if copy2[0].ID == "modified" {
		t.Fatal("Jobs() should return a copy, not the original slice")
	}
}

func TestScheduler_FireJob_CallsHandler(t *testing.T) {
	s := NewScheduler(zap.NewNop())

	var received atomic.Int32
	var mu sync.Mutex
	var lastMsg *models.NipperMessage

	s.SetHandler(func(_ context.Context, msg *models.NipperMessage) error {
		received.Add(1)
		mu.Lock()
		lastMsg = msg
		mu.Unlock()
		return nil
	})

	job := config.CronJob{
		ID:             "fire-test",
		Schedule:       "* * * * * *", // every second
		UserID:         "alice",
		Prompt:         "hello cron",
		NotifyChannels: []string{"slack:C0789"},
	}

	s.fireJob(job)

	if received.Load() != 1 {
		t.Fatalf("expected handler called once, got %d", received.Load())
	}

	mu.Lock()
	defer mu.Unlock()

	if lastMsg.UserID != "alice" {
		t.Errorf("expected userId alice, got %s", lastMsg.UserID)
	}
	if lastMsg.ChannelType != models.ChannelTypeCron {
		t.Errorf("expected channelType cron, got %s", lastMsg.ChannelType)
	}
	if lastMsg.Content.Text != "hello cron" {
		t.Errorf("expected prompt 'hello cron', got %q", lastMsg.Content.Text)
	}
	if lastMsg.DeliveryContext.ReplyMode != "broadcast" {
		t.Errorf("expected broadcast replyMode, got %s", lastMsg.DeliveryContext.ReplyMode)
	}
	if len(lastMsg.DeliveryContext.NotifyChannels) != 1 {
		t.Errorf("expected 1 notifyChannel, got %d", len(lastMsg.DeliveryContext.NotifyChannels))
	}
	meta, ok := lastMsg.Meta.(models.CronMeta)
	if !ok {
		t.Fatal("expected CronMeta")
	}
	if meta.JobID != "fire-test" {
		t.Errorf("expected jobId fire-test, got %s", meta.JobID)
	}
}

func TestScheduler_FireJob_NoHandler(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	// Should not panic when no handler is registered.
	s.fireJob(config.CronJob{ID: "j", UserID: "u", Prompt: "p"})
}

func TestScheduler_FireJob_HandlerError(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	s.SetHandler(func(_ context.Context, _ *models.NipperMessage) error {
		return fmt.Errorf("handler boom")
	})

	// Should not panic on handler error.
	s.fireJob(config.CronJob{ID: "j", Schedule: "* * * * * *", UserID: "u", Prompt: "p"})
}

func TestScheduler_StartStop(t *testing.T) {
	s := NewScheduler(zap.NewNop())
	jobs := []config.CronJob{
		{ID: "j1", Schedule: "0 * * * * *", UserID: "alice", Prompt: "hi"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)
	s.Start()
	stopCtx := s.Stop()
	<-stopCtx.Done()
}

func TestScheduler_FireJob_MessageFields(t *testing.T) {
	s := NewScheduler(zap.NewNop())

	var got *models.NipperMessage
	s.SetHandler(func(_ context.Context, msg *models.NipperMessage) error {
		got = msg
		return nil
	})

	job := config.CronJob{
		ID:             "daily",
		UserID:         "bob",
		Prompt:         "check servers",
		NotifyChannels: []string{"slack:C1", "whatsapp:1555010001@s.whatsapp.net"},
	}
	s.fireJob(job)

	if got == nil {
		t.Fatal("handler was not called")
	}
	if got.MessageID == "" {
		t.Error("expected non-empty messageId")
	}
	if got.OriginMessageID == "" {
		t.Error("expected non-empty originMessageId")
	}
	if got.ChannelIdentity != "cron:daily" {
		t.Errorf("expected channelIdentity 'cron:daily', got %q", got.ChannelIdentity)
	}
	if got.DeliveryContext.ChannelID != "daily" {
		t.Errorf("expected channelId 'daily', got %q", got.DeliveryContext.ChannelID)
	}
	if got.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
	if len(got.DeliveryContext.NotifyChannels) != 2 {
		t.Errorf("expected 2 notifyChannels, got %d", len(got.DeliveryContext.NotifyChannels))
	}
}

func TestScheduler_ActualFiring(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive test in short mode")
	}

	s := NewScheduler(zap.NewNop())

	var received atomic.Int32
	s.SetHandler(func(_ context.Context, _ *models.NipperMessage) error {
		received.Add(1)
		return nil
	})

	jobs := []config.CronJob{
		{ID: "fast", Schedule: "* * * * * *", UserID: "alice", Prompt: "tick"},
	}
	_ = s.LoadJobs(context.Background(), jobs, validatorAlwaysOK)
	s.Start()

	time.Sleep(2500 * time.Millisecond)

	stopCtx := s.Stop()
	<-stopCtx.Done()

	count := received.Load()
	if count < 1 {
		t.Fatalf("expected at least 1 firing in 2.5s, got %d", count)
	}
}

// --------------- Validate tests ---------------

func TestValidate_ValidConfig(t *testing.T) {
	cfg := config.CronChannelConfig{
		Enabled: true,
		Jobs: []config.CronJob{
			{ID: "j1", Schedule: "0 * * * * *", UserID: "alice", Prompt: "hi"},
			{ID: "j2", Schedule: "0 */5 * * * *", UserID: "bob", Prompt: "hey"},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_EmptyJobs(t *testing.T) {
	cfg := config.CronChannelConfig{Enabled: true}
	if err := Validate(cfg); err != nil {
		t.Fatalf("empty jobs should be valid: %v", err)
	}
}

func TestValidate_DuplicateJobID(t *testing.T) {
	cfg := config.CronChannelConfig{
		Jobs: []config.CronJob{
			{ID: "dup", Schedule: "0 * * * * *", UserID: "alice", Prompt: "hi"},
			{ID: "dup", Schedule: "0 * * * * *", UserID: "bob", Prompt: "hey"},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for duplicate job ID")
	}
}

func TestValidate_MissingID(t *testing.T) {
	cfg := config.CronChannelConfig{
		Jobs: []config.CronJob{{Schedule: "0 * * * * *", UserID: "a", Prompt: "p"}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for missing ID")
	}
}

func TestValidate_MissingSchedule(t *testing.T) {
	cfg := config.CronChannelConfig{
		Jobs: []config.CronJob{{ID: "j", UserID: "a", Prompt: "p"}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for missing schedule")
	}
}

func TestValidate_MissingUserID(t *testing.T) {
	cfg := config.CronChannelConfig{
		Jobs: []config.CronJob{{ID: "j", Schedule: "0 * * * * *", Prompt: "p"}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for missing userId")
	}
}

func TestValidate_MissingPrompt(t *testing.T) {
	cfg := config.CronChannelConfig{
		Jobs: []config.CronJob{{ID: "j", Schedule: "0 * * * * *", UserID: "a"}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for missing prompt")
	}
}

// --------------- CronCapabilities test ---------------

func TestCronCapabilities(t *testing.T) {
	caps := models.CronCapabilities()
	if caps.SupportsMarkdown || caps.SupportsStreaming || caps.SupportsImages ||
		caps.SupportsDocuments || caps.SupportsAudio || caps.SupportsReactions ||
		caps.SupportsThreads || caps.SupportsMessageEdits {
		t.Error("cron channel should have all capabilities set to false")
	}
}
