package rabbitmq

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

func testConfig() config.RabbitMQChanConfig {
	return config.RabbitMQChanConfig{
		URL:              "amqp://localhost:5672",
		ExchangeInbound:  "nipper.inbound",
		ExchangeOutbound: "nipper.outbound",
		ExchangeDLX:      "nipper.dlx",
		Prefetch:         1,
		Heartbeat:        60,
		Reconnect: config.ReconnectConfig{
			Enabled:        true,
			InitialDelayMS: 100,
			MaxDelayMS:     1000,
		},
	}
}

func TestNewAdapter(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	if a == nil {
		t.Fatal("expected non-nil adapter")
	}
	if a.ChannelType() != models.ChannelTypeRabbitMQ {
		t.Fatalf("expected channel type rabbitmq, got %s", a.ChannelType())
	}
}

func TestAdapter_ChannelType(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	if a.ChannelType() != models.ChannelTypeRabbitMQ {
		t.Fatalf("expected rabbitmq, got %s", a.ChannelType())
	}
}

func TestAdapter_HealthCheck_NotConnected(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	if err := a.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected health check error when not connected")
	}
}

func TestAdapter_IsConnected_Initial(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	if a.IsConnected() {
		t.Fatal("expected not connected initially")
	}
}

func TestAdapter_ConsumerCount_Initial(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	if a.ConsumerCount() != 0 {
		t.Fatal("expected 0 consumers initially")
	}
}

func TestAdapter_DeliverEvent_Noop(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	err := a.DeliverEvent(context.Background(), &models.NipperEvent{})
	if err != nil {
		t.Fatalf("DeliverEvent should be no-op, got error: %v", err)
	}
}

func TestAdapter_DeliverResponse_NoDeliveryClient(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	resp := &models.NipperResponse{
		ResponseID: "resp-01",
		UserID:     "user-01",
		Text:       "hello",
	}
	err := a.DeliverResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error when delivery client not initialized")
	}
}

func TestAdapter_DeliverResponse_NilResponse(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	err := a.DeliverResponse(context.Background(), nil)
	if err != nil {
		t.Fatalf("nil response should not error: %v", err)
	}
}

func TestAdapter_SetHandler(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	a.SetHandler(func(ctx context.Context, msg *models.NipperMessage) error {
		return nil
	})

	if a.handler == nil {
		t.Fatal("handler should be set")
	}
}

func TestAdapter_NormalizeInbound_WithWrapper(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	raw := `{"_body": {"text": "hello from service"}, "_meta": {"exchange": "nipper.inbound", "routingKey": "nipper.user-01.inbox", "queue": "nipper-user-01-inbox"}}`
	msg, err := a.NormalizeInbound(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.Content.Text != "hello from service" {
		t.Fatalf("unexpected text: %s", msg.Content.Text)
	}
	if msg.UserID != "user-01" {
		t.Fatalf("unexpected userId: %s", msg.UserID)
	}
}

func TestAdapter_NormalizeInbound_MissingBody(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	raw := `{"_meta": {"routingKey": "nipper.user-01.inbox"}}`
	_, err := a.NormalizeInbound(context.Background(), []byte(raw))
	if err == nil {
		t.Fatal("expected error for missing _body")
	}
}

func TestAdapter_NormalizeInbound_InvalidWrapper(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testConfig(),
		Logger: zap.NewNop(),
	})

	_, err := a.NormalizeInbound(context.Background(), []byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.RabbitMQChanConfig
		wantErr bool
	}{
		{
			"valid",
			config.RabbitMQChanConfig{
				URL:              "amqp://localhost:5672",
				ExchangeInbound:  "nipper.inbound",
				ExchangeOutbound: "nipper.outbound",
			},
			false,
		},
		{
			"missing url",
			config.RabbitMQChanConfig{
				ExchangeInbound:  "nipper.inbound",
				ExchangeOutbound: "nipper.outbound",
			},
			true,
		},
		{
			"missing exchange inbound",
			config.RabbitMQChanConfig{
				URL:              "amqp://localhost:5672",
				ExchangeOutbound: "nipper.outbound",
			},
			true,
		},
		{
			"missing exchange outbound",
			config.RabbitMQChanConfig{
				URL:             "amqp://localhost:5672",
				ExchangeInbound: "nipper.inbound",
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.RabbitMQChanConfig
		want string
	}{
		{
			"basic",
			config.RabbitMQChanConfig{URL: "amqp://localhost:5672"},
			"amqp://localhost:5672",
		},
		{
			"with credentials",
			config.RabbitMQChanConfig{URL: "amqp://localhost:5672", Username: "user", Password: "pass"},
			"amqp://user:pass@localhost:5672",
		},
		{
			"with vhost",
			config.RabbitMQChanConfig{URL: "amqp://localhost:5672", Username: "user", Password: "pass", VHost: "/nipper"},
			"amqp://user:pass@localhost:5672/%2Fnipper",
		},
		{
			"empty url defaults",
			config.RabbitMQChanConfig{},
			"amqp://localhost:5672",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildURL(tt.cfg)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
