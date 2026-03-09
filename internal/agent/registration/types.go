// Package registration provides the Gateway auto-registration client for Open-Nipper agents.
package registration

// RegistrationResult holds the full config blob returned by the Gateway on successful registration.
type RegistrationResult struct {
	AgentID  string      `json:"agent_id"`
	UserID   string      `json:"user_id"`
	UserName string      `json:"user_name"`
	RabbitMQ RMQConfig   `json:"rabbitmq"`
	User     UserInfo    `json:"user"`
	Policies PoliciesInfo `json:"policies"`
}

// RMQConfig holds the RabbitMQ connection details provided by the Gateway.
type RMQConfig struct {
	URL         string           `json:"url"`
	TLSURL      string           `json:"tls_url,omitempty"`
	Username    string           `json:"username"`
	Password    string           `json:"password"`
	VHost       string           `json:"vhost"`
	Queues      QueuesConfig     `json:"queues"`
	Exchanges   ExchangesConfig  `json:"exchanges"`
	RoutingKeys RoutingKeysConfig `json:"routing_keys"`
}

// QueuesConfig holds per-user queue names.
type QueuesConfig struct {
	Agent   string `json:"agent"`
	Control string `json:"control"`
}

// ExchangesConfig holds exchange names.
type ExchangesConfig struct {
	Sessions string `json:"sessions"`
	Events   string `json:"events"`
	Control  string `json:"control"`
	Logs     string `json:"logs"`
}

// RoutingKeysConfig holds routing key templates.
// The {sessionId} and {eventType} placeholders must be replaced before use.
type RoutingKeysConfig struct {
	EventsPublish string `json:"events_publish"`
	LogsPublish   string `json:"logs_publish"`
}

// UserInfo holds user preferences returned during registration.
type UserInfo struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	DefaultModel string         `json:"default_model"`
	Preferences  map[string]any `json:"preferences"`
}

// PoliciesInfo holds tool allow/deny policies for this agent.
type PoliciesInfo struct {
	Tools *ToolsPolicy `json:"tools,omitempty"`
}

// ToolsPolicy specifies which tools are allowed or denied.
type ToolsPolicy struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// registrationResponse is the outer JSON wrapper returned by the Gateway.
type registrationResponse struct {
	OK     bool                  `json:"ok"`
	Result *RegistrationResult   `json:"result,omitempty"`
	Error  string                `json:"error,omitempty"`
}
