package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
	"github.com/open-nipper/open-nipper/pkg/session"
)

// CommandAction describes the side-effect of a handled command.
type CommandAction int

const (
	ActionNone       CommandAction = iota
	ActionNewSession               // session was reset; transcript cleared
	ActionCompact                  // transcript was compacted
)

// CommandResult is the outcome of processing a slash command.
type CommandResult struct {
	Handled  bool
	Response string
	Action   CommandAction
}

// handleCommand checks if the message text is a slash command and executes it.
// Returns nil if the message is not a command.
func (r *Runtime) handleCommand(ctx context.Context, msg *models.NipperMessage) *CommandResult {
	text := strings.TrimSpace(msg.Content.Text)
	if text == "" {
		for _, p := range msg.Content.Parts {
			if p.Type == "text" && p.Text != "" {
				text = strings.TrimSpace(p.Text)
				break
			}
		}
	}

	if !strings.HasPrefix(text, "/") {
		return nil
	}

	parts := strings.SplitN(text, " ", 2)
	command := strings.ToLower(parts[0])
	var args string
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	switch command {
	case "/help":
		return r.cmdHelp()
	case "/new", "/reset":
		return r.cmdNewSession(ctx, msg)
	case "/usage":
		return r.cmdUsage(msg.SessionKey)
	case "/compact":
		return r.cmdCompact(ctx, msg.SessionKey)
	case "/status":
		return r.cmdStatus(ctx, msg.SessionKey)
	case "/persona":
		return r.cmdPersona(ctx, msg.SessionKey, args)
	case "/setup":
		return r.cmdSetup(msg.UserID, args)
	default:
		return nil
	}
}

func (r *Runtime) cmdHelp() *CommandResult {
	help := `*Available Commands*

- /help — Show this help message
- /new — Start a new session (clears conversation history)
- /reset — Alias for /new
- /setup — View or update your profile (name, language, etc.)
- /usage — Show context window usage and estimated LLM costs
- /compact — Force transcript compaction to free context space
- /status — Show current session information
- /persona <description> — Set the agent persona for this session

All commands are processed locally and do not consume LLM tokens.`

	return &CommandResult{
		Handled:  true,
		Response: help,
	}
}

func (r *Runtime) cmdNewSession(ctx context.Context, msg *models.NipperMessage) *CommandResult {
	sessionKey := msg.SessionKey

	// Archive the current transcript.
	if err := r.sessions.ArchiveSession(ctx, sessionKey); err != nil {
		r.logger.Warn("failed to archive session during /new", zap.Error(err))
	}

	// Parse session key to get components.
	userID, channelType, sessionID, parseErr := session.ParseSessionKey(sessionKey)
	if parseErr != nil {
		userID = msg.UserID
		channelType = string(msg.ChannelType)
		sessionID = sessionKey
	}

	// Re-create the session with a clean slate (same key components).
	model := r.cfg.Inference.Model
	if r.reg.User.DefaultModel != "" {
		model = r.reg.User.DefaultModel
	}

	_, err := r.sessions.CreateSession(ctx, session.CreateSessionRequest{
		UserID:      userID,
		SessionID:   sessionID,
		ChannelType: channelType,
		Model:       model,
	})
	if err != nil {
		r.logger.Error("failed to create new session during /new", zap.Error(err))
		return &CommandResult{
			Handled:  true,
			Response: "Failed to create new session. Please try again.",
		}
	}

	// Clear usage tracking for this session.
	if r.usageTracker != nil {
		r.usageTracker.Reset(sessionKey)
	}

	r.logger.Info("session cleared via /new command",
		zap.String("sessionKey", sessionKey),
		zap.String("userId", userID),
	)

	return &CommandResult{
		Handled:  true,
		Response: "Session cleared. Starting fresh. Your conversation history has been archived.",
		Action:   ActionNewSession,
	}
}

func (r *Runtime) cmdUsage(sessionKey string) *CommandResult {
	if r.usageTracker == nil {
		return &CommandResult{
			Handled:  true,
			Response: "Usage tracking is not available.",
		}
	}

	return &CommandResult{
		Handled:  true,
		Response: r.usageTracker.FormatUsage(sessionKey),
	}
}

func (r *Runtime) cmdCompact(ctx context.Context, sessionKey string) *CommandResult {
	compactor := session.NewCompactor(r.sessions.(*session.Store), r.logger)
	result, err := compactor.Compact(ctx, sessionKey, 20)
	if err != nil {
		r.logger.Error("compaction failed via /compact", zap.Error(err))
		return &CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("Compaction failed: %v", err),
		}
	}

	if !result.Compacted {
		return &CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("No compaction needed. Transcript has %d messages (threshold: 20).", result.OriginalLineCount),
			Action:   ActionCompact,
		}
	}

	return &CommandResult{
		Handled: true,
		Response: fmt.Sprintf(
			"Compaction complete.\n- Archived: %d messages\n- Remaining: %d messages\n- Compaction #%d",
			result.ArchivedLineCount, result.RemainingLineCount, result.CompactionCount,
		),
		Action: ActionCompact,
	}
}

