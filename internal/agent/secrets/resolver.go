// Package secrets provides environment-variable-based secret resolution for the agent.
package secrets

import (
	"os"
	"regexp"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// Resolver resolves secrets from environment variables.
type Resolver struct {
	logger *zap.Logger
}

// NewResolver creates a new secret resolver.
func NewResolver(logger *zap.Logger) *Resolver {
	return &Resolver{logger: logger}
}

// Resolve reads SecretsConfig.EnvMap and returns a map of name → resolved value.
// It logs which secret names were resolved (never the values).
func (r *Resolver) Resolve(cfg config.SecretsConfig) map[string]string {
	resolved := make(map[string]string, len(cfg.EnvMap))
	for name, envKey := range cfg.EnvMap {
		val := os.Getenv(envKey)
		if val != "" {
			resolved[name] = val
			r.logger.Debug("secret resolved", zap.String("name", name))
		} else {
			r.logger.Warn("secret not found in environment", zap.String("name", name), zap.String("envKey", envKey))
		}
	}
	return resolved
}

// ExpandString replaces ${VAR} placeholders in s with environment variable values.
func ExpandString(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1]
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		return match
	})
}
