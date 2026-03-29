// Command embedding-test validates embedding accuracy for MCP tool matching.
//
// It loads a tool catalog and test cases from YAML files, embeds them via any
// OpenAI-compatible endpoint, computes cosine similarity, and reports pass/fail
// results. Supports both batch test suites and ad-hoc single queries.
//
// See docs/EMBEDDING_TEST.md for usage and examples.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/jescarri/open-nipper/internal/agent/tools"
	"gopkg.in/yaml.v3"
)

// catalogFile is the YAML structure for the tool catalog.
type catalogFile struct {
	Tools []catalogEntry `yaml:"tools"`
}

type catalogEntry struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	ServerName  string   `yaml:"server_name"`
	Tags        []string `yaml:"tags"`
}

// testFile is the YAML structure for test cases.
type testFile struct {
	Tests []testCase `yaml:"tests"`
}

type testCase struct {
	Intent      string   `yaml:"intent"`
	ExpectTools []string `yaml:"expect_tools"`
	RejectTools []string `yaml:"reject_tools"`
	Description string   `yaml:"description"`
}

func main() {
	baseURL := flag.String("base-url", "", "embedding API base URL (required)")
	model := flag.String("model", "", "embedding model name (required)")
	apiKey := flag.String("api-key", "", "API key (optional)")
	threshold := flag.Float64("threshold", 0.3, "similarity threshold (0.0–1.0)")
	catalogPath := flag.String("catalog", "", "path to tool catalog YAML (required)")
	testsPath := flag.String("tests", "", "path to test cases YAML (run test suite)")
	query := flag.String("query", "", "ad-hoc query (show similarity scores for a single intent)")
	topN := flag.Int("top", 10, "number of top results to show per query")
	flag.Parse()

	if *baseURL == "" || *model == "" || *catalogPath == "" {
		fmt.Fprintln(os.Stderr, "error: --base-url, --model, and --catalog are required")
		fmt.Fprintln(os.Stderr)
		flag.Usage()
		os.Exit(1)
	}
	if *testsPath == "" && *query == "" {
		fmt.Fprintln(os.Stderr, "error: provide --tests (batch mode) or --query (single query mode)")
		fmt.Fprintln(os.Stderr)
		flag.Usage()
		os.Exit(1)
	}

	// Load catalog.
	catalog, err := loadCatalog(*catalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading catalog: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	embedder := tools.NewOpenAIEmbedder(*baseURL, *model, *apiKey)

	// Ping.
	fmt.Printf("Pinging %s (model: %s)...\n", *baseURL, *model)
	if err := embedder.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: ping failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK")
	fmt.Println()

	// Index catalog.
	catalogVecs, err := indexCatalog(ctx, embedder, catalog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: indexing catalog: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Indexed %d tools (%d dimensions)\n\n", len(catalog), len(catalogVecs[0]))

	if *query != "" {
		// Single query mode.
		runSingleQuery(ctx, embedder, catalog, catalogVecs, *query, *threshold, *topN)
	} else {
		// Batch test mode.
		cases, err := loadTests(*testsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: loading tests: %v\n", err)
			os.Exit(1)
		}
		ok := runTestSuite(ctx, embedder, catalog, catalogVecs, cases, *threshold, *topN)
		if !ok {
			os.Exit(1)
		}
	}
}

// ---------- Catalog & test loading ----------

func loadCatalog(path string) ([]tools.ToolCatalogEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f catalogFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	entries := make([]tools.ToolCatalogEntry, len(f.Tools))
	for i, t := range f.Tools {
		entries[i] = tools.ToolCatalogEntry{
			Name:        t.Name,
			Description: t.Description,
			ServerName:  t.ServerName,
			Tags:        t.Tags,
		}
	}
	return entries, nil
}

func loadTests(path string) ([]testCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f testFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return f.Tests, nil
}

// ---------- Embedding ----------

func indexCatalog(ctx context.Context, embedder *tools.OpenAIEmbedder, catalog []tools.ToolCatalogEntry) ([][]float64, error) {
	texts := make([]string, len(catalog))
	for i, e := range catalog {
		texts[i] = e.Name + ": " + e.Description
		if len(e.Tags) > 0 {
			texts[i] += " " + strings.Join(e.Tags, " ")
		}
	}
	return embedder.EmbedStrings(ctx, texts)
}

type scored struct {
	name  string
	score float64
}

func scoreIntent(ctx context.Context, embedder *tools.OpenAIEmbedder, catalog []tools.ToolCatalogEntry, catalogVecs [][]float64, intent string) ([]scored, error) {
	intentVecs, err := embedder.EmbedStrings(ctx, []string{intent})
	if err != nil {
		return nil, err
	}
	intentVec := intentVecs[0]

	scores := make([]scored, len(catalog))
	for i, e := range catalog {
		scores[i] = scored{name: e.Name, score: cosineSimilarity(intentVec, catalogVecs[i])}
	}
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})
	return scores, nil
}

