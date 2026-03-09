package security

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
)

func defaultTestConfig() *config.Config {
	return &config.Config{
		Gateway: config.GatewayConfig{
			Bind: "127.0.0.1",
			Port: 18789,
			Admin: config.AdminConfig{
				Enabled: true,
				Bind:    "127.0.0.1",
				Port:    18790,
			},
		},
		Queue: config.QueueConfig{
			RabbitMQ: config.QueueRabbitMQConfig{
				URL: "amqp://localhost:5672",
			},
		},
		Datastore: config.DatastoreConfig{
			Path: "~/.open-nipper/nipper.db",
		},
	}
}

func TestCheckGatewayBind_Localhost(t *testing.T) {
	cfg := defaultTestConfig()
	findings := checkGatewayBind(cfg)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for 127.0.0.1, got %d", len(findings))
	}
}

func TestCheckGatewayBind_Exposed(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Gateway.Bind = "0.0.0.0"
	findings := checkGatewayBind(cfg)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding for 0.0.0.0, got %d", len(findings))
	}
	if findings[0].Severity != SeverityWarn {
		t.Fatalf("expected warn severity, got %s", findings[0].Severity)
	}
	if findings[0].CheckID != "gateway-bind" {
		t.Fatalf("expected check ID gateway-bind, got %s", findings[0].CheckID)
	}
}

func TestCheckAdminBind_Localhost(t *testing.T) {
	cfg := defaultTestConfig()
	findings := checkAdminBind(cfg)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for localhost admin, got %d", len(findings))
	}
}

func TestCheckAdminBind_Exposed(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Gateway.Admin.Bind = "0.0.0.0"
	findings := checkAdminBind(cfg)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding for exposed admin, got %d", len(findings))
	}
	if findings[0].Severity != SeverityWarn {
		t.Fatalf("expected warn severity, got %s", findings[0].Severity)
	}
}

func TestCheckAdminBind_Disabled(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Gateway.Admin.Enabled = false
	cfg.Gateway.Admin.Bind = "0.0.0.0"
	findings := checkAdminBind(cfg)
	if len(findings) != 0 {
		t.Fatalf("expected no findings when admin is disabled, got %d", len(findings))
	}
}

func TestCheckNoSecretsInConfig_Clean(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Channels.WhatsApp.Config.WuzapiToken = "${WUZAPI_TOKEN}"
	findings := checkNoSecretsInConfig(cfg)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for env var placeholders, got %d", len(findings))
	}
}

func TestCheckNoSecretsInConfig_LiteralSecret(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Channels.WhatsApp.Config.WuzapiToken = "ghp_FAKE_FOR_TEST_DETECTION_ONLY_do_not_use"
	findings := checkNoSecretsInConfig(cfg)
	if len(findings) == 0 {
		t.Fatal("expected finding for literal GitHub token")
	}
	if findings[0].Severity != SeverityCritical {
		t.Fatalf("expected critical severity, got %s", findings[0].Severity)
	}
}

func TestCheckNoSecretsInConfig_EmptyValues(t *testing.T) {
	cfg := defaultTestConfig()
	findings := checkNoSecretsInConfig(cfg)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for empty values, got %d", len(findings))
	}
}

func TestCheckNoSecretsInConfig_RawSecretFields_Placeholder(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.RawSecretFields = map[string]string{
		"channels.whatsapp.config.wuzapi_token": "${WUZAPI_TOKEN}",
		"queue.rabbitmq.password":               "${RABBITMQ_PASSWORD}",
	}
	findings := checkNoSecretsInConfig(cfg)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for ${VAR} placeholders in RawSecretFields, got %d", len(findings))
	}
}

func TestCheckNoSecretsInConfig_RawSecretFields_Literal(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.RawSecretFields = map[string]string{
		"channels.whatsapp.config.wuzapi_token": "ghp_FAKE_FOR_TEST_DETECTION_ONLY_do_not_use",
	}
	findings := checkNoSecretsInConfig(cfg)
	if len(findings) == 0 {
		t.Fatal("expected finding for literal secret in RawSecretFields")
	}
}

