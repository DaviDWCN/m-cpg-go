package concept

import (
	"regexp"
	"strings"
)

var stopWords = map[string]bool{
	"a": true, "about": true, "above": true, "after": true, "again": true, "against": true, "all": true,
	"am": true, "an": true, "and": true, "any": true, "are": true, "aren't": true, "as": true, "at": true,
	"be": true, "because": true, "been": true, "before": true, "being": true, "below": true, "between": true,
	"both": true, "but": true, "by": true, "can't": true, "cannot": true, "could": true, "couldn't": true,
	"did": true, "didn't": true, "do": true, "does": true, "doesn't": true, "doing": true, "don't": true,
	"down": true, "during": true, "each": true, "few": true, "for": true, "from": true, "further": true,
	"had": true, "hadn't": true, "has": true, "hasn't": true, "have": true, "haven't": true, "having": true,
	"he": true, "he'd": true, "he'll": true, "he's": true, "her": true, "here": true, "here's": true,
	"hers": true, "herself": true, "him": true, "himself": true, "his": true, "how": true, "how's": true,
	"i": true, "i'd": true, "i'll": true, "i'm": true, "i've": true, "if": true, "in": true, "into": true,
	"is": true, "isn't": true, "it": true, "it's": true, "its": true, "itself": true, "let's": true,
	"me": true, "more": true, "most": true, "mustn't": true, "my": true, "myself": true, "no": true,
	"nor": true, "not": true, "of": true, "off": true, "on": true, "once": true, "only": true, "or": true,
	"other": true, "ought": true, "our": true, "ours": true, "ourselves": true, "out": true, "over": true,
	"own": true, "same": true, "shan't": true, "she": true, "she'd": true, "she'll": true, "she's": true,
	"should": true, "shouldn't": true, "so": true, "some": true, "such": true, "than": true, "that": true,
	"that's": true, "the": true, "their": true, "theirs": true, "them": true, "themselves": true,
	"then": true, "there": true, "there's": true, "these": true, "they": true, "they'd": true,
	"they'll": true, "they're": true, "they've": true, "this": true, "those": true, "through": true,
	"to": true, "too": true, "under": true, "until": true, "up": true, "very": true, "was": true,
	"wasn't": true, "we": true, "we'd": true, "we'll": true, "we're": true, "we've": true, "were": true,
	"weren't": true, "what": true, "what's": true, "when": true, "when's": true, "where": true,
	"where's": true, "which": true, "while": true, "who": true, "who's": true, "whom": true, "why": true,
	"why's": true, "with": true, "won't": true, "would": true, "wouldn't": true, "you": true, "you'd": true,
	"you'll": true, "you're": true, "you've": true, "your": true, "yours": true, "yourself": true,
	"yourselves": true,
	// Programming specific stop words
	"func": true, "return": true, "var": true, "let": true, "const": true, "else": true,
	"switch": true, "case": true, "default": true, "break": true,
	"continue": true, "class": true, "struct": true, "interface": true, "type": true,
}

// ExtractConcepts takes a text string and returns a map of unique concept phrases
// found within it, using a simplified RAKE approach.
func ExtractConcepts(text string) []string {
	// 1. Split text into sentences/clauses by punctuation
	sentenceDelimiters := regexp.MustCompile(`[.,;:?!()\[\]{}"'\n\r\t]+`)
	sentences := sentenceDelimiters.Split(strings.ToLower(text), -1)

	var candidates []string
	wordRegex := regexp.MustCompile(`[a-zA-Z0-9_]+`)

	for _, sentence := range sentences {
		words := wordRegex.FindAllString(sentence, -1)
		if len(words) == 0 {
			continue
		}

		var currentPhrase []string
		for _, word := range words {
			if stopWords[word] || len(word) < 2 {
				// Stop word or single char marks the end of a phrase
				if len(currentPhrase) > 0 {
					candidates = append(candidates, strings.Join(currentPhrase, " "))
					currentPhrase = nil
				}
			} else {
				currentPhrase = append(currentPhrase, word)
			}
		}
		// Add trailing phrase if any
		if len(currentPhrase) > 0 {
			candidates = append(candidates, strings.Join(currentPhrase, " "))
		}
	}

	// Calculate word frequencies and co-occurrences (simplified RAKE scoring)
	wordFreq := make(map[string]int)
	wordDegree := make(map[string]int)

	for _, phrase := range candidates {
		words := strings.Split(phrase, " ")
		phraseLength := len(words)
		for _, word := range words {
			wordFreq[word]++
			// Degree is phrase length minus 1, but we add 1 for the word itself (RAKE standard)
			wordDegree[word] += phraseLength
		}
	}

	// Score phrases
	phraseScores := make(map[string]float64)
	for _, phrase := range candidates {
		words := strings.Split(phrase, " ")
		var score float64
		for _, word := range words {
			// score = degree(w) / freq(w)
			if wordFreq[word] > 0 {
				score += float64(wordDegree[word]) / float64(wordFreq[word])
			}
		}
		phraseScores[phrase] = score
	}

	// Deduplicate and filter (only return phrases with a decent score)
	// We'll return unique phrases sorted by score (highest first), or just a list.
	// For simplicity, we just return phrases that exist in our map
	var results []string
	seen := make(map[string]bool)
	for phrase, score := range phraseScores {
		// Minimum score threshold (e.g. at least 1.0) and at least some length
		if score >= 1.0 && len(phrase) > 2 && !seen[phrase] {
			results = append(results, phrase)
			seen[phrase] = true
		}
	}

	return results
}
