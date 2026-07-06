package vector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
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

	// Clean and split text into words
	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		words = []string{"empty"}
	}

	// For each dimension, calculate a deterministic value based on the text hash
	for d := 0; d < dims; d++ {
		var val float64
		for i, word := range words {
			h := fnv.New32a()
			h.Write([]byte(fmt.Sprintf("%s_%d_%d", word, d, i)))
			// Map hash value to range [-1, 1]
			hashVal := float64(h.Sum32()) / float64(math.MaxUint32)
			val += math.Sin(hashVal * 2.0 * math.Pi)
		}
		vec[d] = float32(val / math.Sqrt(float64(len(words))))
	}

	// Normalize the pseudo-embedding vector to unit length
	var sumSquares float32
	for _, v := range vec {
		sumSquares += v * v
	}
	norm := float32(math.Sqrt(float64(sumSquares)))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}

	return vec
}
