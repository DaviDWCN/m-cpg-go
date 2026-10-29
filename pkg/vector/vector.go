package vector

import (
	"database/sql"
	"encoding/json"
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

// Search retrieves the top K matching vectors using sqlite-vec cosine distance.
// k is clamped to the number of stored rows because sqlite-vec rejects a k
// larger than the index. projectID limits hits to one project when set.
// Results are ordered by ascending distance.
func (s *VectorStore) Search(queryVec []float32, limit int, projectID string) ([]SearchResult, error) {
	if s.db == nil || len(queryVec) == 0 || limit <= 0 {
		return nil, nil
	}

	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM vectors").Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}

	k := limit
	if projectID != "" && k*5 > k {
		k = limit * 5
	}
	if k > n {
		k = n
	}
	if k <= 0 {
		return nil, nil
	}

	embBytes := Float32SliceToBytes(queryVec)
	query := `
		SELECT m.node_id, vec_distance_cosine(v.embedding, ?1) as dist, m.metadata
		FROM vectors v
		JOIN vectors_meta m ON v.rowid = m.rowid
		JOIN nodes n ON n.id = m.node_id
		WHERE v.rowid IN (SELECT rowid FROM vectors WHERE embedding MATCH ?1 AND k = ?2)
		  AND (?3 = '' OR n.project_id = ?3)
		ORDER BY dist ASC
		LIMIT ?4
	`

	rows, err := s.db.Query(query, embBytes, k, projectID, limit)
	if err != nil {
		return nil, err
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
			ID: id,
			// Convert distance to similarity (1 - distance) for backward compatibility
			Score:    1.0 - dist,
			Metadata: metadata,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
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