// ---------- Single query mode ----------

func runSingleQuery(ctx context.Context, embedder *tools.OpenAIEmbedder, catalog []tools.ToolCatalogEntry, catalogVecs [][]float64, query string, threshold float64, topN int) {
	fmt.Printf("Query: %q\n", query)
	fmt.Printf("Threshold: %.2f\n\n", threshold)

	scores, err := scoreIntent(ctx, embedder, catalog, catalogVecs, query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	limit := topN
	if len(scores) < limit {
		limit = len(scores)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Rank\tTool\tScore\tMatch\n")
	fmt.Fprintf(w, "----\t----\t-----\t-----\n")
	matched := 0
	for i := 0; i < limit; i++ {
		s := scores[i]
		match := ""
		if s.score >= threshold {
			match = "YES"
			matched++
		}
		fmt.Fprintf(w, "%d\t%s\t%.4f\t%s\n", i+1, s.name, s.score, match)
	}
	w.Flush()

	fmt.Printf("\n%d tools above threshold %.2f\n", matched, threshold)
}

// ---------- Batch test mode ----------

func runTestSuite(ctx context.Context, embedder *tools.OpenAIEmbedder, catalog []tools.ToolCatalogEntry, catalogVecs [][]float64, cases []testCase, threshold float64, topN int) bool {
	passed, failed, total := 0, 0, 0

	for _, tc := range cases {
		total++
		fmt.Printf("━━━ %s\n", tc.Description)
		fmt.Printf("    Intent: %q\n", tc.Intent)

		scores, err := scoreIntent(ctx, embedder, catalog, catalogVecs, tc.Intent)
		if err != nil {
			fmt.Printf("    ERROR: %v\n\n", err)
			failed++
			continue
		}

		// Print top N.
		limit := topN
		if len(scores) < limit {
			limit = len(scores)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "    Rank\tTool\tScore\tStatus\n")
		fmt.Fprintf(w, "    ----\t----\t-----\t------\n")
		for i := 0; i < limit; i++ {
			s := scores[i]
			status := ""
			if contains(tc.ExpectTools, s.name) && s.score >= threshold {
				status = "✓ expected"
			} else if contains(tc.ExpectTools, s.name) && s.score < threshold {
				status = "✗ MISS (expected but below threshold)"
			} else if contains(tc.RejectTools, s.name) && s.score >= threshold {
				status = "✗ FALSE POSITIVE"
			} else if s.score >= threshold {
				status = "△ above threshold"
			}
			fmt.Fprintf(w, "    %d\t%s\t%.4f\t%s\n", i+1, s.name, s.score, status)
		}
		w.Flush()

		// Evaluate pass/fail.
		aboveThreshold := make(map[string]bool)
		for _, s := range scores {
			if s.score >= threshold {
				aboveThreshold[s.name] = true
			}
		}

		ok := true
		if len(tc.ExpectTools) == 0 {
			// Negative case: warn if top score is suspiciously high.
			if len(scores) > 0 && scores[0].score >= threshold+0.15 {
				fmt.Printf("    WARN: top score %.4f is high for a negative case\n", scores[0].score)
			}
		} else {
			for _, exp := range tc.ExpectTools {
				if !aboveThreshold[exp] {
					fmt.Printf("    MISS: %s not above threshold %.2f\n", exp, threshold)
					ok = false
				}
			}
		}
		for _, rej := range tc.RejectTools {
			if aboveThreshold[rej] {
				fmt.Printf("    FALSE POSITIVE: %s should not match\n", rej)
				ok = false
			}
		}

		if ok {
			fmt.Println("    PASS")
			passed++
		} else {
			fmt.Println("    FAIL")
			failed++
		}
		fmt.Println()
	}

	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("Results: %d/%d passed, %d failed (threshold: %.2f)\n", passed, total, failed, threshold)
	if failed > 0 {
		fmt.Println("Try adjusting --threshold or switching --model")
	}
	return failed == 0
}

// ---------- Math helpers ----------

func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