func TestCheckNoSecretsInConfig_SlackToken(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Channels.Slack.Config.BotToken = "xoxb-FAKE-FOR-TEST-DETECTION-ONLY-12345678901234-xxxxxxxxxxxxxxxxxxxxxxxx"
	findings := checkNoSecretsInConfig(cfg)
	if len(findings) == 0 {
		t.Fatal("expected finding for literal Slack token")
	}
}

func TestCheckNoSecretsInConfig_AWSKey(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Queue.RabbitMQ.Password = "AKIAIOSFODNN7EXAMPLE"
	findings := checkNoSecretsInConfig(cfg)
	if len(findings) == 0 {
		t.Fatal("expected finding for literal AWS key")
	}
}

func TestCheckNoSecretsInConfig_NipperToken(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Gateway.Admin.Auth.Token = "npr_FAKE_FOR_TEST_DETECTION_ONLY_do_not_use"
	findings := checkNoSecretsInConfig(cfg)
	if len(findings) == 0 {
		t.Fatal("expected finding for literal nipper token")
	}
}

func TestCheckRabbitMQTLS_Localhost(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Queue.RabbitMQ.URL = "amqp://localhost:5672"
	findings := checkRabbitMQTLS(cfg)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for localhost, got %d", len(findings))
	}
}

func TestCheckRabbitMQTLS_RemoteNoTLS(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Queue.RabbitMQ.URL = "amqp://rabbitmq.example.com:5672"
	findings := checkRabbitMQTLS(cfg)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding for remote non-TLS, got %d", len(findings))
	}
	if findings[0].Severity != SeverityWarn {
		t.Fatalf("expected warn severity, got %s", findings[0].Severity)
	}
}

func TestCheckRabbitMQTLS_RemoteWithTLS(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Queue.RabbitMQ.URL = "amqps://rabbitmq.example.com:5671"
	findings := checkRabbitMQTLS(cfg)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for amqps://, got %d", len(findings))
	}
}

func TestCheckRabbitMQTLS_EmptyURL(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Queue.RabbitMQ.URL = ""
	findings := checkRabbitMQTLS(cfg)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for empty URL, got %d", len(findings))
	}
}

func TestRunStartupAudit_CleanConfig(t *testing.T) {
	cfg := defaultTestConfig()
	logger := zap.NewNop()

	findings := RunStartupAudit(context.Background(), cfg, logger)

	for _, f := range findings {
		if f.Severity == SeverityCritical {
			t.Fatalf("unexpected critical finding in clean config: %s: %s", f.CheckID, f.Description)
		}
	}
}

func TestRunStartupAudit_MultipleIssues(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Gateway.Bind = "0.0.0.0"
	cfg.Gateway.Admin.Bind = "0.0.0.0"
	logger := zap.NewNop()

	findings := RunStartupAudit(context.Background(), cfg, logger)
	warnCount := countBySeverity(findings, SeverityWarn)
	if warnCount < 2 {
		t.Fatalf("expected at least 2 warn findings (gateway-bind, admin-bind), got %d", warnCount)
	}
}

func TestCountBySeverity(t *testing.T) {
	findings := []AuditFinding{
		{Severity: SeverityCritical},
		{Severity: SeverityWarn},
		{Severity: SeverityCritical},
		{Severity: SeverityInfo},
	}
	if got := countBySeverity(findings, SeverityCritical); got != 2 {
		t.Fatalf("expected 2 critical, got %d", got)
	}
	if got := countBySeverity(findings, SeverityWarn); got != 1 {
		t.Fatalf("expected 1 warn, got %d", got)
	}
	if got := countBySeverity(findings, SeverityInfo); got != 1 {
		t.Fatalf("expected 1 info, got %d", got)
	}
}
