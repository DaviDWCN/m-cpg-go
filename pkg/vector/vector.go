package vector

import (
	"bytes"
	"encoding/binary"
	"math"
	"sync"
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
}

func NewVectorStore() *VectorStore {
	return &VectorStore{
		nodes: make([]VectorNode, 0),
	}
}

// AddVector adds a vector to the in-memory store
func (s *VectorStore) AddVector(id string, vec []float32, metadata map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Update if exists, otherwise append
	for i, node := range s.nodes {
		if node.ID == id {
			s.nodes[i].Vector = vec
			s.nodes[i].Metadata = metadata
			return
		}
	}

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

	for i, node := range s.nodes {
		if node.ID == id {
			s.nodes = append(s.nodes[:i], s.nodes[i+1:]...)
			return
		}
	}
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

	// Simple selection sort or bubble sort for small Top-K is fine.
	// But let's write a standard sort.
	for i := 0; i < len(results); i++ {
		maxIdx := i
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[maxIdx].Score {
				maxIdx = j
			}
		}
		if maxIdx != i {
			results[i], results[maxIdx] = results[maxIdx], results[i]
		}
	}

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

// Float32SliceToBytes converts a float32 slice to a raw binary byte slice
func Float32SliceToBytes(slice []float32) []byte {
	buf := new(bytes.Buffer)
	// Write each float in Little Endian order
	for _, f := range slice {
		binary.Write(buf, binary.LittleEndian, f)
	}
	return buf.Bytes()
}

// BytesToFloat32Slice converts raw binary bytes back to a float32 slice
func BytesToFloat32Slice(data []byte) []float32 {
	if len(data)%4 != 0 {
		return nil
	}

	count := len(data) / 4
	slice := make([]float32, count)
	buf := bytes.NewReader(data)
	for i := 0; i < count; i++ {
		binary.Read(buf, binary.LittleEndian, &slice[i])
	}
	return slice
}
