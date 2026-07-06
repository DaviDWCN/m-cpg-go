package vector

import (
	"math"
	"sort"
	"sync"
	"unsafe"
)

type VectorNode struct {
	ID       string
	Vector   []float32
	Metadata map[string]any
}

type SearchResult struct {
	ID       string
	Score    float32
	Metadata map[string]any
}

type VectorStore struct {
	mu    sync.RWMutex
	nodes []VectorNode
	index map[string]int // ID -> index in nodes
}

func NewVectorStore() *VectorStore {
	return &VectorStore{
		nodes: make([]VectorNode, 0),
		index: make(map[string]int),
	}
}

// AddVector adds a vector to the in-memory store
func (s *VectorStore) AddVector(id string, vec []float32, metadata map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Update if exists, otherwise append
	if idx, exists := s.index[id]; exists {
		s.nodes[idx].Vector = vec
		s.nodes[idx].Metadata = metadata
		return
	}

	s.index[id] = len(s.nodes)
	s.nodes = append(s.nodes, VectorNode{
		ID:       id,
		Vector:   vec,
		Metadata: metadata,
	})
}

// RemoveVector deletes a vector from the in-memory store
func (s *VectorStore) RemoveVector(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, exists := s.index[id]
	if !exists {
		return
	}

	lastIdx := len(s.nodes) - 1
	if idx != lastIdx {
		s.nodes[idx] = s.nodes[lastIdx]
		s.index[s.nodes[idx].ID] = idx
	}
	s.nodes = s.nodes[:lastIdx]
	delete(s.index, id)
}

// Search retrieves the top K matching vectors using cosine similarity
func (s *VectorStore) Search(queryVec []float32, limit int) []SearchResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(queryVec) == 0 || len(s.nodes) == 0 {
		return nil
	}

	results := make([]SearchResult, 0, len(s.nodes))
	for _, node := range s.nodes {
		score := CosineSimilarity(queryVec, node.Vector)
		results = append(results, SearchResult{
			ID:       node.ID,
			Score:    score,
			Metadata: node.Metadata,
		})
	}

	// Sort matching results descending by similarity score
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if limit > len(results) {
		limit = len(results)
	}
	return results[:limit]
}

// CosineSimilarity calculates the cosine similarity score between two float32 slices
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}

	var dotProduct, normA, normB float32
	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0.0 || normB == 0.0 {
		return 0.0
	}

	return dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

// Float32SliceToBytes converts a float32 slice to a raw binary byte slice using unsafe pointer casting
func Float32SliceToBytes(slice []float32) []byte {
	if len(slice) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&slice[0])), len(slice)*4)
}

// BytesToFloat32Slice converts raw binary bytes back to a float32 slice using unsafe pointer casting
func BytesToFloat32Slice(data []byte) []float32 {
	if len(data) == 0 || len(data)%4 != 0 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&data[0])), len(data)/4)
}
