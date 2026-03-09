package queue

import (
	"context"
	"fmt"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

// mockChannel records all topology declarations made against it and can be
// pre-configured to return errors for specific operations.
type mockChannel struct {
	exchanges []exchangeDecl
	queues    []queueDecl
	bindings  []bindingDecl
	publishes []publishedMsg
	qosCalls  []qosCall

	exchangeErr error
	queueErr    error
	bindErr     error
	publishErr  error
	qosErr      error

	deliveryCh <-chan amqp.Delivery
	closed     bool
}

type exchangeDecl struct {
	name, kind                              string
	durable, autoDelete, internal, noWait bool
}

type queueDecl struct {
	name                                     string
	durable, autoDelete, exclusive, noWait bool
	args                                     amqp.Table
}

type bindingDecl struct {
	queue, key, exchange string
}

type publishedMsg struct {
	exchange, key      string
	mandatory, immediate bool
	msg                amqp.Publishing
}

type qosCall struct {
	prefetchCount, prefetchSize int
	global                      bool
}

func (m *mockChannel) ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error {
	if m.exchangeErr != nil {
		return m.exchangeErr
	}
	m.exchanges = append(m.exchanges, exchangeDecl{name, kind, durable, autoDelete, internal, noWait})
	return nil
}

func (m *mockChannel) QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error) {
	if m.queueErr != nil {
		return amqp.Queue{}, m.queueErr
	}
	m.queues = append(m.queues, queueDecl{name, durable, autoDelete, exclusive, noWait, args})
	return amqp.Queue{Name: name}, nil
}

func (m *mockChannel) QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error {
	if m.bindErr != nil {
		return m.bindErr
	}
	m.bindings = append(m.bindings, bindingDecl{name, key, exchange})
	return nil
}

func (m *mockChannel) PublishWithContext(_ context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	if m.publishErr != nil {
		return m.publishErr
	}
	m.publishes = append(m.publishes, publishedMsg{exchange, key, mandatory, immediate, msg})
	return nil
}

func (m *mockChannel) Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error) {
	if m.deliveryCh != nil {
		return m.deliveryCh, nil
	}
	ch := make(chan amqp.Delivery)
	close(ch)
	return ch, nil
}

func (m *mockChannel) Qos(prefetchCount, prefetchSize int, global bool) error {
	if m.qosErr != nil {
		return m.qosErr
	}
	m.qosCalls = append(m.qosCalls, qosCall{prefetchCount, prefetchSize, global})
	return nil
}

func (m *mockChannel) IsClosed() bool { return m.closed }
func (m *mockChannel) Close() error   { m.closed = true; return nil }

// --- helpers ---

func exchangeNames(ch *mockChannel) []string {
	names := make([]string, 0, len(ch.exchanges))
	for _, ex := range ch.exchanges {
		names = append(names, ex.name)
	}
	return names
}

