package vector

import (
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	// 1. Identical vectors
	v1 := []float32{1.0, 0.0, 0.0}
	v2 := []float32{1.0, 0.0, 0.0}
	score := CosineSimilarity(v1, v2)
	if score < 0.99 || score > 1.01 {
		t.Errorf("expected similarity 1.0, got %f", score)
	}

	// 2. Orthogonal vectors
	v3 := []float32{0.0, 1.0, 0.0}
	score = CosineSimilarity(v1, v3)
	if score < -0.01 || score > 0.01 {
		t.Errorf("expected similarity 0.0, got %f", score)
	}

	// 3. Opposite vectors
	v4 := []float32{-1.0, 0.0, 0.0}
	score = CosineSimilarity(v1, v4)
	if score < -1.01 || score > -0.99 {
		t.Errorf("expected similarity -1.0, got %f", score)
	}
}

func TestVectorStore_Search(t *testing.T) {
	store := NewVectorStore()
	store.AddVector("node_a", []float32{1.0, 0.0, 0.0}, map[string]any{"name": "A"})
	store.AddVector("node_b", []float32{0.0, 1.0, 0.0}, map[string]any{"name": "B"})
	store.AddVector("node_c", []float32{0.7, 0.7, 0.0}, map[string]any{"name": "C"})

	// Search closest to [0.9, 0.1, 0.0]
	query := []float32{0.9, 0.1, 0.0}
	results := store.Search(query, 2)

	if len(results) != 2 {
		t.Fatalf("expected 2 search results, got %d", len(results))
	}

	// Top match should be node_a (closer than node_c, and node_b should not be top 2 if we sort correctly)
	if results[0].ID != "node_a" {
		t.Errorf("expected top result to be node_a, got %s (score %f)", results[0].ID, results[0].Score)
	}
	if results[1].ID != "node_c" {
		t.Errorf("expected second result to be node_c, got %s (score %f)", results[1].ID, results[1].Score)
	}
}

func TestBinaryConversion(t *testing.T) {
	vec := []float32{0.1, -0.5, 0.999, 12345.67}
	bytes := Float32SliceToBytes(vec)
	if len(bytes) != 16 { // 4 floats * 4 bytes each = 16 bytes
		t.Errorf("expected 16 bytes, got %d", len(bytes))
	}

	restored := BytesToFloat32Slice(bytes)
	if len(restored) != len(vec) {
		t.Fatalf("slice length mismatch: expected %d, got %d", len(vec), len(restored))
	}

	for i := range vec {
		if restored[i] != vec[i] {
			t.Errorf("value mismatch at index %d: expected %f, got %f", i, vec[i], restored[i])
		}
	}
}
