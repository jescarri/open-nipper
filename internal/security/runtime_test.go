package security

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

type mockHealthChecker struct {
	err error
}

func (m *mockHealthChecker) Ping(_ context.Context) error {
	return m.err
}

func TestNewRuntimeMonitor(t *testing.T) {
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config: defaultTestConfig(),
		Logger: zap.NewNop(),
	})
	if mon == nil {
		t.Fatal("expected non-nil monitor")
	}
}

func TestRuntimeMonitor_InitialState(t *testing.T) {
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config: defaultTestConfig(),
		Logger: zap.NewNop(),
	})

	if !mon.IsDatastoreHealthy() {
		t.Fatal("expected datastore healthy initially")
	}
	if !mon.IsQueueHealthy() {
		t.Fatal("expected queue healthy initially")
	}
	if len(mon.Findings()) != 0 {
		t.Fatal("expected no findings initially")
	}
}

func TestRuntimeMonitor_RecordFailedRegistration(t *testing.T) {
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config: defaultTestConfig(),
		Logger: zap.NewNop(),
	})

	mon.RecordFailedRegistration()
	mon.RecordFailedRegistration()
	mon.RecordFailedRegistration()

	count := atomic.LoadInt64(&mon.failedRegistrations)
	if count != 3 {
		t.Fatalf("expected 3 failed registrations, got %d", count)
	}
}

func TestRuntimeMonitor_StartStop(t *testing.T) {
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config: defaultTestConfig(),
		Logger: zap.NewNop(),
	})

	ctx := context.Background()
	mon.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	mon.Stop()
}

func TestRuntimeMonitor_Connectivity_DatastoreDown(t *testing.T) {
	ds := &mockHealthChecker{err: fmt.Errorf("database locked")}
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config:    defaultTestConfig(),
		Logger:    zap.NewNop(),
		Datastore: ds,
	})

	mon.checkConnectivity(context.Background())

	if mon.IsDatastoreHealthy() {
		t.Fatal("expected datastore unhealthy after failed ping")
	}
}

func TestRuntimeMonitor_Connectivity_DatastoreUp(t *testing.T) {
	ds := &mockHealthChecker{err: nil}
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config:    defaultTestConfig(),
		Logger:    zap.NewNop(),
		Datastore: ds,
	})

	mon.checkConnectivity(context.Background())

	if !mon.IsDatastoreHealthy() {
		t.Fatal("expected datastore healthy after successful ping")
	}
}

func TestRuntimeMonitor_Connectivity_QueueDown(t *testing.T) {
	qc := &mockHealthChecker{err: fmt.Errorf("connection refused")}
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config:       defaultTestConfig(),
		Logger:       zap.NewNop(),
		QueueChecker: qc,
	})

	mon.checkConnectivity(context.Background())

	if mon.IsQueueHealthy() {
		t.Fatal("expected queue unhealthy after failed ping")
	}
}

func TestRuntimeMonitor_Connectivity_BothUp(t *testing.T) {
	ds := &mockHealthChecker{err: nil}
	qc := &mockHealthChecker{err: nil}
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config:       defaultTestConfig(),
		Logger:       zap.NewNop(),
		Datastore:    ds,
		QueueChecker: qc,
	})

	mon.checkConnectivity(context.Background())

	if !mon.IsDatastoreHealthy() {
		t.Fatal("expected datastore healthy")
	}
	if !mon.IsQueueHealthy() {
		t.Fatal("expected queue healthy")
	}
}

func TestRuntimeMonitor_Findings_Capped(t *testing.T) {
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config: defaultTestConfig(),
		Logger: zap.NewNop(),
	})

	for i := 0; i < 150; i++ {
		mon.addFinding(AuditFinding{
			CheckID:     fmt.Sprintf("test-%d", i),
			Severity:    SeverityInfo,
			Description: "test finding",
		})
	}

	findings := mon.Findings()
	if len(findings) != 100 {
		t.Fatalf("expected findings capped at 100, got %d", len(findings))
	}
}

func TestRuntimeMonitor_Findings_Copy(t *testing.T) {
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config: &config.Config{},
		Logger: zap.NewNop(),
	})

	mon.addFinding(AuditFinding{
		CheckID:  "test",
		Severity: SeverityInfo,
	})

	f1 := mon.Findings()
	f2 := mon.Findings()

	if len(f1) != len(f2) {
		t.Fatal("findings should be consistent")
	}

	f1[0].CheckID = "modified"
	f3 := mon.Findings()
	if f3[0].CheckID == "modified" {
		t.Fatal("Findings should return a copy, not a reference")
	}
}

func TestRuntimeMonitor_Connectivity_NilCheckers(t *testing.T) {
	mon := NewRuntimeMonitor(RuntimeMonitorDeps{
		Config: defaultTestConfig(),
		Logger: zap.NewNop(),
	})

	mon.checkConnectivity(context.Background())

	if !mon.IsDatastoreHealthy() {
		t.Fatal("nil datastore checker should remain healthy")
	}
	if !mon.IsQueueHealthy() {
		t.Fatal("nil queue checker should remain healthy")
	}
}
