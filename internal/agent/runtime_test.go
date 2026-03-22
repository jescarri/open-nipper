package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/jescarri/open-nipper/internal/agent/skills"
	"github.com/jescarri/open-nipper/internal/models"
)

func TestIsMCPTransportError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "transport has been closed",
			err:  errors.New("failed to call mcp tool: transport error: transport has been closed"),
			want: true,
		},
		{
			name: "wrapped transport error",
			err:  errors.New("[NodeRunError] failed to invoke tool[name:GetLiveContext id:736747418]: failed to call mcp tool: transport error: transport has been closed\n------------------------\nnode path: [tools]"),
			want: true,
		},
		{
			name: "generic transport error",
			err:  errors.New("transport error: something went wrong"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMCPTransportError(tt.err)
			if got != tt.want {
				t.Errorf("isMCPTransportError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "429 in message",
			err:  errors.New("error, status code: 429, status: Too Many Requests, message: Rate limit exceeded"),
			want: true,
		},
		{
			name: "rate limit phrase",
			err:  errors.New("rate limit exceeded for model"),
			want: true,
		},
		{
			name: "too many requests",
			err:  errors.New("too many requests, please try again later"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRateLimitError(tt.err)
			if got != tt.want {
				t.Errorf("isRateLimitError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestStripThinkTags(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantCleaned    string
		wantThinking   string
		hasThinking    bool
	}{
		{
			name:        "no think tags",
			input:       "Hello world",
			wantCleaned: "Hello world",
			hasThinking: false,
		},
		{
			name:        "single think block",
			input:       "<think>reasoning here</think>Answer here",
			wantCleaned: "Answer here",
			hasThinking: true,
		},
		{
			name:        "multiple think blocks",
			input:       "<think>first thought</think>middle text<think>second thought</think>final text",
			wantCleaned: "middle textfinal text",
			hasThinking: true,
		},
		{
			name:        "unclosed think tag",
			input:       "before<think>thinking forever...",
			wantCleaned: "before",
			hasThinking: true,
		},
		{
			name:        "think block at start with whitespace",
			input:       "  <think>\n  let me think about this\n</think>\n\nHere is my answer.",
			wantCleaned: "Here is my answer.",
			hasThinking: true,
		},
		{
			name:        "empty think block",
			input:       "<think></think>Content",
			wantCleaned: "Content",
			hasThinking: false, // empty thinking after TrimSpace
		},
		{
			name:        "think block with nested angle brackets",
			input:       "<think>if x > 3 and y < 5 then</think>Result",
			wantCleaned: "Result",
			hasThinking: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaned, thinking := stripThinkTags(tt.input)
			if cleaned != tt.wantCleaned {
				t.Errorf("cleaned = %q, want %q", cleaned, tt.wantCleaned)
			}
			if tt.hasThinking && thinking == "" {
				t.Error("expected non-empty thinking, got empty")
			}
			if !tt.hasThinking && thinking != "" {
				t.Errorf("expected empty thinking, got %q", thinking)
			}
		})
	}
}

func TestIsGarbledOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "empty string",
			input: "",
			want:  false,
		},
		{
			name:  "normal response",
			input: "Here is a helpful answer to your question about Go programming.",
			want:  false,
		},
		{
			name:  "short response",
			input: "Yes",
			want:  false,
		},
		{
			name:  "mostly ellipsis and dots",
			input: "… … … ...\n\n…  ...\n\n...\n\n...\n\n...\n\n...\n\n...\n\n—",
			want:  true,
		},
		{
			name:  "garbled with few words and lots of filler",
			input: "**?\n\n… … ...\n\n…  ...\n\n...\n\n...\n\n...\n\n...\n\n...\n\n...\n\n...\n\n...\n\n—",
			want:  true,
		},
		{
			name:  "repeated asterisks",
			input: "* * * * * * * * * * * * * * * * * * * * * * * * * * * * * * * * * *",
			want:  true,
		},
		{
			name:  "replacement characters",
			input: "Hello \uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD world",
			want:  true,
		},
		{
			name:  "normal markdown with bullet points",
			input: "Here are some tips:\n- First tip\n- Second tip\n- Third tip\n\nHope that helps!",
			want:  false,
		},
		{
			name:  "code block response",
			input: "```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```",
			want:  false,
		},
		{
			name:  "dots and dashes only",
			input: "... ... ... ... ... ... ... ... --- --- --- ...",
			want:  true,
		},
		{
			name:  "zero-width spaces and word joiners (invisible garble)",
			input: "*\u200B*\u200B \u2060… \u200B… \u200B*\n*\u200B*\n\n…\n\n•\u2060 \u2060*\n…\n… \n…\n…\n\n * *\n\u200B\n…\n \n…\n…\n\n... *—\u2011 *",
			want:  true,
		},
		{
			name:  "garbled prefix with valid suffix",
			input: "*\u200B*\u200B \u2060… … … *  \n*\u200B*\n*\u200B*\n\n…\n\n•\u2060 \u2060*\n…\n… \n…\n…\n\n * *\n\u200B\n…\n \n…\n…\n\n... *—\u2011 *\n\u200B\n*\u200B *\n*\u200B *\n! **… ? ? ?? ... \n\n**… … ... \n\n… dis\u200B\n\nThe *\u200B*\n\n✂️ *Ralph, the office air extractor is now OFF.\n\n✅ Switch \"office air extractor\" turned off.\n\nAnything else you want me to mess with? 🍸🚀",
			want:  true,
		},
		{
			name: "garbled filler with scattered words and reasoning narration leak",
			input: "Turned ?... uh… *……  …  ...  ... … …\n\nOops! Looks *…  ... *... \n\n... ?\n\nOops… ……\n\nLooks ...?\n\n?\n\nSorry ...?\n\n... \n\nWe got **……\u200B\u200B\u200B\n\n... \n\n... \n\n...\n\n???\n\n... \n\n…\n\n\u200B\n\nThe answer??…\n\nOops…  … …\n\nOops… wh…\n\n... \n\n... \n\n...\n\n… \n\n!\n\n...\n\n… \n\n...\n\n? \n\n…\n\n… ??\n\n... \n\n… \n\nWe … \n\nWe ... \n\nIt looks ...? \n\n... \n\nWe ... \n\n…\n\n…\n\n?\n\n...\n\n… …\n\n… ?\n\n… …\n\n... \n\n... \n\n…\n\n…\n\n...\n\n...\n\n...\n\n...\n\n…\n\n… \n\n…\n\n…\n\nThe \n\n\u200B\n\nSorry… \n\n?\n\nWe ... \n\n… \n\n... \n\n... \n\n... \n\nThe ……\n\n? \n\n...\n\nThe user sent an audio message: \"Hey, turn off the office lights.\"\n\nWe turned off using HassTurnOff. The tool responded success.\n\nNow we need to reply in WhatsApp style.\n\nOffice lights turned off ✅\n\nAll set, Ralph. The office is now in the dark. 🍻🕶️\n\n—",
			want: true,
		},
		{
			name:  "long normal response should not be flagged",
			input: "The quick brown fox jumps over the lazy dog. This is a perfectly normal response with enough length to exercise the first-half check. It contains meaningful words, punctuation, and proper grammar throughout.",
			want:  false,
		},
		{
			name:  "gpt-oss garbled with reasoning leak and valid tail",
			input: "**bold**...oops! Looks \u2026 \u2026 ... \u2026 ...\n\n\n\nOops! **...**\n\n\n\n?\n\n\n\nOops\u2026 \n\n\n\n...\n\n\n\n...\n\n\n\n?\n\n\n\n... \n\nSorry\u2026 \n\nWe **...** \n\nWe\u2026 \n\n\u2026\n\n\n--- \n\nThe \n\n\nWe \n\n\n\u2026\n\n\u200B\n\nIt seems the tool call succeeded but the response wasn't properly formatted. I need to inform the user that the office lights have been turned off. Use WhatsApp style, emojis, etc.*turning off the office lights* \U0001f4a1\U0001f526\n\nDone! The office is now as dark as a robot's soul after a night of cheap whiskey. \U0001f37b\U0001f5a4",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isGarbledOutput(tt.input)
			if got != tt.want {
				t.Errorf("isGarbledOutput(%q) = %v, want %v", tt.input[:min(len(tt.input), 50)], got, tt.want)
			}
		})
	}
}

