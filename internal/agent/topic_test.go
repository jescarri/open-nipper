package agent

import "testing"

func TestDetectTopicChange_TooFewMessages(t *testing.T) {
	changed, _ := detectTopicChange([]string{"hello", "hi"}, "something totally different about quantum physics")
	if changed {
		t.Error("should not detect topic change with too few messages")
	}
}

func TestDetectTopicChange_SameTopic(t *testing.T) {
	history := []string{
		"Can you help me with my Go code?",
		"I need to fix a bug in the handler",
		"The function returns an error when parsing JSON",
		"Let me check the error handling in the parser",
	}
	changed, _ := detectTopicChange(history, "What about the JSON marshaling in that same function?")
	if changed {
		t.Error("should not detect topic change for same topic")
	}
}

func TestDetectTopicChange_DifferentTopic(t *testing.T) {
	history := []string{
		"Can you help me with my Go code?",
		"I need to fix a bug in the handler",
		"The function returns an error when parsing JSON",
		"Let me check the error handling in the parser",
	}
	changed, msg := detectTopicChange(history, "What is the weather forecast for Toronto this weekend? I want to plan a hiking trip to the mountains.")
	if !changed {
		t.Error("should detect topic change for completely different topic")
	}
	if msg == "" {
		t.Error("expected a suggestion message")
	}
}

func TestDetectTopicChange_ShortMessage(t *testing.T) {
	history := []string{
		"Tell me about the solar system",
		"What about Mars?",
		"How far is it from Earth?",
		"What is its atmosphere like?",
	}
	// Very short messages like "ok" should not trigger
	changed, _ := detectTopicChange(history, "ok thanks")
	if changed {
		t.Error("short messages should not trigger topic change")
	}
}

func TestExtractWords(t *testing.T) {
	words := extractWords("Hello, World! This is a test of word extraction.")
	if _, ok := words["hello"]; !ok {
		t.Error("expected 'hello' in words")
	}
	if _, ok := words["the"]; ok {
		t.Error("'the' should be filtered as stop word")
	}
	if _, ok := words["is"]; ok {
		t.Error("'is' should be filtered (too short)")
	}
}
