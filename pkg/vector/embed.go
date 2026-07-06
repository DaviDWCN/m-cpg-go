package vector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"time"
)

type embedRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// GetEmbedding fetches the embedding vector from Ollama/OpenAI or falls back to pseudo-embeddings
func GetEmbedding(text, provider, model, endpoint, apiKey string) ([]float32, error) {
	if provider == "" || provider == "mock" {
		return GeneratePseudoEmbedding(text), nil
	}

	// Prepare JSON payload
	reqBody := embedRequest{
		Input: text,
		Model: model,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", endpoint+"/embeddings", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" && apiKey != "ollama" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Log warning to stderr and fallback to pseudo-embedding
		fmt.Fprintf(os.Stderr, "[Warning] Embedding API call failed: %v. Falling back to pseudo-embedding.\n", err)
		return GeneratePseudoEmbedding(text), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "[Warning] Embedding API returned status %d: %s. Falling back to pseudo-embedding.\n", resp.StatusCode, string(bodyBytes))
		return GeneratePseudoEmbedding(text), nil
	}

	var res embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to decode embedding response: %w", err)
	}

	if len(res.Data) == 0 || len(res.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding returned from api")
	}

	return res.Data[0].Embedding, nil
}

// GeneratePseudoEmbedding creates a deterministic 768-dimensional float32 vector based on text content
func GeneratePseudoEmbedding(text string) []float32 {
	dims := 768
	vec := make([]float32, dims)

	h := fnv.New64a()
	h.Write([]byte(text))
	seed := int64(h.Sum64())
	rng := rand.New(rand.NewSource(seed))

	var sumSquares float32
	for d := 0; d < dims; d++ {
		// Map rand.Float32() [0.0, 1.0) to [-1.0, 1.0)
		val := rng.Float32()*2.0 - 1.0
		vec[d] = val
		sumSquares += val * val
	}

	// Normalize the pseudo-embedding vector to unit length
	norm := float32(math.Sqrt(float64(sumSquares)))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}

	return vec
}
