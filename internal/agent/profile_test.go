package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProfile_Empty(t *testing.T) {
	dir := t.TempDir()
	p, err := LoadProfile(dir, "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.IsEmpty() {
		t.Error("expected empty profile")
	}
}

func TestSaveAndLoadProfile(t *testing.T) {
	dir := t.TempDir()
	userID := "user1"

	p := &UserProfile{
		UserName:    "Alice",
		AgentName:   "Nipper",
		Personality: "friendly",
		Location:    "London, UK",
		SkillLevel:  "intermediate",
		Language:    "English",
	}

	if err := SaveProfile(dir, userID, p); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadProfile(dir, userID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.UserName != "Alice" {
		t.Errorf("UserName = %q, want Alice", loaded.UserName)
	}
	if loaded.AgentName != "Nipper" {
		t.Errorf("AgentName = %q, want Nipper", loaded.AgentName)
	}
	if loaded.Personality != "friendly" {
		t.Errorf("Personality = %q, want friendly", loaded.Personality)
	}
	if loaded.Location != "London, UK" {
		t.Errorf("Location = %q, want London, UK", loaded.Location)
	}
	if loaded.SkillLevel != "intermediate" {
		t.Errorf("SkillLevel = %q, want intermediate", loaded.SkillLevel)
	}
	if loaded.Language != "English" {
		t.Errorf("Language = %q, want English", loaded.Language)
	}
	if loaded.ConfiguredAt.IsZero() {
		t.Error("ConfiguredAt should be set")
	}
	if loaded.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

func TestSaveProfile_PreservesConfiguredAt(t *testing.T) {
	dir := t.TempDir()
	userID := "user1"

	p := &UserProfile{UserName: "Alice"}
	if err := SaveProfile(dir, userID, p); err != nil {
		t.Fatalf("save1: %v", err)
	}

	loaded1, _ := LoadProfile(dir, userID)
	firstConfigured := loaded1.ConfiguredAt

	loaded1.AgentName = "Bob"
	if err := SaveProfile(dir, userID, loaded1); err != nil {
		t.Fatalf("save2: %v", err)
	}

	loaded2, _ := LoadProfile(dir, userID)
	if !loaded2.ConfiguredAt.Equal(firstConfigured) {
		t.Errorf("ConfiguredAt changed: %v -> %v", firstConfigured, loaded2.ConfiguredAt)
	}
	if loaded2.AgentName != "Bob" {
		t.Errorf("AgentName should be Bob, got %q", loaded2.AgentName)
	}
}

func TestProfilePath_Security(t *testing.T) {
	path := profilePath("/base", "user1")
	if !strings.Contains(path, filepath.Join("users", "user1", "profile.json")) {
		t.Errorf("unexpected path: %s", path)
	}
}

func TestProfileIsEmpty(t *testing.T) {
	p := &UserProfile{}
	if !p.IsEmpty() {
		t.Error("zero-value profile should be empty")
	}

	p.UserName = "test"
	if p.IsEmpty() {
		t.Error("profile with a name should not be empty")
	}
}

func TestProfileFormatDisplay(t *testing.T) {
	p := &UserProfile{
		UserName: "Alice",
		Language: "Spanish",
	}
	display := p.FormatDisplay()
	if !strings.Contains(display, "Alice") {
		t.Error("display should contain user name")
	}
	if !strings.Contains(display, "Spanish") {
		t.Error("display should contain language")
	}
	if !strings.Contains(display, "(not set)") {
		t.Error("display should show (not set) for empty fields")
	}
	if !strings.Contains(display, "Profile Settings") {
		t.Error("display should have header")
	}
}

func TestProfileFormatForPrompt(t *testing.T) {
	p := &UserProfile{}
	if p.FormatForPrompt() != "" {
		t.Error("empty profile should return empty prompt")
	}

	p.UserName = "Alice"
	p.Language = "Spanish"
	prompt := p.FormatForPrompt()
	if !strings.Contains(prompt, "User Profile") {
		t.Error("prompt should have User Profile header")
	}
	if !strings.Contains(prompt, "Alice") {
		t.Error("prompt should contain user name")
	}
	if !strings.Contains(prompt, "Spanish") {
		t.Error("prompt should contain language")
	}
	if strings.Contains(prompt, "Personality") {
		t.Error("prompt should not include empty fields")
	}
}

func TestResolveFieldAlias(t *testing.T) {
	tests := []struct {
		alias string
		idx   int
		ok    bool
	}{
		{"name", 0, true},
		{"Name", 0, true},
		{"USERNAME", 0, true},
		{"1", 0, true},
		{"agent", 1, true},
		{"2", 1, true},
		{"personality", 2, true},
		{"persona", 2, true},
		{"3", 2, true},
		{"location", 3, true},
		{"city", 3, true},
		{"4", 3, true},
		{"skill", 4, true},
		{"level", 4, true},
		{"5", 4, true},
		{"language", 5, true},
		{"lang", 5, true},
		{"6", 5, true},
		{"latitude", 6, true},
		{"lat", 6, true},
		{"7", 6, true},
		{"longitude", 7, true},
		{"lon", 7, true},
		{"lng", 7, true},
		{"8", 7, true},
		{"unknown", -1, false},
		{"9", -1, false},
	}
	for _, tc := range tests {
		idx, ok := ResolveFieldAlias(tc.alias)
		if ok != tc.ok {
			t.Errorf("ResolveFieldAlias(%q): ok=%v, want %v", tc.alias, ok, tc.ok)
		}
		if ok && idx != tc.idx {
			t.Errorf("ResolveFieldAlias(%q): idx=%d, want %d", tc.alias, idx, tc.idx)
		}
	}
}

func TestSetField(t *testing.T) {
	p := &UserProfile{}

	p.SetField(0, "Alice")
	if p.UserName != "Alice" {
		t.Errorf("SetField(0): got %q", p.UserName)
	}

	p.SetField(1, "Nipper")
	if p.AgentName != "Nipper" {
		t.Errorf("SetField(1): got %q", p.AgentName)
	}

	p.SetField(2, "helpful")
	if p.Personality != "helpful" {
		t.Errorf("SetField(2): got %q", p.Personality)
	}

	p.SetField(3, "Tokyo, Japan")
	if p.Location != "Tokyo, Japan" {
		t.Errorf("SetField(3): got %q", p.Location)
	}

	p.SetField(4, "expert")
	if p.SkillLevel != "expert" {
		t.Errorf("SetField(4): got %q", p.SkillLevel)
	}

	p.SetField(5, "Japanese")
	if p.Language != "Japanese" {
		t.Errorf("SetField(5): got %q", p.Language)
	}

	p.SetField(6, "35.6762")
	if p.Latitude != "35.6762" {
		t.Errorf("SetField(6): got %q", p.Latitude)
	}

	p.SetField(7, "139.6503")
	if p.Longitude != "139.6503" {
		t.Errorf("SetField(7): got %q", p.Longitude)
	}

	// Out of bounds should not panic.
	p.SetField(-1, "x")
	p.SetField(99, "x")
}

func TestSaveProfile_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	userID := "brand-new-user"

	p := &UserProfile{UserName: "Test"}
	if err := SaveProfile(dir, userID, p); err != nil {
		t.Fatalf("save: %v", err)
	}

	path := profilePath(dir, userID)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("profile file should exist at %s", path)
	}
}

