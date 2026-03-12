# Docker Compose Deployment Guide

This guide walks through deploying Open-Nipper with Docker Compose. The stack includes:

| Service    | Description                                      |
|------------|--------------------------------------------------|
| **caddy**  | Reverse proxy with automatic TLS (Let's Encrypt) |
| **rabbitmq** | Message broker for gateway-agent communication |
| **gateway** | Open-Nipper gateway (webhooks, admin API)       |
| **agent**  | Open-Nipper agent (LLM message processing)       |

## Prerequisites

- Docker Engine 24+ with Compose V2
- The user running `docker compose` must have access to the Docker daemon (i.e. be in the `docker` group or run as root)
- A public DNS hostname pointing to the server (for Let's Encrypt)
- AWS credentials with Route53 permissions for DNS-01 challenge (for TLS)

## Quick Start

```bash
cd deploy/docker-compose

# 1. Edit environment files
cp .env.caddy .env.caddy      # set NIPPER_HOSTNAME, AWS credentials
cp .env.gateway .env.gateway    # set RabbitMQ password, admin token
cp .env.agent .env.agent        # set NIPPER_AUTH_TOKEN, OPENAI_API_KEY

# 2. (Optional) Regenerate default config files
#    This outputs the built-in defaults to stdout:
nipper serve --dump-config > gateway.yaml
nipper agent --dump-config > agent.yaml

# 3. Start the stack
docker compose up -d

# 4. Check logs
docker compose logs -f
```

## Configuration

### Environment Files

Each component has its own `.env` file to separate secrets:

| File           | Used by   | Key variables                                          |
|----------------|-----------|--------------------------------------------------------|
| `.env.caddy`   | caddy     | `NIPPER_HOSTNAME`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, `AWS_HOSTED_ZONE_ID` |
| `.env.gateway` | gateway   | `RABBITMQ_USERNAME`, `RABBITMQ_PASSWORD`, `RABBITMQ_MGMT_USERNAME`, `RABBITMQ_MGMT_PASSWORD`, `ADMIN_API_TOKEN` |
| `.env.agent`   | agent     | `NIPPER_GATEWAY_URL`, `NIPPER_AUTH_TOKEN`, `OPENAI_API_KEY`, `INFERENCE_BASE_URL` |

### Config Files

`gateway.yaml` and `agent.yaml` are mounted read-only into the containers. To regenerate from defaults:

```bash
# From the project root (requires the nipper binary):
go run ./cmd/nipper serve --dump-config > deploy/docker-compose/gateway.yaml
go run ./cmd/nipper agent --dump-config > deploy/docker-compose/agent.yaml
```

Or, if you have a pre-built binary:

```bash
nipper serve --dump-config > deploy/docker-compose/gateway.yaml
nipper agent --dump-config > deploy/docker-compose/agent.yaml
```

### Caddy TLS

Caddy uses the Route53 DNS-01 challenge for Let's Encrypt certificate issuance. The `Dockerfile.caddy` bundles the `caddy-dns/route53` plugin.

- Port 80 redirects to HTTPS (443) automatically
- The admin API is exposed on port 18790 via HTTPS
- Edit the `Caddyfile` to customize routing

### Durable Storage

Each service has a named Docker volume for persistent data:

| Volume          | Service  | Contents                           |
|-----------------|----------|------------------------------------|
| `caddy_data`    | caddy    | TLS certificates                   |
| `caddy_config`  | caddy    | Caddy configuration state          |
| `rabbitmq_data` | rabbitmq | Message queues, exchanges, users   |
| `gateway_data`  | gateway  | SQLite database, backups           |
| `agent_data`    | agent    | Session state, memory files        |

### Agent Sandbox (Docker-in-Docker)

The agent container mounts `/var/run/docker.sock` from the host. This allows the agent to create sandboxed Docker containers for bash tool execution. The user running `docker compose` must have Docker daemon access.

The sandbox is configured in `agent.yaml` under `agent.sandbox`:

```yaml
agent:
  sandbox:
    enabled: true
    image: ubuntu:noble
    memory_limit_mb: 2048
    cpu_limit: 2.0
    timeout_seconds: 120
    network_enabled: true
```

## Provisioning an Agent

After the stack is running, provision a user and agent via the admin API:

```bash
# Bootstrap creates a user + agent in one step
docker compose exec gateway nipper admin bootstrap \
  --user-id "user1" \
  --display-name "My User" \
  --channel whatsapp \
  --channel-id "+1234567890"

# The output includes the agent token (npr_...).
# Set it in .env.agent as NIPPER_AUTH_TOKEN, then restart the agent:
docker compose restart agent
```

## Scaling Agents

To run multiple agents, add additional agent services in `docker-compose.yml` or use `docker compose up --scale agent=N`. Each agent needs its own `.env.agent` with a unique `NIPPER_AUTH_TOKEN`.

## Monitoring

- **Gateway metrics**: `https://<hostname>/metrics` (Prometheus format)
- **Agent metrics**: exposed on port 9091 inside the container (uncomment the Caddyfile block to expose externally)
- **RabbitMQ Management**: uncomment the port mapping in `docker-compose.yml` to access at `http://<host>:15672`

## Troubleshooting

```bash
# View logs for a specific service
docker compose logs -f gateway
docker compose logs -f agent

# Shell into the gateway container
docker compose exec gateway sh

# Check RabbitMQ connectivity
docker compose exec gateway wget -q -O- http://rabbitmq:15672/api/overview

# Verify TLS certificate
curl -vI https://nipper.example.com

# Re-provision after config changes
docker compose down
docker compose up -d
```
