// Package queue implements the internal RabbitMQ queue system used for Gateway↔Agent communication.
package queue

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
)

// AMQPChannel is the subset of *amqp.Channel operations used by this package.
// Defining it as an interface allows topology, publisher, and consumer logic to be
// tested without a live RabbitMQ broker.
type AMQPChannel interface {
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
	Qos(prefetchCount, prefetchSize int, global bool) error
	IsClosed() bool
	Close() error
}
