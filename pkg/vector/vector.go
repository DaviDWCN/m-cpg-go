package vector

import (
	"container/heap"
	"math"
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

// minHeap implements heap.Interface for SearchResult
type minHeap []SearchResult

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].Score < h[j].Score } // Min-heap: smallest score at root
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *minHeap) Push(x interface{}) {
	*h = append(*h, x.(SearchResult))
}

func (h *minHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// Search retrieves the top K matching vectors using cosine similarity
func (s *VectorStore) Search(queryVec []float32, limit int) []SearchResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(queryVec) == 0 || len(s.nodes) == 0 || limit <= 0 {
		return nil
	}

	// Pre-calculate query norm
	var normQ float32
	for _, val := range queryVec {
		normQ += val * val
	}
	normQ = float32(math.Sqrt(float64(normQ)))

	h := &minHeap{}
	heap.Init(h)

	for _, node := range s.nodes {
		score := cosineSimilarityWithQueryNorm(queryVec, node.Vector, normQ)

		if h.Len() < limit {
			heap.Push(h, SearchResult{
				ID:       node.ID,
				Score:    score,
				Metadata: node.Metadata,
			})
		} else if score > (*h)[0].Score {
			// If score is better than the worst score in the heap, replace it
			(*h)[0] = SearchResult{
				ID:       node.ID,
				Score:    score,
				Metadata: node.Metadata,
			}
			heap.Fix(h, 0)
		}
	}

	// Extract results from heap (they will come out in ascending order of score)
	results := make([]SearchResult, h.Len())
	for i := len(results) - 1; i >= 0; i-- {
		results[i] = heap.Pop(h).(SearchResult)
	}

	return results
}

// CosineSimilarity calculates the cosine similarity score between two float32 slices
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}

	var normA float32
	for _, val := range a {
		normA += val * val
	}
	normA = float32(math.Sqrt(float64(normA)))

	return cosineSimilarityWithQueryNorm(a, b, normA)
}

func cosineSimilarityWithQueryNorm(a, b []float32, normA float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}

	var dotProduct, normB float32
	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normB += b[i] * b[i]
	}
	normB = float32(math.Sqrt(float64(normB)))

	if normA == 0.0 || normB == 0.0 {
		return 0.0
	}

	return dotProduct / (normA * normB)
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
