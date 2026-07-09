package vector

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
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
	db *sql.DB
}

func NewVectorStore(db *sql.DB) *VectorStore {
	return &VectorStore{
		db: db,
	}
}

// AddVector is deprecated. Use db.SaveVector instead.
func (s *VectorStore) AddVector(id string, vec []float32, metadata map[string]any) {}

// RemoveVector is deprecated.
func (s *VectorStore) RemoveVector(id string) {}

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
	if s.db == nil || len(queryVec) == 0 || limit <= 0 {
		return nil
	}

	embBytes := Float32SliceToBytes(queryVec)
	query := `
		SELECT m.node_id, vec_distance_cosine(v.embedding, ?1) as dist, m.metadata
		FROM vectors v
		JOIN vectors_meta m ON v.rowid = m.rowid
		WHERE v.rowid IN (SELECT rowid FROM vectors WHERE embedding MATCH ?1 AND k = ?2)
	`

	rows, err := s.db.Query(query, embBytes, limit)
	if err != nil {
		fmt.Printf("Vector Search Error: %v\n", err)
		return nil
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var id string
		var dist float32
		var metaStr sql.NullString

		if err := rows.Scan(&id, &dist, &metaStr); err != nil {
			continue
		}

		var metadata map[string]any
		if metaStr.Valid && metaStr.String != "" {
			json.Unmarshal([]byte(metaStr.String), &metadata)
		} else {
			metadata = make(map[string]any)
		}

		results = append(results, SearchResult{
			ID:       id,
			// Convert distance to similarity (1 - distance) for backward compatibility
			Score:    1.0 - dist,
			Metadata: metadata,
		})
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
