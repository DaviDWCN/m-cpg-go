package concept

import (
	"strings"
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

func TestExtractConceptsChinese(t *testing.T) {
	concepts := ExtractConcepts("代码图需要解析调用关系")
	found := false
	for _, c := range concepts {
		if strings.Contains(c, "代码") || strings.Contains(c, "调用") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a Chinese concept, got %v", concepts)
	}
}
