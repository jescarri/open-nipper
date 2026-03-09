// Package security implements startup audits and runtime security checks
// for the Open-Nipper gateway.
package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

// Severity classifies the urgency of a security finding.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarn     Severity = "warn"
	SeverityInfo     Severity = "info"
)

// AuditFinding represents a single result from the startup security audit.
type AuditFinding struct {
	CheckID     string   `json:"checkId"`
	Severity    Severity `json:"severity"`
	Description string   `json:"description"`
	Remediation string   `json:"remediation,omitempty"`
}

// secretPatterns matches common secret/credential formats that should not
// appear as literal values in configuration files.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(password|passwd|secret|token|api_key|apikey)\s*[:=]\s*["\']?[A-Za-z0-9+/=_\-]{8,}`),
	regexp.MustCompile(`-----BEGIN\s+(RSA |EC |DSA )?PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\-._~+/]+=*`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)sk[-_][a-zA-Z0-9]{20,}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
	regexp.MustCompile(`gho_[A-Za-z0-9]{36}`),
	regexp.MustCompile(`xox[bpas]-[A-Za-z0-9\-]+`),
	regexp.MustCompile(`npr_[A-Za-z0-9]{20,}`),
}

// RunStartupAudit performs all startup security checks and returns findings.
// The process does NOT exit on findings — it logs them and continues.
func RunStartupAudit(ctx context.Context, cfg *config.Config, logger *zap.Logger) []AuditFinding {
	var findings []AuditFinding

	findings = append(findings, checkGatewayBind(cfg)...)
	findings = append(findings, checkAdminBind(cfg)...)
	findings = append(findings, checkFilesystemPermissions(cfg)...)
	findings = append(findings, checkNoSecretsInConfig(cfg)...)
	findings = append(findings, checkUserDirectoryIsolation(cfg)...)
	findings = append(findings, checkRabbitMQTLS(cfg)...)
	findings = append(findings, checkAuditLogWritable(cfg)...)

	for _, f := range findings {
		switch f.Severity {
		case SeverityCritical:
			logger.Error("security audit finding",
				zap.String("checkId", f.CheckID),
				zap.String("severity", string(f.Severity)),
				zap.String("description", f.Description),
				zap.String("remediation", f.Remediation),
			)
		case SeverityWarn:
			logger.Warn("security audit finding",
				zap.String("checkId", f.CheckID),
				zap.String("severity", string(f.Severity)),
				zap.String("description", f.Description),
				zap.String("remediation", f.Remediation),
			)
		case SeverityInfo:
			logger.Info("security audit finding",
				zap.String("checkId", f.CheckID),
				zap.String("severity", string(f.Severity)),
				zap.String("description", f.Description),
			)
		}
	}

	logger.Info("startup security audit complete",
		zap.Int("findings", len(findings)),
		zap.Int("critical", countBySeverity(findings, SeverityCritical)),
		zap.Int("warnings", countBySeverity(findings, SeverityWarn)),
	)

	return findings
}

func checkGatewayBind(cfg *config.Config) []AuditFinding {
	if cfg.Gateway.Bind != "127.0.0.1" && cfg.Gateway.Bind != "localhost" {
		return []AuditFinding{{
			CheckID:     "gateway-bind",
			Severity:    SeverityWarn,
			Description: fmt.Sprintf("gateway.bind is %q — binding to all interfaces exposes the gateway to the network; use a reverse proxy and firewall for production", cfg.Gateway.Bind),
			Remediation: "Consider 127.0.0.1 if behind a reverse proxy, or ensure proper firewall rules",
		}}
	}
	return nil
}

func checkAdminBind(cfg *config.Config) []AuditFinding {
	if !cfg.Gateway.Admin.Enabled {
		return nil
	}
	if cfg.Gateway.Admin.Bind != "127.0.0.1" && cfg.Gateway.Admin.Bind != "localhost" {
		return []AuditFinding{{
			CheckID:     "admin-bind",
			Severity:    SeverityWarn,
			Description: fmt.Sprintf("gateway.admin.bind is %q — admin API exposed to network; ensure auth is enabled and access is restricted", cfg.Gateway.Admin.Bind),
			Remediation: "Set gateway.admin.bind to 127.0.0.1 for local-only access, or enable gateway.admin.auth.token",
		}}
	}
	return nil
}

func checkFilesystemPermissions(cfg *config.Config) []AuditFinding {
	homeDir := nipperHomeDir()
	if homeDir == "" {
		return nil
	}

	info, err := os.Stat(homeDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return []AuditFinding{{
			CheckID:     "filesystem-permissions",
			Severity:    SeverityWarn,
			Description: fmt.Sprintf("cannot stat %s: %v", homeDir, err),
		}}
	}

	mode := info.Mode().Perm()
	if mode&0077 != 0 {
		return []AuditFinding{{
			CheckID:     "filesystem-permissions",
			Severity:    SeverityWarn,
			Description: fmt.Sprintf("%s has mode %04o — should be 0700 (group/other have access)", homeDir, mode),
			Remediation: fmt.Sprintf("Run: chmod 700 %s", homeDir),
		}}
	}
	return nil
}

