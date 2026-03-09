package agent

import (
	"strings"
	"unicode"
)

// topicChangeThreshold is the minimum Jaccard distance (1 - similarity) between
// the recent conversation and the new message to consider it a topic change.
// A value of 0.85 means less than 15% word overlap triggers the suggestion.
const topicChangeThreshold = 0.92

// minHistoryForTopicDetection is the minimum number of transcript lines needed
// before topic change detection kicks in. With too few messages, everything
// looks like a topic change.
const minHistoryForTopicDetection = 4

// recentWindowForTopicDetection is how many recent user messages to consider
// when building the "current topic" word set.
const recentWindowForTopicDetection = 6

// detectTopicChange returns true and a suggestion message when the user's new
// message appears to be about a completely different topic than the recent
// conversation. Returns false, "" when the conversation seems continuous.
func detectTopicChange(recentHistory []string, newMessage string) (bool, string) {
	if len(recentHistory) < minHistoryForTopicDetection {
		return false, ""
	}

	newWords := extractWords(newMessage)
	if len(newWords) < 3 {
		// Very short messages (greetings, "ok", "thanks") shouldn't trigger.
		return false, ""
	}

	// Build word set from recent history.
	historyWords := make(map[string]struct{})
	start := 0
	if len(recentHistory) > recentWindowForTopicDetection {
		start = len(recentHistory) - recentWindowForTopicDetection
	}
	for _, line := range recentHistory[start:] {
		for w := range extractWords(line) {
			historyWords[w] = struct{}{}
		}
	}

	if len(historyWords) == 0 {
		return false, ""
	}

	// Jaccard distance: 1 - |intersection| / |union|
	intersection := 0
	for w := range newWords {
		if _, ok := historyWords[w]; ok {
			intersection++
		}
	}
	union := len(historyWords)
	for w := range newWords {
		if _, ok := historyWords[w]; !ok {
			union++
		}
	}

	if union == 0 {
		return false, ""
	}

	distance := 1.0 - float64(intersection)/float64(union)
	if distance >= topicChangeThreshold {
		return true, "It looks like you're switching topics. Consider sending `/new` to start a fresh context for better results."
	}
	return false, ""
}

// extractWords tokenizes text into a set of lowercase words, filtering out
// stop words and very short tokens.
func extractWords(text string) map[string]struct{} {
	words := make(map[string]struct{})
	for _, raw := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(raw) <= 2 || stopWords[raw] {
			continue
		}
		words[raw] = struct{}{}
	}
	return words
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true,
	"not": true, "you": true, "all": true, "can": true, "had": true,
	"her": true, "was": true, "one": true, "our": true, "out": true,
	"has": true, "have": true, "been": true, "would": true, "could": true,
	"that": true, "this": true, "with": true, "from": true, "they": true,
	"will": true, "what": true, "when": true, "make": true, "like": true,
	"just": true, "know": true, "take": true, "come": true,
	"than": true, "them": true, "very": true, "some": true, "into": true, "does": true,
	"also": true, "about": true, "which": true, "their": true, "there": true,
	"other": true, "these": true, "after": true, "should": true, "where": true,
	// Spanish stop words
	"que": true, "los": true, "las": true, "del": true, "por": true,
	"una": true, "con": true, "para": true, "como": true, "pero": true,
	"más": true, "este": true, "esta": true, "esto": true, "eso": true,
}
