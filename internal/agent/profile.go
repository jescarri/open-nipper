package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// UserProfile holds persistent user configuration that survives session resets.
// Stored at {basePath}/users/{userID}/profile.json.
type UserProfile struct {
	UserName    string    `json:"userName"`
	AgentName   string    `json:"agentName"`
	Personality string    `json:"personality"`
	Location    string    `json:"location"`
	SkillLevel  string    `json:"skillLevel"`
	Language    string    `json:"language"`
	Latitude    string    `json:"latitude,omitempty"`
	Longitude   string    `json:"longitude,omitempty"`
	ConfiguredAt time.Time `json:"configuredAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// profileField maps a user-facing field identifier to getter/setter on UserProfile.
type profileField struct {
	Label  string
	Get    func(*UserProfile) string
	Set    func(*UserProfile, string)
}

var profileFields = []profileField{
	{
		Label: "User Name",
		Get:   func(p *UserProfile) string { return p.UserName },
		Set:   func(p *UserProfile, v string) { p.UserName = v },
	},
	{
		Label: "Agent Name",
		Get:   func(p *UserProfile) string { return p.AgentName },
		Set:   func(p *UserProfile, v string) { p.AgentName = v },
	},
	{
		Label: "Personality",
		Get:   func(p *UserProfile) string { return p.Personality },
		Set:   func(p *UserProfile, v string) { p.Personality = v },
	},
	{
		Label: "Location",
		Get:   func(p *UserProfile) string { return p.Location },
		Set:   func(p *UserProfile, v string) { p.Location = v },
	},
	{
		Label: "Skill Level",
		Get:   func(p *UserProfile) string { return p.SkillLevel },
		Set:   func(p *UserProfile, v string) { p.SkillLevel = v },
	},
	{
		Label: "Preferred Language",
		Get:   func(p *UserProfile) string { return p.Language },
		Set:   func(p *UserProfile, v string) { p.Language = v },
	},
	{
		Label: "Latitude",
		Get:   func(p *UserProfile) string { return p.Latitude },
		Set:   func(p *UserProfile, v string) { p.Latitude = v },
	},
	{
		Label: "Longitude",
		Get:   func(p *UserProfile) string { return p.Longitude },
		Set:   func(p *UserProfile, v string) { p.Longitude = v },
	},
}

// profileFieldAliases maps user-facing short names to the profileFields index.
var profileFieldAliases = map[string]int{
	"name":        0,
	"username":    0,
	"user":        0,
	"1":           0,
	"agent":       1,
	"agentname":   1,
	"bot":         1,
	"2":           1,
	"personality": 2,
	"persona":     2,
	"3":           2,
	"location":    3,
	"city":        3,
	"loc":         3,
	"4":           3,
	"skill":       4,
	"skilllevel":  4,
	"level":       4,
	"5":           4,
	"language":    5,
	"lang":        5,
	"6":           5,
	"latitude":    6,
	"lat":         6,
	"7":           6,
	"longitude":   7,
	"lon":         7,
	"lng":         7,
	"8":           7,
}

// coordinatesAliases are special compound field names handled outside the
// single-field system. They set both latitude and longitude at once.
var coordinatesAliases = map[string]bool{
	"coordinates": true,
	"coords":      true,
	"gps":         true,
}

// IsCoordinatesAlias returns true if the alias is a compound coordinates field.
func IsCoordinatesAlias(alias string) bool {
	return coordinatesAliases[strings.ToLower(strings.TrimSpace(alias))]
}

// ParseCoordinates parses a "lat,lon" or "lat lon" string into two components.
func ParseCoordinates(raw string) (lat, lon string, err error) {
	raw = strings.TrimSpace(raw)
	var parts []string
	if strings.Contains(raw, ",") {
		parts = strings.SplitN(raw, ",", 2)
	} else {
		parts = strings.Fields(raw)
	}
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected 'latitude,longitude' (e.g., 45.49,-75.66)")
	}
	lat = strings.TrimSpace(parts[0])
	lon = strings.TrimSpace(parts[1])

	if _, err := strconv.ParseFloat(lat, 64); err != nil {
		return "", "", fmt.Errorf("invalid latitude %q: must be a number", lat)
	}
	if _, err := strconv.ParseFloat(lon, 64); err != nil {
		return "", "", fmt.Errorf("invalid longitude %q: must be a number", lon)
	}
	return lat, lon, nil
}

func profilePath(basePath, userID string) string {
	return filepath.Join(basePath, "users", userID, "profile.json")
}

// LoadProfile reads the user profile from disk. Returns a zero-value profile
// (not an error) if the file does not exist.
func LoadProfile(basePath, userID string) (*UserProfile, error) {
	path := profilePath(basePath, userID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &UserProfile{}, nil
		}
		return nil, fmt.Errorf("reading profile: %w", err)
	}
	var p UserProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decoding profile: %w", err)
	}
	return &p, nil
}

// SaveProfile writes the user profile to disk atomically.
func SaveProfile(basePath, userID string, p *UserProfile) error {
	path := profilePath(basePath, userID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating profile dir: %w", err)
	}

	p.UpdatedAt = time.Now().UTC()
	if p.ConfiguredAt.IsZero() {
		p.ConfiguredAt = p.UpdatedAt
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding profile: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing profile tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming profile: %w", err)
	}
	return nil
}

// IsEmpty returns true if no fields have been set.
func (p *UserProfile) IsEmpty() bool {
	return p.UserName == "" && p.AgentName == "" && p.Personality == "" &&
		p.Location == "" && p.SkillLevel == "" && p.Language == "" &&
		p.Latitude == "" && p.Longitude == ""
}

// FormatDisplay returns a human-readable listing of the profile suitable for
// sending back in a chat message.
func (p *UserProfile) FormatDisplay() string {
	var sb strings.Builder
	sb.WriteString("*Profile Settings*\n\n")

	for i, f := range profileFields {
		val := f.Get(p)
		if val == "" {
			val = "(not set)"
		}
		sb.WriteString(fmt.Sprintf("%d. %s: %s\n", i+1, f.Label, val))
	}

	sb.WriteString("\n*To change a setting:*\n")
	sb.WriteString("  /setup name <your name>\n")
	sb.WriteString("  /setup agent <agent name>\n")
	sb.WriteString("  /setup personality <type>\n")
	sb.WriteString("  /setup location <City, Country>\n")
	sb.WriteString("  /setup skill <beginner|intermediate|advanced|expert>\n")
	sb.WriteString("  /setup language <preferred language>\n")
	sb.WriteString("  /setup coords <lat,lon> (e.g., 45.49,-75.66)\n")
	sb.WriteString("\nYou can also use the field number: /setup 1 Jane Doe")
	return sb.String()
}

// FormatForPrompt returns a block suitable for injection into the system prompt.
// Returns an empty string if the profile is empty.
func (p *UserProfile) FormatForPrompt() string {
	if p.IsEmpty() {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## User Profile\n\n")

	if p.UserName != "" {
		sb.WriteString(fmt.Sprintf("- User Name: %s\n", p.UserName))
	}
	if p.AgentName != "" {
		sb.WriteString(fmt.Sprintf("- Your Name (the assistant): %s\n", p.AgentName))
	}
	if p.Personality != "" {
		sb.WriteString(fmt.Sprintf("- Personality: %s\n", p.Personality))
	}
	if p.Location != "" {
		sb.WriteString(fmt.Sprintf("- User Location: %s\n", p.Location))
	}
	if p.SkillLevel != "" {
		sb.WriteString(fmt.Sprintf("- User Skill Level: %s\n", p.SkillLevel))
	}
	if p.Language != "" {
		sb.WriteString(fmt.Sprintf("- Preferred Language: %s (always respond in this language unless asked otherwise)\n", p.Language))
	}
	if p.Latitude != "" && p.Longitude != "" {
		sb.WriteString(fmt.Sprintf("- Coordinates: %s, %s (use these for weather and location-based tools)\n", p.Latitude, p.Longitude))
	}

	return sb.String()
}

// ResolveFieldAlias looks up a field alias (case-insensitive) and returns the
// index into profileFields plus true, or -1 and false if not found.
func ResolveFieldAlias(alias string) (int, bool) {
	idx, ok := profileFieldAliases[strings.ToLower(strings.TrimSpace(alias))]
	return idx, ok
}

// SetField updates a single profile field by its index in profileFields.
func (p *UserProfile) SetField(idx int, value string) {
	if idx >= 0 && idx < len(profileFields) {
		profileFields[idx].Set(p, strings.TrimSpace(value))
	}
}

// FieldLabel returns the human-facing label for a field index.
func FieldLabel(idx int) string {
	if idx >= 0 && idx < len(profileFields) {
		return profileFields[idx].Label
	}
	return "Unknown"
}

// Skill level ordering: beginner < intermediate < advanced < expert.
const (
	SkillBeginner     = "beginner"
	SkillIntermediate = "intermediate"
	SkillAdvanced     = "advanced"
	SkillExpert       = "expert"
)

// IsSkillLevelAboveIntermediate returns true when the user's skill level is
// advanced or expert (i.e. strictly greater than intermediate).
func IsSkillLevelAboveIntermediate(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case SkillAdvanced, SkillExpert:
		return true
	default:
		return false
	}
}
