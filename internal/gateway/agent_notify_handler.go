package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/channels"
	"github.com/jescarri/open-nipper/internal/datastore"
	"github.com/jescarri/open-nipper/internal/models"
)

// AgentNotifyHandler implements POST /agents/me/notify — sends a text message
// to all of the user's registered channels (same resolution as cron broadcast).
// Used by the agent to notify the user out-of-band (e.g. OIDC device auth URL).
type AgentNotifyHandler struct {
	repo     datastore.Repository
	adapters map[models.ChannelType]channels.ChannelAdapter
	logger   *zap.Logger
}

// AgentNotifyHandlerDeps bundles dependencies.
type AgentNotifyHandlerDeps struct {
	Repo     datastore.Repository
	Adapters map[models.ChannelType]channels.ChannelAdapter
	Logger   *zap.Logger
}

// NewAgentNotifyHandler creates a new AgentNotifyHandler.
func NewAgentNotifyHandler(deps AgentNotifyHandlerDeps) *AgentNotifyHandler {
	return &AgentNotifyHandler{
		repo:     deps.Repo,
		adapters: deps.Adapters,
		logger:   deps.Logger,
	}
}

type notifyRequest struct {
	Message string `json:"message"`
}

type notifyResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Handle processes POST /agents/me/notify.
func (h *AgentNotifyHandler) Handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID, ok := h.resolveAgentUser(w, r)
	if !ok {
		return
	}

	var body notifyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeNotifyJSON(w, http.StatusBadRequest, notifyResponse{OK: false, Error: "invalid JSON"})
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeNotifyJSON(w, http.StatusBadRequest, notifyResponse{OK: false, Error: "message is required"})
		return
	}

	// Resolve notify channels from the user's registered identities (same as cron).
	notifyChannels, err := resolveNotifyChannels(ctx, h.repo, userID)
	if err != nil {
		h.logger.Error("resolve notify channels", zap.String("userId", userID), zap.Error(err))
		writeNotifyJSON(w, http.StatusInternalServerError, notifyResponse{OK: false, Error: "failed to resolve delivery channels"})
		return
	}

	if len(notifyChannels) == 0 {
		h.logger.Warn("no notify channels for user", zap.String("userId", userID))
		writeNotifyJSON(w, http.StatusOK, notifyResponse{OK: true}) // no channels, nothing to do
		return
	}

	// Deliver directly to each channel adapter (same logic as dispatcher broadcast).
	responseID := uuid.NewString()
	for _, target := range notifyChannels {
		parts := strings.SplitN(target, ":", 2)
		if len(parts) != 2 {
			h.logger.Warn("invalid notifyChannels format", zap.String("target", target))
			continue
		}
		ct := models.ChannelType(parts[0])
		channelID := parts[1]
		adapter, exists := h.adapters[ct]
		if !exists {
			h.logger.Warn("notify target channel not found", zap.String("channelType", string(ct)))
			continue
		}

		resp := &models.NipperResponse{
			ResponseID:  responseID,
			SessionKey:  "system-notify-" + responseID,
			UserID:      userID,
			ChannelType: ct,
			Text:        body.Message,
			DeliveryContext: models.DeliveryContext{
				ChannelType: ct,
				ChannelID:   channelID,
			},
			Timestamp: time.Now().UTC(),
		}

		// Set channel-specific Meta required by each adapter.
		switch ct {
		case models.ChannelTypeWhatsApp:
			resp.Meta = models.WhatsAppMeta{ChatJID: channelID, SenderJID: channelID}
		case models.ChannelTypeSlack:
			resp.Meta = models.SlackMeta{ChannelID: channelID}
		}

		if err := adapter.DeliverResponse(ctx, resp); err != nil {
			h.logger.Error("notify delivery failed",
				zap.String("channelType", string(ct)),
				zap.String("channelId", channelID),
				zap.Error(err),
			)
		}
	}

	h.logger.Info("notification sent",
		zap.String("userId", userID),
		zap.Int("channels", len(notifyChannels)),
	)
	writeNotifyJSON(w, http.StatusOK, notifyResponse{OK: true})
}

// resolveAgentUser validates Bearer token and returns the user ID.
func (h *AgentNotifyHandler) resolveAgentUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	token := extractBearerToken(r)
	if token == "" {
		writeNotifyJSON(w, http.StatusUnauthorized, notifyResponse{OK: false, Error: "unauthorized"})
		return "", false
	}
	agent, err := h.repo.GetAgentByTokenHash(r.Context(), hashToken(token))
	if err != nil {
		writeNotifyJSON(w, http.StatusUnauthorized, notifyResponse{OK: false, Error: "unauthorized"})
		return "", false
	}
	if agent.Status != "provisioned" && agent.Status != "registered" {
		writeNotifyJSON(w, http.StatusForbidden, notifyResponse{OK: false, Error: "agent revoked"})
		return "", false
	}
	return agent.UserID, true
}

func writeNotifyJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