func checkNoSecretsInConfig(cfg *config.Config) []AuditFinding {
	// Use raw (pre-resolution) values so we can distinguish ${ENV_VAR} placeholders
	// from literal secrets. RawSecretFields is populated by config.Load() before env expansion.
	// Fallback to resolved values when nil (e.g. in tests that construct Config directly).
	configValues := cfg.RawSecretFields
	if configValues == nil {
		configValues = fallbackSecretCandidates(cfg)
	}

	var findings []AuditFinding
	for field, value := range configValues {
		if value == "" {
			continue
		}
		// Values that are ${...} placeholders (unresolved) are OK — user is using env vars
		if strings.HasPrefix(value, "${") && strings.Contains(value, "}") {
			continue
		}
		for _, pat := range secretPatterns {
			fakeKV := "token: " + value
			if pat.MatchString(fakeKV) || pat.MatchString(value) {
				findings = append(findings, AuditFinding{
					CheckID:     "no-secrets-in-config",
					Severity:    SeverityCritical,
					Description: fmt.Sprintf("config field %q appears to contain a literal secret/token", field),
					Remediation: "Use ${ENV_VAR} placeholder instead of literal values for secrets",
				})
				break
			}
		}
	}
	return findings
}

func checkUserDirectoryIsolation(cfg *config.Config) []AuditFinding {
	usersDir := filepath.Join(nipperHomeDir(), "users")
	entries, err := os.ReadDir(usersDir)
	if err != nil {
		return nil
	}

	var findings []AuditFinding
	for _, entry := range entries {
		fullPath := filepath.Join(usersDir, entry.Name())
		info, err := os.Lstat(fullPath)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			findings = append(findings, AuditFinding{
				CheckID:     "user-directory-isolation",
				Severity:    SeverityCritical,
				Description: fmt.Sprintf("symlink found in user directory: %s", fullPath),
				Remediation: "Remove symlinks from ~/.open-nipper/users/ — they can escape directory isolation",
			})
		}
	}
	return findings
}

func checkRabbitMQTLS(cfg *config.Config) []AuditFinding {
	url := cfg.Queue.RabbitMQ.URL
	if url == "" {
		return nil
	}
	isLocalhost := strings.Contains(url, "localhost") || strings.Contains(url, "127.0.0.1")
	if !isLocalhost && !strings.HasPrefix(url, "amqps://") {
		return []AuditFinding{{
			CheckID:     "rabbitmq-tls",
			Severity:    SeverityWarn,
			Description: "RabbitMQ URL is non-localhost but does not use amqps:// (TLS)",
			Remediation: "Use amqps:// for remote RabbitMQ connections",
		}}
	}
	return nil
}

func checkAuditLogWritable(cfg *config.Config) []AuditFinding {
	logDir := filepath.Join(nipperHomeDir(), "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return []AuditFinding{{
			CheckID:     "audit-log-writable",
			Severity:    SeverityWarn,
			Description: fmt.Sprintf("audit log directory %s is not writable: %v", logDir, err),
			Remediation: "Ensure the logs directory exists and is writable",
		}}
	}
	testFile := filepath.Join(logDir, ".audit-check")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		return []AuditFinding{{
			CheckID:     "audit-log-writable",
			Severity:    SeverityWarn,
			Description: fmt.Sprintf("cannot write to audit log directory %s: %v", logDir, err),
			Remediation: "Ensure the logs directory is writable by the gateway process",
		}}
	}
	os.Remove(testFile)
	return nil
}

func nipperHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".open-nipper")
}

// fallbackSecretCandidates returns resolved config values for secret fields.
// Used when RawSecretFields is nil (e.g. tests). May produce false positives
// for values that came from ${ENV_VAR} placeholders.
func fallbackSecretCandidates(cfg *config.Config) map[string]string {
	return map[string]string{
		"channels.whatsapp.config.wuzapi_token":     cfg.Channels.WhatsApp.Config.WuzapiToken,
		"channels.whatsapp.config.wuzapi_hmac_key":  cfg.Channels.WhatsApp.Config.WuzapiHMACKey,
		"channels.slack.config.bot_token":            cfg.Channels.Slack.Config.BotToken,
		"channels.slack.config.signing_secret":      cfg.Channels.Slack.Config.SigningSecret,
		"channels.slack.config.app_token":           cfg.Channels.Slack.Config.AppToken,
		"channels.mqtt.config.password":             cfg.Channels.MQTT.Config.Password,
		"channels.rabbitmq_channel.config.password": cfg.Channels.RabbitMQ.Config.Password,
		"queue.rabbitmq.password":                   cfg.Queue.RabbitMQ.Password,
		"agents.rabbitmq_management.password":      cfg.Agents.RabbitMQManagement.Password,
		"gateway.admin.auth.token":                  cfg.Gateway.Admin.Auth.Token,
	}
}

func countBySeverity(findings []AuditFinding, sev Severity) int {
	count := 0
	for _, f := range findings {
		if f.Severity == sev {
			count++
		}
	}
	return count
}
