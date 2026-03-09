package gateway_test

import (
	"testing"
	"time"

	"github.com/open-nipper/open-nipper/internal/gateway"
	"github.com/open-nipper/open-nipper/internal/models"
)

func TestRegistry_RegisterAndLookup(t *testing.T) {
	reg := gateway.NewRegistry()
	dc := models.DeliveryContext{
		ChannelType: models.ChannelTypeSlack,
		ChannelID:   "C0123",
		ReplyMode:   "thread",
	}

	reg.Register("user:alice:channel:slack:session:s1", dc, nil, nil)

	got, _, _, ok := reg.Lookup("user:alice:channel:slack:session:s1")
	if !ok {
		t.Fatal("expected entry to be found")
	}
	if got.ChannelID != "C0123" {
		t.Errorf("expected ChannelID=C0123, got %q", got.ChannelID)
	}
	if got.ReplyMode != "thread" {
		t.Errorf("expected ReplyMode=thread, got %q", got.ReplyMode)
	}
}

func TestRegistry_RegisterAndLookup_WithMeta(t *testing.T) {
	reg := gateway.NewRegistry()
	dc := models.DeliveryContext{ChannelType: models.ChannelTypeWhatsApp}
	meta := models.WhatsAppMeta{ChatJID: "1555010001@s.whatsapp.net", SenderJID: "1555010001@s.whatsapp.net"}

	parts := []models.ContentPart{{Type: "image", URL: "https://s3.example.com/a.jpg", MimeType: "image/jpeg"}}
	reg.Register("wa-session", dc, meta, parts)

	_, gotMeta, gotParts, ok := reg.Lookup("wa-session")
	if !ok {
		t.Fatal("expected entry to be found")
	}
	waMeta, ok := gotMeta.(models.WhatsAppMeta)
	if !ok {
		t.Fatal("expected WhatsAppMeta")
	}
	if waMeta.ChatJID != "1555010001@s.whatsapp.net" {
		t.Errorf("unexpected ChatJID: %q", waMeta.ChatJID)
	}
	if len(gotParts) != 1 || gotParts[0].URL != "https://s3.example.com/a.jpg" {
		t.Fatalf("unexpected inbound parts: %+v", gotParts)
	}
}

func TestRegistry_LookupMissing(t *testing.T) {
	reg := gateway.NewRegistry()
	_, _, _, ok := reg.Lookup("nonexistent")
	if ok {
		t.Error("expected Lookup to return false for missing key")
	}
}

func TestRegistry_ConsumeRemovesEntry(t *testing.T) {
	reg := gateway.NewRegistry()
	dc := models.DeliveryContext{ChannelType: models.ChannelTypeWhatsApp}
	reg.Register("key1", dc, nil, nil)

	_, _, _, ok := reg.Consume("key1")
	if !ok {
		t.Fatal("expected Consume to succeed")
	}

	_, _, _, ok = reg.Lookup("key1")
	if ok {
		t.Error("expected entry to be gone after Consume")
	}
}

func TestRegistry_ConsumeNonexistentReturnsFalse(t *testing.T) {
	reg := gateway.NewRegistry()
	_, _, _, ok := reg.Consume("doesnotexist")
	if ok {
		t.Error("expected Consume to return false for missing key")
	}
}

func TestRegistry_Len(t *testing.T) {
	reg := gateway.NewRegistry()
	if reg.Len() != 0 {
		t.Errorf("expected Len=0, got %d", reg.Len())
	}

	dc := models.DeliveryContext{ChannelType: models.ChannelTypeSlack}
	reg.Register("k1", dc, nil, nil)
	reg.Register("k2", dc, nil, nil)

	if reg.Len() != 2 {
		t.Errorf("expected Len=2, got %d", reg.Len())
	}
}

func TestRegistry_ConsumeReturnsFIFO(t *testing.T) {
	reg := gateway.NewRegistry()
	dc1 := models.DeliveryContext{ChannelType: models.ChannelTypeSlack, ReplyMode: "thread"}
	dc2 := models.DeliveryContext{ChannelType: models.ChannelTypeSlack, ReplyMode: "dm"}

	reg.Register("k1", dc1, nil, nil)
	reg.Register("k1", dc2, nil, nil)

	got1, _, _, ok := reg.Consume("k1")
	if !ok {
		t.Fatal("expected first Consume to succeed")
	}
	if got1.ReplyMode != "thread" {
		t.Errorf("expected first Consume to return thread, got %q", got1.ReplyMode)
	}

	got2, _, _, ok := reg.Consume("k1")
	if !ok {
		t.Fatal("expected second Consume to succeed")
	}
	if got2.ReplyMode != "dm" {
		t.Errorf("expected second Consume to return dm, got %q", got2.ReplyMode)
	}

	_, _, _, ok = reg.Consume("k1")
	if ok {
		t.Error("expected third Consume to return false (queue empty)")
	}
}

func TestRegistry_EvictOlderThan(t *testing.T) {
	reg := gateway.NewRegistry()
	dc := models.DeliveryContext{ChannelType: models.ChannelTypeSlack}

	reg.Register("old", dc, nil, nil)
	// Simulate an old entry by waiting briefly and registering a new one.
	time.Sleep(10 * time.Millisecond)
	cutoff := time.Now()
	time.Sleep(10 * time.Millisecond)
	reg.Register("new", dc, nil, nil)

	evicted := reg.EvictOlderThan(cutoff)
	if evicted != 1 {
		t.Errorf("expected 1 evicted, got %d", evicted)
	}

	_, _, _, ok := reg.Lookup("old")
	if ok {
		t.Error("expected old entry to be evicted")
	}
	_, _, _, ok = reg.Lookup("new")
	if !ok {
		t.Error("expected new entry to remain")
	}
}
