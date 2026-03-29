# Embedding Test Tool

A CLI tool for validating embedding accuracy against your MCP tool catalog. Use it to:

- Compare embedding models before deploying
- Tune the similarity threshold
- Verify multilingual and semantic matching
- Catch regressions when changing the catalog or model

## Build

```bash
go build -o embedding-test ./cmd/embedding-test
```

## Usage

The tool has two modes: **batch test suite** and **single query**.

### Batch test suite

Run a full set of test cases and get pass/fail results:

```bash
./embedding-test \
  --base-url https://embeddings.identitylabs.mx/v1 \
  --model embeddinggemma:300m \
  --catalog cmd/embedding-test/catalog.example.yaml \
  --tests cmd/embedding-test/tests.example.yaml \
  --threshold 0.3
```

Output:

```
━━━ Spanish: turn on the light
    Intent: "enciende la luz"
    Rank  Tool          Score   Status
    1     HassLightSet  0.4416  ✓ expected
    2     HassTurnOff   0.4105  △ above threshold
    3     HassTurnOn    0.3485  ✓ expected
    ...
    PASS

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Results: 19/20 passed, 1 failed (threshold: 0.30)
```

Exit code is 1 if any test fails (useful for CI).

### Single query

Explore how a specific intent ranks against the catalog:

```bash
./embedding-test \
  --base-url https://embeddings.identitylabs.mx/v1 \
  --model embeddinggemma:300m \
  --catalog cmd/embedding-test/catalog.example.yaml \
  --query "enciende la luz" \
  --threshold 0.3
```

Output:

```
Query: "enciende la luz"
Threshold: 0.30

Rank  Tool                         Score   Match
1     HassLightSet                 0.4416  YES
2     HassTurnOff                  0.4105  YES
3     HassTurnOn                   0.3485  YES
4     GetLiveContext               0.3235  YES
5     create_folder                0.2912
6     get_gmail_unsubscribe_links  0.2751
...

4 tools above threshold 0.30
```

## Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--base-url` | yes | | Embedding API base URL (e.g. `http://localhost:11434/v1`) |
| `--model` | yes | | Embedding model name (e.g. `embeddinggemma:300m`) |
| `--catalog` | yes | | Path to tool catalog YAML |
| `--tests` | no* | | Path to test cases YAML (batch mode) |
| `--query` | no* | | Single intent string (query mode) |
| `--threshold` | no | `0.3` | Minimum cosine similarity to consider a match |
| `--api-key` | no | | API key for the embedding endpoint |
| `--top` | no | `10` | Number of top results to show per query |

*One of `--tests` or `--query` is required.

## File formats

### Catalog YAML

Mirrors what the agent sees via `mcpLoader.ToolInfos()`. Update this when you add or change MCP servers.

```yaml
tools:
  - name: HassTurnOn
    description: "Turns on, opens, or presses a device or entity"
    server_name: home-assistant
    tags: [light, switch, plug, fan]

  - name: search_gmail_messages
    description: "Search Gmail messages by query"
    server_name: google-workspace
    tags: [email, inbox]
```

See `cmd/embedding-test/catalog.example.yaml` for a full example.

### Test cases YAML

Each test defines an intent and the tools that should (or should not) match:

```yaml
tests:
  - intent: "enciende la luz"
    description: "Spanish: turn on the light"
    expect_tools: [HassTurnOn, HassLightSet]   # MUST be above threshold
    reject_tools: [search_notes, manage_event]  # must NOT be above threshold

  - intent: "tell me a joke"
    description: "Negative: nothing should match"
    expect_tools: []  # empty = negative case
```

See `cmd/embedding-test/tests.example.yaml` for a full example.

### Test result statuses

| Status | Meaning |
|--------|---------|
| `✓ expected` | Tool is in `expect_tools` and above threshold |
| `✗ MISS` | Tool is in `expect_tools` but below threshold (test fails) |
| `✗ FALSE POSITIVE` | Tool is in `reject_tools` but above threshold (test fails) |
| `△ above threshold` | Tool is above threshold but not in expect/reject lists |

## Comparing models

Run the same test suite with different models to compare accuracy:

```bash
# Ollama models
for model in embeddinggemma:300m nomic-embed-text all-minilm; do
  echo "=== $model ==="
  ./embedding-test \
    --base-url http://localhost:11434/v1 \
    --model "$model" \
    --catalog catalog.yaml \
    --tests tests.yaml
  echo
done
```

## Tuning the threshold

```bash
# Try different thresholds
for t in 0.2 0.25 0.3 0.35 0.4; do
  echo "=== threshold=$t ==="
  ./embedding-test \
    --base-url http://localhost:11434/v1 \
    --model embeddinggemma:300m \
    --catalog catalog.yaml \
    --tests tests.yaml \
    --threshold "$t" 2>&1 | tail -2
  echo
done
```

Lower threshold = more matches (risk of noise). Higher threshold = stricter (risk of missing tools). The hybrid matcher compensates — keyword matching covers what embeddings miss.

## Adding to your catalog

When you add a new MCP server or tool:

1. Add entries to your `catalog.yaml`
2. Add test cases to your `tests.yaml` (include multilingual variants)
3. Run the suite to verify the model handles it
4. If a tool consistently misses, add semantic tags to improve matching