func (r *Runtime) cmdStatus(ctx context.Context, sessionKey string) *CommandResult {
	sess, err := r.sessions.GetSession(ctx, sessionKey)
	if err != nil {
		return &CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("Could not load session: %v", err),
		}
	}

	var sb strings.Builder
	sb.WriteString("*Session Status*\n\n")
	sb.WriteString(fmt.Sprintf("- Session key: %s\n", sessionKey))
	sb.WriteString(fmt.Sprintf("- Status: %s\n", sess.Status))
	sb.WriteString(fmt.Sprintf("- Model: %s\n", sess.Metadata.Model))
	sb.WriteString(fmt.Sprintf("- Provider: %s\n", r.cfg.Inference.Provider))
	sb.WriteString(fmt.Sprintf("- Messages: %d\n", sess.Metadata.MessageCount))
	sb.WriteString(fmt.Sprintf("- Compactions: %d\n", sess.Metadata.CompactionCount))
	sb.WriteString(fmt.Sprintf("- Created: %s\n", sess.CreatedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("- Last activity: %s\n", sess.Metadata.LastActivityAt.Format(time.RFC3339)))

	if r.usageTracker != nil {
		usage := r.usageTracker.Get(sessionKey)
		if usage != nil && usage.ContextWindowSize > 0 {
			sb.WriteString(fmt.Sprintf("- Context window: %d tokens\n", usage.ContextWindowSize))
			sb.WriteString(fmt.Sprintf("- Last context usage: %.1f%%\n", usage.LastUsagePercent))
		}
	}

	return &CommandResult{
		Handled:  true,
		Response: sb.String(),
	}
}

func (r *Runtime) cmdPersona(_ context.Context, sessionKey, persona string) *CommandResult {
	if strings.TrimSpace(persona) == "" {
		return &CommandResult{
			Handled:  true,
			Response: "Usage: /persona <description>\n\nExample: /persona You are a senior DevOps engineer who speaks concisely.",
		}
	}

	r.mu.Lock()
	if r.sessionPersonas == nil {
		r.sessionPersonas = make(map[string]string)
	}
	r.sessionPersonas[sessionKey] = persona
	r.mu.Unlock()

	r.logger.Info("persona set via /persona command",
		zap.String("sessionKey", sessionKey),
		zap.Int("personaLen", len(persona)),
	)

	return &CommandResult{
		Handled:  true,
		Response: fmt.Sprintf("Persona updated for this session:\n\n_%s_", persona),
	}
}

func (r *Runtime) cmdSetup(userID, args string) *CommandResult {
	profile, err := LoadProfile(r.cfg.BasePath, userID)
	if err != nil {
		r.logger.Error("failed to load profile for /setup", zap.Error(err))
		return &CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("Failed to load profile: %v", err),
		}
	}

	args = strings.TrimSpace(args)

	// No args: show current profile and instructions.
	if args == "" {
		return &CommandResult{
			Handled:  true,
			Response: profile.FormatDisplay(),
		}
	}

	// Parse: first token is the field name/number, the rest is the value.
	parts := strings.SplitN(args, " ", 2)
	fieldKey := strings.TrimSpace(parts[0])
	var fieldValue string
	if len(parts) > 1 {
		fieldValue = strings.TrimSpace(parts[1])
	}

	// Handle compound "coordinates" / "coords" / "gps" alias.
	if IsCoordinatesAlias(fieldKey) {
		if fieldValue == "" {
			return &CommandResult{
				Handled:  true,
				Response: "Please provide coordinates.\n\nUsage: /setup coords <lat,lon>\nExample: /setup coords 45.49,-75.66",
			}
		}
		lat, lon, parseErr := ParseCoordinates(fieldValue)
		if parseErr != nil {
			return &CommandResult{
				Handled:  true,
				Response: fmt.Sprintf("Invalid coordinates: %v\n\nExample: /setup coords 45.49,-75.66", parseErr),
			}
		}
		profile.Latitude = lat
		profile.Longitude = lon
		if err := SaveProfile(r.cfg.BasePath, userID, profile); err != nil {
			r.logger.Error("failed to save profile for /setup coords", zap.Error(err))
			return &CommandResult{
				Handled:  true,
				Response: fmt.Sprintf("Failed to save profile: %v", err),
			}
		}
		r.logger.Info("profile coordinates updated via /setup",
			zap.String("userId", userID),
			zap.String("lat", lat),
			zap.String("lon", lon),
		)
		return &CommandResult{
			Handled: true,
			Response: fmt.Sprintf(
				"Updated *Coordinates* to: %s, %s\n\n%s",
				lat, lon, profile.FormatDisplay(),
			),
		}
	}

	idx, ok := ResolveFieldAlias(fieldKey)
	if !ok {
		return &CommandResult{
			Handled: true,
			Response: fmt.Sprintf(
				"Unknown field: *%s*\n\nValid fields: name, agent, personality, location, skill, language, lat, lon, coords (or 1-8).",
				fieldKey,
			),
		}
	}

	if fieldValue == "" {
		return &CommandResult{
			Handled: true,
			Response: fmt.Sprintf(
				"Please provide a value.\n\nUsage: /setup %s <value>",
				fieldKey,
			),
		}
	}

	profile.SetField(idx, fieldValue)

	if err := SaveProfile(r.cfg.BasePath, userID, profile); err != nil {
		r.logger.Error("failed to save profile for /setup", zap.Error(err))
		return &CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("Failed to save profile: %v", err),
		}
	}

	r.logger.Info("profile field updated via /setup",
		zap.String("userId", userID),
		zap.String("field", FieldLabel(idx)),
		zap.String("value", fieldValue),
	)

	return &CommandResult{
		Handled: true,
		Response: fmt.Sprintf(
			"Updated *%s* to: %s\n\n%s",
			FieldLabel(idx),
			fieldValue,
			profile.FormatDisplay(),
		),
	}
}
