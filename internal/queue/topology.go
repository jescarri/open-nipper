package queue

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Exchange names used by the internal Gateway↔Agent queue system.
const (
	ExchangeSessions    = "nipper.sessions"
	ExchangeEvents      = "nipper.events"
	ExchangeControl     = "nipper.control"
	ExchangeSessionsDLX = "nipper.sessions.dlx"
	ExchangeLogs        = "nipper.logs"
)

// Queue names for the static (non-per-user) queues.
const (
	QueueEventsGateway = "nipper-events-gateway"
	QueueSessionsDLQ   = "nipper-sessions-dlq"
)

// Per-user queue property values.
const (
	userQueueMessageTTL = int32(300_000) // 5 minutes in ms
	userQueueMaxLength  = int32(50)
	dlqMessageTTL       = int64(86_400_000) // 24 hours in ms
	eventsGatewayPrefetch = 50
)

// DeclareTopology declares all static exchanges, queues, and bindings.
// All operations are idempotent (passive=false, existing resources with the same
// properties are left untouched by the broker).
func DeclareTopology(ch AMQPChannel) error {
	// --- Exchanges ---

	exchanges := []struct {
		name    string
		kind    string
		durable bool
	}{
		{ExchangeSessions, "topic", true},
		{ExchangeEvents, "topic", true},
		{ExchangeControl, "topic", true},
		{ExchangeSessionsDLX, "fanout", true},
		{ExchangeLogs, "topic", true},
	}

	for _, ex := range exchanges {
		if err := ch.ExchangeDeclare(
			ex.name,
			ex.kind,
			ex.durable,
			false, // autoDelete
			false, // internal
			false, // noWait
			nil,   // args
		); err != nil {
			return fmt.Errorf("declaring exchange %q: %w", ex.name, err)
		}
	}

	// --- Static queues ---

	// nipper-events-gateway: agent events consumed by the gateway.
	if _, err := ch.QueueDeclare(
		QueueEventsGateway,
		true,  // durable
		false, // autoDelete
		false, // exclusive
		false, // noWait
		nil,   // args
	); err != nil {
		return fmt.Errorf("declaring queue %q: %w", QueueEventsGateway, err)
	}

	// nipper-sessions-dlq: dead-letter sink for expired / rejected session messages.
	dlqArgs := amqp.Table{
		"x-message-ttl": dlqMessageTTL,
	}
	if _, err := ch.QueueDeclare(
		QueueSessionsDLQ,
		true,  // durable
		false, // autoDelete
		false, // exclusive
		false, // noWait
		dlqArgs,
	); err != nil {
		return fmt.Errorf("declaring queue %q: %w", QueueSessionsDLQ, err)
	}

	// --- Bindings ---

	// All events on nipper.events.# → nipper-events-gateway.
	if err := ch.QueueBind(QueueEventsGateway, "nipper.events.#", ExchangeEvents, false, nil); err != nil {
		return fmt.Errorf("binding %q to %q: %w", QueueEventsGateway, ExchangeEvents, err)
	}

	// Dead-lettered session messages → nipper-sessions-dlq (DLX is fanout, routing key is ignored).
	if err := ch.QueueBind(QueueSessionsDLQ, "#", ExchangeSessionsDLX, false, nil); err != nil {
		return fmt.Errorf("binding %q to %q: %w", QueueSessionsDLQ, ExchangeSessionsDLX, err)
	}

	return nil
}

// DeclareUserQueues declares per-user agent and control queues for the given userID
// and creates the required bindings.  This is called at agent provisioning time and
// also during the gateway startup sequence to ensure queues exist for all provisioned users.
func DeclareUserQueues(ch AMQPChannel, userID string) error {
	agentQueue := UserAgentQueue(userID)
	controlQueue := UserControlQueue(userID)

	// Per-user agent queue — messages from nipper.sessions exchange arrive here.
	agentArgs := amqp.Table{
		"x-dead-letter-exchange":    ExchangeSessionsDLX,
		"x-dead-letter-routing-key": "nipper.sessions.dlq",
		"x-message-ttl":             userQueueMessageTTL,
		"x-max-length":              userQueueMaxLength,
		"x-overflow":                "reject-publish",
	}
	if _, err := ch.QueueDeclare(
		agentQueue,
		true,  // durable
		false, // autoDelete
		false, // exclusive
		false, // noWait
		agentArgs,
	); err != nil {
		return fmt.Errorf("declaring agent queue %q: %w", agentQueue, err)
	}

	// Bind: nipper.sessions.{userId}.# → nipper-agent-{userId}
	agentBindingKey := fmt.Sprintf("nipper.sessions.%s.#", userID)
	if err := ch.QueueBind(agentQueue, agentBindingKey, ExchangeSessions, false, nil); err != nil {
		return fmt.Errorf("binding agent queue %q: %w", agentQueue, err)
	}

	// Per-user control queue — interrupt/abort signals from the gateway.
	if _, err := ch.QueueDeclare(
		controlQueue,
		true,  // durable
		false, // autoDelete
		false, // exclusive
		false, // noWait
		nil,
	); err != nil {
		return fmt.Errorf("declaring control queue %q: %w", controlQueue, err)
	}

	// Bind: nipper.control.{userId} → nipper-control-{userId}
	controlBindingKey := fmt.Sprintf("nipper.control.%s", userID)
	if err := ch.QueueBind(controlQueue, controlBindingKey, ExchangeControl, false, nil); err != nil {
		return fmt.Errorf("binding control queue %q: %w", controlQueue, err)
	}

	return nil
}

// UserAgentQueue returns the per-user agent queue name.
func UserAgentQueue(userID string) string {
	return fmt.Sprintf("nipper-agent-%s", userID)
}

// UserControlQueue returns the per-user control queue name.
func UserControlQueue(userID string) string {
	return fmt.Sprintf("nipper-control-%s", userID)
}
