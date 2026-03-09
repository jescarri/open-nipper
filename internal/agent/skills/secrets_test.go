package skills

import (
	"os"
	"testing"

	"go.uber.org/zap"
)

func TestEnvVarProvider_Name(t *testing.T) {
	p := NewEnvVarProvider(zap.NewNop())
	if got := p.Name(); got != "env" {
		t.Errorf("Name() = %q, want env", got)
	}
}

func TestEnvVarProvider_Resolve_AllPresent(t *testing.T) {
	os.Setenv("HOST_A", "val_a")
	os.Setenv("HOST_B", "val_b")
	defer func() {
		os.Unsetenv("HOST_A")
		os.Unsetenv("HOST_B")
	}()

	p := NewEnvVarProvider(zap.NewNop())
	refs := []SkillSecretRef{
		{Name: "a", EnvVar: "CONTAINER_A", Provider: "env", Ref: "HOST_A"},
		{Name: "b", EnvVar: "CONTAINER_B", Provider: "env", Ref: "HOST_B"},
	}
	got, err := p.Resolve(refs)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len(got) = %d, want 2", len(got))
	}
	if got["CONTAINER_A"] != "val_a" || got["CONTAINER_B"] != "val_b" {
		t.Errorf("got %v", got)
	}
}

func TestEnvVarProvider_Resolve_Partial(t *testing.T) {
	os.Setenv("HOST_PRESENT", "present_val")
	defer os.Unsetenv("HOST_PRESENT")

	p := NewEnvVarProvider(zap.NewNop())
	refs := []SkillSecretRef{
		{Name: "present", EnvVar: "CONTAINER_PRESENT", Provider: "env", Ref: "HOST_PRESENT"},
		{Name: "missing", EnvVar: "CONTAINER_MISSING", Provider: "env", Ref: "HOST_MISSING_DOES_NOT_EXIST"},
	}
	got, err := p.Resolve(refs)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len(got) = %d, want 1 (missing skipped)", len(got))
	}
	if got["CONTAINER_PRESENT"] != "present_val" {
		t.Errorf("got %v", got)
	}
}

func TestEnvVarProvider_Resolve_SkipsNonEnv(t *testing.T) {
	os.Setenv("HOST_X", "x")
	defer os.Unsetenv("HOST_X")

	p := NewEnvVarProvider(zap.NewNop())
	refs := []SkillSecretRef{
		{Name: "env_ref", EnvVar: "CONTAINER_X", Provider: "env", Ref: "HOST_X"},
		{Name: "vault_ref", EnvVar: "VAULT_SECRET", Provider: "vault", Ref: "secret/path"},
	}
	got, err := p.Resolve(refs)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got["CONTAINER_X"] != "x" {
		t.Errorf("expected only env ref resolved, got %v", got)
	}
}

func TestProviderRegistry_Register_Resolve(t *testing.T) {
	os.Setenv("REG_HOST_K", "v_k")
	defer os.Unsetenv("REG_HOST_K")

	reg := NewProviderRegistry()
	reg.Register(NewEnvVarProvider(zap.NewNop()))

	refs := []SkillSecretRef{
		{Name: "k", EnvVar: "K", Provider: "env", Ref: "REG_HOST_K"},
		{Name: "default", EnvVar: "DEFAULT", Ref: "REG_HOST_K"}, // empty provider => env
	}
	got, err := reg.Resolve(refs)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got["K"] != "v_k" || got["DEFAULT"] != "v_k" {
		t.Errorf("got %v", got)
	}
}

func TestProviderRegistry_Resolve_UnknownProviderSkipped(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(NewEnvVarProvider(zap.NewNop()))

	refs := []SkillSecretRef{
		{Name: "op_ref", EnvVar: "OP_VAL", Provider: "op", Ref: "op://vault/item"},
	}
	got, err := reg.Resolve(refs)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unknown provider should be skipped, got %v", got)
	}
}

func TestProviderRegistry_Resolve_EmptyRefs(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(NewEnvVarProvider(zap.NewNop()))
	got, err := reg.Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