func queueNames(ch *mockChannel) []string {
	names := make([]string, 0, len(ch.queues))
	for _, q := range ch.queues {
		names = append(names, q.name)
	}
	return names
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func hasBinding(ch *mockChannel, queue, key, exchange string) bool {
	for _, b := range ch.bindings {
		if b.queue == queue && b.key == key && b.exchange == exchange {
			return true
		}
	}
	return false
}

// --- DeclareTopology tests ---

func TestDeclareTopology_AllExchangesDeclared(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareTopology(ch); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	expectedExchanges := []string{
		ExchangeSessions,
		ExchangeEvents,
		ExchangeControl,
		ExchangeSessionsDLX,
		ExchangeLogs,
	}
	names := exchangeNames(ch)
	for _, ex := range expectedExchanges {
		if !contains(names, ex) {
			t.Errorf("exchange %q was not declared; declared: %v", ex, names)
		}
	}
}

func TestDeclareTopology_ExchangeKinds(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareTopology(ch); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	kinds := map[string]string{}
	for _, ex := range ch.exchanges {
		kinds[ex.name] = ex.kind
	}

	for name, wantKind := range map[string]string{
		ExchangeSessions:    "topic",
		ExchangeEvents:      "topic",
		ExchangeControl:     "topic",
		ExchangeSessionsDLX: "fanout",
		ExchangeLogs:        "topic",
	} {
		if got := kinds[name]; got != wantKind {
			t.Errorf("exchange %q: got kind %q, want %q", name, got, wantKind)
		}
	}
}

func TestDeclareTopology_ExchangesDurable(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareTopology(ch); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	for _, ex := range ch.exchanges {
		if !ex.durable {
			t.Errorf("exchange %q: expected durable=true", ex.name)
		}
	}
}

func TestDeclareTopology_StaticQueues(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareTopology(ch); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	names := queueNames(ch)
	for _, q := range []string{QueueEventsGateway, QueueSessionsDLQ} {
		if !contains(names, q) {
			t.Errorf("queue %q was not declared; declared: %v", q, names)
		}
	}
}

func TestDeclareTopology_EventsGatewayBinding(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareTopology(ch); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	if !hasBinding(ch, QueueEventsGateway, "nipper.events.#", ExchangeEvents) {
		t.Errorf("expected binding %q -(%q)-> %q", QueueEventsGateway, "nipper.events.#", ExchangeEvents)
	}
}

func TestDeclareTopology_DLQBinding(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareTopology(ch); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	if !hasBinding(ch, QueueSessionsDLQ, "#", ExchangeSessionsDLX) {
		t.Errorf("expected DLQ binding to %q; bindings: %v", ExchangeSessionsDLX, ch.bindings)
	}
}

func TestDeclareTopology_ExchangeError(t *testing.T) {
	ch := &mockChannel{exchangeErr: fmt.Errorf("amqp error")}
	err := DeclareTopology(ch)
	if err == nil {
		t.Fatal("expected error when exchange declare fails")
	}
}

func TestDeclareTopology_QueueError(t *testing.T) {
	ch := &mockChannel{queueErr: fmt.Errorf("amqp error")}
	err := DeclareTopology(ch)
	if err == nil {
		t.Fatal("expected error when queue declare fails")
	}
}

func TestDeclareTopology_BindError(t *testing.T) {
	ch := &mockChannel{bindErr: fmt.Errorf("amqp bind error")}
	err := DeclareTopology(ch)
	if err == nil {
		t.Fatal("expected error when queue bind fails")
	}
}

// --- DeclareUserQueues tests ---

func TestDeclareUserQueues_QueuesCreated(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareUserQueues(ch, "user-01"); err != nil {
		t.Fatalf("DeclareUserQueues: %v", err)
	}

	names := queueNames(ch)
	for _, expected := range []string{"nipper-agent-user-01", "nipper-control-user-01"} {
		if !contains(names, expected) {
			t.Errorf("queue %q not declared; declared: %v", expected, names)
		}
	}
}

func TestDeclareUserQueues_AgentQueueArgs(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareUserQueues(ch, "user-01"); err != nil {
		t.Fatalf("DeclareUserQueues: %v", err)
	}

	for _, q := range ch.queues {
		if q.name != "nipper-agent-user-01" {
			continue
		}
		if q.args == nil {
			t.Fatal("agent queue args must not be nil")
		}
		if q.args["x-dead-letter-exchange"] != ExchangeSessionsDLX {
			t.Errorf("expected x-dead-letter-exchange=%q, got %v", ExchangeSessionsDLX, q.args["x-dead-letter-exchange"])
		}
		if q.args["x-overflow"] != "reject-publish" {
			t.Errorf("expected x-overflow=reject-publish, got %v", q.args["x-overflow"])
		}
		return
	}
	t.Error("agent queue not found in declared queues")
}

func TestDeclareUserQueues_Bindings(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareUserQueues(ch, "user-01"); err != nil {
		t.Fatalf("DeclareUserQueues: %v", err)
	}

	if !hasBinding(ch, "nipper-agent-user-01", "nipper.sessions.user-01.#", ExchangeSessions) {
		t.Errorf("missing agent queue session binding; bindings: %v", ch.bindings)
	}
	if !hasBinding(ch, "nipper-control-user-01", "nipper.control.user-01", ExchangeControl) {
		t.Errorf("missing control queue binding; bindings: %v", ch.bindings)
	}
}

func TestDeclareUserQueues_UserIDSanitized(t *testing.T) {
	ch := &mockChannel{}
	if err := DeclareUserQueues(ch, "alice"); err != nil {
		t.Fatalf("DeclareUserQueues: %v", err)
	}
	names := queueNames(ch)
	if !contains(names, "nipper-agent-alice") {
		t.Errorf("expected nipper-agent-alice; got %v", names)
	}
}

func TestUserAgentQueue(t *testing.T) {
	got := UserAgentQueue("user-01")
	want := "nipper-agent-user-01"
	if got != want {
		t.Errorf("UserAgentQueue: got %q, want %q", got, want)
	}
}

func TestUserControlQueue(t *testing.T) {
	got := UserControlQueue("user-01")
	want := "nipper-control-user-01"
	if got != want {
		t.Errorf("UserControlQueue: got %q, want %q", got, want)
	}
}
