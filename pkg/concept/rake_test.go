package concept

import (
	"testing"
)

func TestExtractConcepts(t *testing.T) {
	text := "This is a test of the concept extractor algorithm. It should extract key phrases like concept extractor and key phrases."
	concepts := ExtractConcepts(text)

	if len(concepts) == 0 {
		t.Errorf("Expected concepts to be extracted, got 0")
	}

	found := false
	for _, c := range concepts {
		if c == "concept extractor algorithm" || c == "concept extractor" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected 'concept extractor algorithm' or 'concept extractor' to be extracted. Got: %v", concepts)
	}
}
