package gateway

import (
	"testing"
	"time"
)

func TestAgentHeartbeatStore_RecordAndGetAll(t *testing.T) {
	s := NewAgentHeartbeatStore()
	s.Record("agt-1", "user-1", "healthy")
	s.Record("agt-2", "user-2", "degraded")
	all := s.GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
}

func TestAgentHeartbeatStore_MarkStaleAsFailed(t *testing.T) {
	s := NewAgentHeartbeatStore()
	s.Record("agt-1", "user-1", "healthy")
	s.Record("agt-2", "user-2", "healthy")
	time.Sleep(5 * time.Millisecond) // make entries stale for 1ms threshold
	// Mark as failed if older than 1ms
	s.MarkStaleAsFailed(1 * time.Millisecond)
	all2 := s.GetAll()
	for _, e := range all2 {
		if e.Status != "failed" {
			t.Errorf("after stale cleanup expected failed, got %s", e.Status)
		}
	}
}

func TestAgentHeartbeatStore_MarkStaleAsFailed_OnlyStale(t *testing.T) {
	s := NewAgentHeartbeatStore()
	s.Record("agt-old", "user-1", "healthy")
	time.Sleep(5 * time.Millisecond) // make agt-old stale for a 2ms threshold
	s.Record("agt-new", "user-2", "healthy") // fresh
	s.MarkStaleAsFailed(2 * time.Millisecond) // only entries older than 2ms
	all := s.GetAll()
	var oldStatus, newStatus string
	for _, e := range all {
		if e.AgentID == "agt-old" {
			oldStatus = e.Status
		}
		if e.AgentID == "agt-new" {
			newStatus = e.Status
		}
	}
	if oldStatus != "failed" {
		t.Errorf("agt-old should be failed (stale), got %s", oldStatus)
	}
	if newStatus != "healthy" {
		t.Errorf("agt-new should still be healthy (fresh), got %s", newStatus)
	}
}
