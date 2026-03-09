package queue

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

// Broker manages two long-lived AMQP connections: one for publishing (Gateway → agents)
// and one for consuming (agents → Gateway).  Background goroutines watch each connection
// and reconnect with exponential back-off when they drop.
type Broker struct {
	cfg    *config.QueueRabbitMQConfig
	logger *zap.Logger

	mu          sync.RWMutex
	publishConn *amqp.Connection
	consumeConn *amqp.Connection

	ctx    context.Context
	cancel context.CancelFunc

	cbMu               sync.Mutex
	reconnectCallbacks []func()
}

// NewBroker creates a Broker from the queue RabbitMQ configuration.
func NewBroker(cfg *config.QueueRabbitMQConfig, logger *zap.Logger) *Broker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Broker{
		cfg:    cfg,
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Connect dials both AMQP connections and starts the reconnect watchdog goroutines.
// It returns an error only if the initial connection cannot be established.
func (b *Broker) Connect(ctx context.Context) error {
	amqpURL, err := buildAMQPURL(b.cfg)
	if err != nil {
		return fmt.Errorf("building AMQP URL: %w", err)
	}

	pubConn, err := amqp.Dial(amqpURL)
	if err != nil {
		return fmt.Errorf("dialing publish connection: %w", err)
	}
	b.setPublishConn(pubConn)

	conConn, err := amqp.Dial(amqpURL)
	if err != nil {
		pubConn.Close()
		return fmt.Errorf("dialing consume connection: %w", err)
	}
	b.setConsumeConn(conConn)

	b.logger.Info("AMQP connections established",
		zap.String("url", sanitizeAMQPURL(amqpURL)),
	)

	go b.watchConnection("publish", pubConn, amqpURL, b.setPublishConn)
	go b.watchConnection("consume", conConn, amqpURL, b.setConsumeConn)

	return nil
}

// watchConnection monitors conn and reconnects with exponential back-off on unexpected close.
func (b *Broker) watchConnection(name string, conn *amqp.Connection, amqpURL string, setFn func(*amqp.Connection)) {
	current := conn
	for {
		notifyClose := current.NotifyClose(make(chan *amqp.Error, 1))

		select {
		case <-b.ctx.Done():
			return
		case amqpErr, ok := <-notifyClose:
			if !ok {
				// Channel closed; check whether it was intentional.
				select {
				case <-b.ctx.Done():
					return
				default:
				}
				b.logger.Warn("AMQP connection closed", zap.String("connection", name))
			} else if amqpErr != nil {
				b.logger.Warn("AMQP connection lost",
					zap.String("connection", name),
					zap.String("reason", amqpErr.Reason),
					zap.Int("code", int(amqpErr.Code)),
				)
			}
		}

		newConn := b.reconnectWithBackoff(name, amqpURL)
		if newConn == nil {
			return // context cancelled
		}
		setFn(newConn)
		current = newConn

		b.logger.Info("AMQP connection restored", zap.String("connection", name))
		b.notifyReconnect()
	}
}

// reconnectWithBackoff repeatedly dials until success or context cancellation.
func (b *Broker) reconnectWithBackoff(name, amqpURL string) *amqp.Connection {
	initial := time.Duration(b.cfg.Reconnect.InitialDelayMS) * time.Millisecond
	if initial <= 0 {
		initial = time.Second
	}
	maxDelay := time.Duration(b.cfg.Reconnect.MaxDelayMS) * time.Millisecond
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}

	delay := initial
	for {
		select {
		case <-b.ctx.Done():
			return nil
		case <-time.After(delay):
		}

		conn, err := amqp.Dial(amqpURL)
		if err == nil {
			return conn
		}

		b.logger.Warn("AMQP reconnect failed, retrying",
			zap.String("connection", name),
			zap.Error(err),
			zap.Duration("nextRetry", delay),
		)

		next := time.Duration(math.Min(float64(delay*2), float64(maxDelay)))
		delay = next
	}
}

// PublishChannel creates and returns a new AMQP channel on the publish connection.
func (b *Broker) PublishChannel() (*amqp.Channel, error) {
	b.mu.RLock()
	conn := b.publishConn
	b.mu.RUnlock()

	if conn == nil || conn.IsClosed() {
		return nil, fmt.Errorf("publish connection not available")
	}
	return conn.Channel()
}

// ConsumeChannel creates and returns a new AMQP channel on the consume connection.
func (b *Broker) ConsumeChannel() (*amqp.Channel, error) {
	b.mu.RLock()
	conn := b.consumeConn
	b.mu.RUnlock()

	if conn == nil || conn.IsClosed() {
		return nil, fmt.Errorf("consume connection not available")
	}
	return conn.Channel()
}

// IsConnected reports whether both AMQP connections are currently open.
func (b *Broker) IsConnected() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.publishConn != nil && !b.publishConn.IsClosed() &&
		b.consumeConn != nil && !b.consumeConn.IsClosed()
}

// OnReconnect registers fn to be called in a new goroutine after either connection is restored.
func (b *Broker) OnReconnect(fn func()) {
	b.cbMu.Lock()
	defer b.cbMu.Unlock()
	b.reconnectCallbacks = append(b.reconnectCallbacks, fn)
}

// Close cancels the watchdog goroutines and closes both AMQP connections.
func (b *Broker) Close() error {
	b.cancel()

	b.mu.Lock()
	defer b.mu.Unlock()

	var errs []string
	if b.publishConn != nil && !b.publishConn.IsClosed() {
		if err := b.publishConn.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("publish: %v", err))
		}
	}
	if b.consumeConn != nil && !b.consumeConn.IsClosed() {
		if err := b.consumeConn.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("consume: %v", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("closing broker connections: %v", errs)
	}
	return nil
}

// --- internal helpers ---

func (b *Broker) setPublishConn(c *amqp.Connection) {
	b.mu.Lock()
	b.publishConn = c
	b.mu.Unlock()
}

func (b *Broker) setConsumeConn(c *amqp.Connection) {
	b.mu.Lock()
	b.consumeConn = c
	b.mu.Unlock()
}

func (b *Broker) notifyReconnect() {
	b.cbMu.Lock()
	cbs := make([]func(), len(b.reconnectCallbacks))
	copy(cbs, b.reconnectCallbacks)
	b.cbMu.Unlock()

	for _, cb := range cbs {
		go cb()
	}
}

// buildAMQPURL constructs a fully-qualified AMQP URL from the config fields.
// The base URL (e.g. "amqp://localhost:5672") is augmented with credentials and vhost.
func buildAMQPURL(cfg *config.QueueRabbitMQConfig) (string, error) {
	if cfg.URL == "" {
		return "", fmt.Errorf("RabbitMQ URL must not be empty")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return "", fmt.Errorf("parsing RabbitMQ URL %q: %w", cfg.URL, err)
	}
	if cfg.Username != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}
	if cfg.VHost != "" {
		// url.URL.Path stores the decoded form; url.URL.RawPath stores the raw
		// percent-encoded form that String() will emit.  We must set both so
		// that String() preserves the percent-encoding (e.g., vhost "/nipper"
		// → URL path segment "/%2Fnipper", which amqp091-go decodes back to "/nipper").
		u.Path = "/" + cfg.VHost
		u.RawPath = "/" + url.PathEscape(cfg.VHost)
	}
	return u.String(), nil
}

// sanitizeAMQPURL strips credentials from a URL for safe log output.
func sanitizeAMQPURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "[unparseable]"
	}
	u.User = nil
	return u.String()
}