func TestParseCoordinates_Valid(t *testing.T) {
	tests := []struct {
		input   string
		lat     string
		lon     string
	}{
		{"45.49,-75.66", "45.49", "-75.66"},
		{"45.49, -75.66", "45.49", "-75.66"},
		{"45.49 -75.66", "45.49", "-75.66"},
		{" 45.49 , -75.66 ", "45.49", "-75.66"},
	}
	for _, tc := range tests {
		lat, lon, err := ParseCoordinates(tc.input)
		if err != nil {
			t.Errorf("ParseCoordinates(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if lat != tc.lat || lon != tc.lon {
			t.Errorf("ParseCoordinates(%q) = (%q, %q), want (%q, %q)", tc.input, lat, lon, tc.lat, tc.lon)
		}
	}
}

func TestParseCoordinates_Invalid(t *testing.T) {
	invalids := []string{
		"",
		"45.49",
		"abc,-75.66",
		"45.49,abc",
		"1,2,3",
	}
	for _, s := range invalids {
		_, _, err := ParseCoordinates(s)
		if err == nil {
			t.Errorf("ParseCoordinates(%q): expected error", s)
		}
	}
}

func TestIsCoordinatesAlias(t *testing.T) {
	if !IsCoordinatesAlias("coordinates") {
		t.Error("expected true for 'coordinates'")
	}
	if !IsCoordinatesAlias("coords") {
		t.Error("expected true for 'coords'")
	}
	if !IsCoordinatesAlias("GPS") {
		t.Error("expected true for 'GPS'")
	}
	if IsCoordinatesAlias("latitude") {
		t.Error("expected false for 'latitude'")
	}
}

func TestProfileFormatForPrompt_WithCoordinates(t *testing.T) {
	p := &UserProfile{
		UserName:  "Alice",
		Latitude:  "45.49",
		Longitude: "-75.66",
	}
	prompt := p.FormatForPrompt()
	if !strings.Contains(prompt, "45.49") {
		t.Error("prompt should contain latitude")
	}
	if !strings.Contains(prompt, "-75.66") {
		t.Error("prompt should contain longitude")
	}
	if !strings.Contains(prompt, "Coordinates") {
		t.Error("prompt should contain Coordinates label")
	}
}

func TestProfileIsEmpty_WithCoordinates(t *testing.T) {
	p := &UserProfile{Latitude: "45.49", Longitude: "-75.66"}
	if p.IsEmpty() {
		t.Error("profile with coordinates should not be empty")
	}
}

func TestLoadProfile_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	userID := "user1"

	profileDir := filepath.Join(dir, "users", userID)
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "profile.json"), []byte("{invalid"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadProfile(dir, userID)
	if err == nil {
		t.Error("expected error for corrupt profile")
	}
}

func TestIsSkillLevelAboveIntermediate(t *testing.T) {
	above := []string{"advanced", "expert", "ADVANCED", "Expert", " advanced "}
	for _, level := range above {
		if !IsSkillLevelAboveIntermediate(level) {
			t.Errorf("IsSkillLevelAboveIntermediate(%q) = false, want true", level)
		}
	}
	notAbove := []string{"", "beginner", "intermediate", "unknown", "foo"}
	for _, level := range notAbove {
		if IsSkillLevelAboveIntermediate(level) {
			t.Errorf("IsSkillLevelAboveIntermediate(%q) = true, want false", level)
		}
	}
}