func TestIsSpecialTokenLeakage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "channel token in error",
			err:  errors.New("[NodeRunError] failed to receive stream chunk: error, Failed to parse input at pos 0: <|channel|>analysis<|message|>The user gave a YouTube URL"),
			want: true,
		},
		{
			name: "im_start token",
			err:  errors.New("Failed to parse input at pos 0: <|im_start|>assistant"),
			want: true,
		},
		{
			name: "end token only",
			err:  errors.New("parse error near <|end|>"),
			want: true,
		},
		{
			name: "generic parse failure with special token marker",
			err:  errors.New("Failed to parse input at pos 5: <|unknown_token|>"),
			want: true,
		},
		{
			name: "normal parse error without special tokens",
			err:  errors.New("Failed to parse input at pos 0: {invalid json}"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSpecialTokenLeakage(tt.err)
			if got != tt.want {
				t.Errorf("isSpecialTokenLeakage(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestStripChatTemplateTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no template tokens",
			input: "Hello, this is a normal response.",
			want:  "Hello, this is a normal response.",
		},
		{
			name:  "single channel token",
			input: "<|channel|>analysis<|message|>The user wants help",
			want:  "analysisThe user wants help",
		},
		{
			name:  "gpt-oss leaked tokens",
			input: "<|channel|>analysis<|message|>The user provided a YouTube link. We need to use yt_summary skill via skill_exec.<|end|><|start|>assistant<|channel|>commentary to=functions skill_exec",
			want:  "analysisThe user provided a YouTube link. We need to use yt_summary skill via skill_exec.assistantcommentary to=functions skill_exec",
		},
		{
			name:  "im_start and im_end tokens",
			input: "<|im_start|>assistant\nHere is my answer<|im_end|>",
			want:  "assistant\nHere is my answer",
		},
		{
			name:  "template tokens mixed in normal text",
			input: "Before <|end|> after",
			want:  "Before  after",
		},
		{
			name:  "unclosed marker",
			input: "Normal text <| incomplete marker without closing",
			want:  "Normal text <| incomplete marker without closing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripChatTemplateTokens(tt.input)
			if got != tt.want {
				t.Errorf("stripChatTemplateTokens() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatchSkillsByMessage(t *testing.T) {
	allSkills := []skills.Skill{
		{Name: "summarize_url", Config: &skills.SkillConfig{Keywords: []string{"http://", "https://", "url", "link", "summarize", "summarise", "save", "reading list", "bookmark"}}},
		{Name: "yt_summary", Config: &skills.SkillConfig{Keywords: []string{"youtube", "youtu.be", "video", "yt", "transcript", "captions"}}},
		{Name: "plant-care", Config: &skills.SkillConfig{Keywords: []string{"plant", "soil", "moisture", "water", "garden", "lawn", "watering"}}},
		{Name: "home-devices", Config: &skills.SkillConfig{Keywords: []string{"light", "lights", "switch", "plug", "fan", "device", "turn on", "turn off", "toggle", "lamp"}}},
		{Name: "unknown-skill"}, // no keywords — should be skipped
	}

	tests := []struct {
		name    string
		msg     *models.NipperMessage
		want    []string
		wantNil bool // only true when msg is nil (matching not attempted)
	}{
		{
			name:    "nil message",
			msg:     nil,
			wantNil: true,
		},
		{
			name: "empty text",
			msg:  &models.NipperMessage{Content: models.MessageContent{Text: ""}},
			want: []string{}, // non-nil empty: matching attempted, nothing matched
		},
		{
			name: "matches plant-care by keyword",
			msg:  &models.NipperMessage{Content: models.MessageContent{Text: "How often should I water my plant?"}},
			want: []string{"plant-care"},
		},
		{
			name: "matches yt_summary by URL in parts",
			msg: &models.NipperMessage{Content: models.MessageContent{
				Text: "summarize this",
				Parts: []models.ContentPart{
					{URL: "https://youtube.com/watch?v=abc123"},
				},
			}},
			want: []string{"summarize_url", "yt_summary"},
		},
		{
			name: "matches home-devices",
			msg:  &models.NipperMessage{Content: models.MessageContent{Text: "turn on the lights"}},
			want: []string{"home-devices"},
		},
		{
			name: "matches summarize_url by https",
			msg:  &models.NipperMessage{Content: models.MessageContent{Text: "check out https://example.com"}},
			want: []string{"summarize_url"},
		},
		{
			name: "no keyword match",
			msg:  &models.NipperMessage{Content: models.MessageContent{Text: "what is the meaning of life?"}},
			want: []string{}, // non-nil empty: matching attempted, nothing matched
		},
		{
			name: "case insensitive",
			msg:  &models.NipperMessage{Content: models.MessageContent{Text: "PLANT moisture level?"}},
			want: []string{"plant-care"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchSkillsByMessage(allSkills, tt.msg)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Errorf("got[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

func TestMessageHasImages(t *testing.T) {
	tests := []struct {
		name string
		msg  *models.NipperMessage
		want bool
	}{
		{
			name: "nil message",
			msg:  nil,
			want: false,
		},
		{
			name: "no parts",
			msg:  &models.NipperMessage{Content: models.MessageContent{Text: "hello"}},
			want: false,
		},
		{
			name: "text parts only",
			msg: &models.NipperMessage{Content: models.MessageContent{
				Parts: []models.ContentPart{{Type: "text", Text: "hi"}},
			}},
			want: false,
		},
		{
			name: "image type part",
			msg: &models.NipperMessage{Content: models.MessageContent{
				Parts: []models.ContentPart{{Type: "image", URL: "https://example.com/img.jpg"}},
			}},
			want: true,
		},
		{
			name: "image mime type",
			msg: &models.NipperMessage{Content: models.MessageContent{
				Parts: []models.ContentPart{{Type: "document", MimeType: "image/png", URL: "https://example.com/img.png"}},
			}},
			want: true,
		},
		{
			name: "audio part not image",
			msg: &models.NipperMessage{Content: models.MessageContent{
				Parts: []models.ContentPart{{Type: "audio", MimeType: "audio/ogg"}},
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messageHasImages(tt.msg)
			if got != tt.want {
				t.Errorf("messageHasImages() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSalvageCleanSuffix(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantNil bool // true if we expect ""
	}{
		{
			name:    "empty content",
			input:   "",
			wantNil: true,
		},
		{
			name:  "clean content passthrough",
			input: "Office lights turned off ✅\n\nAll set, Ralph.",
			want:  "Office lights turned off ✅\n\nAll set, Ralph.",
		},
		{
			name:  "garbled prefix with clean answer at tail",
			input: "?... uh… *……  …  ...  ... … …\n\n... ?\n\nOops… ……\n\n... \n\n…\n\nOffice lights turned off ✅\n\nAll set, Ralph. The office is now in the dark. 🍻🕶️",
			want:  "Office lights turned off ✅\n\nAll set, Ralph. The office is now in the dark. 🍻🕶️",
		},
		{
			name:  "garbled prefix + reasoning narration + clean answer",
			input: "?... *…… …\n\nThe user sent an audio message.\n\nWe turned off using HassTurnOff.\n\nNow we need to reply in WhatsApp style.\n\nOffice lights turned off ✅\n\nAll set, Ralph.",
			want:  "Office lights turned off ✅\n\nAll set, Ralph.",
		},
		{
			name:    "all garbled no clean suffix",
			input:   "… … … ...\n\n…  ...\n\n...\n\n...\n\n...",
			wantNil: true,
		},
		{
			name:    "clean suffix too short",
			input:   "… … … ...\n\n…  ...\n\n—",
			wantNil: true,
		},
		{
			name:  "gpt-oss garbled with reasoning leak salvages clean tail",
			input: "**bold**...oops! Looks \u2026 \u2026 ... \u2026 ...\n\n\n\nOops! **...**\n\n\n\n?\n\n\n\nOops\u2026 \n\n\n\n...\n\n\n\n...\n\n\n\n?\n\n\n\n... \n\nSorry\u2026 \n\nWe **...** \n\nWe\u2026 \n\n\u2026\n\n\n--- \n\nThe \n\n\nWe \n\n\n\u2026\n\n\u200B\n\nIt seems the tool call succeeded but the response wasn't properly formatted. I need to inform the user that the office lights have been turned off. Use WhatsApp style, emojis, etc.*turning off the office lights* \U0001f4a1\U0001f526\n\nDone! The office is now as dark as a robot's soul after a night of cheap whiskey. \U0001f37b\U0001f5a4",
			want: "Done! The office is now as dark as a robot's soul after a night of cheap whiskey. \U0001f37b\U0001f5a4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := salvageCleanSuffix(tt.input)
			if tt.wantNil {
				if got != "" {
					t.Errorf("salvageCleanSuffix() = %q, want empty", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("salvageCleanSuffix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsReasoningNarration(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"The user sent an audio message.", true},
		{"We need to reply in WhatsApp style.", true},
		{"Now we need to confirm the action.", true},
		{"Let's craft the final answer.", true},
		{"Office lights turned off ✅", false},
		{"All set, Ralph.", false},
		{"Here is the weather forecast.", false},
		{"I turned off the lights for you.", false}, // starts with "I " not "I need"
		{"The tool responded success.", true},
		{"It seems the tool call succeeded but the response wasn't properly formatted.", true},
		{"It looks like the action completed successfully.", true},
		{"It appears that we need to send a confirmation.", true},
		{"The tool call returned a success response.", true},
		{"Use WhatsApp style, emojis, etc.", true},
		{"The user is asking to turn off the lights.", true},
	}

	for _, tt := range tests {
		t.Run(tt.input[:min(len(tt.input), 30)], func(t *testing.T) {
			got := isReasoningNarration(tt.input)
			if got != tt.want {
				t.Errorf("isReasoningNarration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestStripThinkTagsMultipleBlocksConcat(t *testing.T) {
	input := "<think>thought 1</think>text<think>thought 2</think>"
	_, thinking := stripThinkTags(input)
	if !strings.Contains(thinking, "thought 1") || !strings.Contains(thinking, "thought 2") {
		t.Errorf("expected both thoughts in thinking, got: %s", thinking)
	}
}
