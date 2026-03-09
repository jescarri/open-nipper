package skills

import (
	"os"

	"go.uber.org/zap"
)

// SecretProvider resolves secret references to plaintext values.
// Implementations: EnvVarProvider (Stage 3), VaultProvider (future), OPProvider (future).
type SecretProvider interface {
	// Name returns the provider identifier (e.g. "env", "vault", "op").
	Name() string

	// Resolve takes a list of secret references and returns a map of env_var → value.
	// References are provider-specific (env var names, vault paths, op:// URIs).
	Resolve(refs []SkillSecretRef) (map[string]string, error)
}

// EnvVarProvider resolves secrets from the host process environment.
type EnvVarProvider struct {
	logger *zap.Logger
}

// NewEnvVarProvider creates an env-var secret provider.
func NewEnvVarProvider(logger *zap.Logger) *EnvVarProvider {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &EnvVarProvider{logger: logger}
}

// Name returns the provider identifier.
func (p *EnvVarProvider) Name() string { return "env" }

// Resolve reads host env vars and returns a map keyed by container env var name.
// Missing env vars are logged as warnings and skipped (non-fatal).
func (p *EnvVarProvider) Resolve(refs []SkillSecretRef) (map[string]string, error) {
	result := make(map[string]string, len(refs))
	for _, ref := range refs {
		if ref.Provider != "" && ref.Provider != "env" {
			continue // skip non-env refs
		}
		val := os.Getenv(ref.Ref) // ref.Ref is the host env var name
		if val == "" {
			p.logger.Warn("skill secret not found in environment",
				zap.String("name", ref.Name),
				zap.String("envVar", ref.Ref),
			)
			continue
		}
		result[ref.EnvVar] = val // ref.EnvVar is the container env var name
		p.logger.Debug("skill secret resolved",
			zap.String("name", ref.Name),
			zap.String("containerEnvVar", ref.EnvVar),
		)
	}
	return result, nil
}

// ProviderRegistry routes secret references to the correct provider and merges results.
type ProviderRegistry struct {
	providers map[string]SecretProvider
}

// NewProviderRegistry creates an empty registry. Register providers before use.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]SecretProvider),
	}
}

// Register adds a secret provider. Same name overwrites.
func (r *ProviderRegistry) Register(p SecretProvider) {
	if p != nil {
		r.providers[p.Name()] = p
	}
}

// Resolve resolves all refs via their providers and returns a single map of env_var → value.
// Refs with unknown or empty provider are treated as "env". Missing values are skipped.
func (r *ProviderRegistry) Resolve(refs []SkillSecretRef) (map[string]string, error) {
	merged := make(map[string]string)
	for _, ref := range refs {
		provider := ref.Provider
		if provider == "" {
			provider = "env"
		}
		p, ok := r.providers[provider]
		if !ok {
			continue // unknown provider, skip
		}
		one, err := p.Resolve([]SkillSecretRef{ref})
		if err != nil {
			continue
		}
		for k, v := range one {
			merged[k] = v
		}
	}
	return merged, nil
}
