package models

import "time"

// QueueMode determines how messages are buffered and dispatched.
type QueueMode string

const (
	QueueModeSteer     QueueMode = "steer"
	QueueModeCollect   QueueMode = "collect"
	QueueModeInterrupt QueueMode = "interrupt"
	QueueModeQueue     QueueMode = "queue"
)

// QueueItem wraps a NipperMessage with queue metadata for routing.
type QueueItem struct {
	ID          string        `json:"id"`
	Message     *NipperMessage `json:"message"`
	Mode        QueueMode     `json:"mode"`
	Priority    int           `json:"priority"`
	EnqueuedAt  time.Time     `json:"enqueuedAt"`
	CollectedMessages []*NipperMessage `json:"collectedMessages,omitempty"`
}

// ControlMessage is sent on the nipper.control exchange to signal the agent.
type ControlMessage struct {
	Type      ControlMessageType `json:"type"`
	UserID    string             `json:"userId"`
	SessionKey string            `json:"sessionKey,omitempty"`
	Timestamp time.Time          `json:"timestamp"`
}

// ControlMessageType enumerates control signals.
type ControlMessageType string

const (
	ControlMessageInterrupt ControlMessageType = "interrupt"
	ControlMessageAbort     ControlMessageType = "abort"
)
