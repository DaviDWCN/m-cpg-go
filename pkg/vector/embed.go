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

// GeneratePseudoEmbedding creates a feature-hashing based TF-BoW 768-dimensional float32 vector based on text content.
// This preserves some semantic similarity when external APIs fail.
func GeneratePseudoEmbedding(text string) []float32 {
	dims := 768
	vec := make([]float32, dims)

	// Basic tokenization
	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		return vec
	}

	// Feature Hashing
	var sumSquares float32
	for _, word := range words {
		// Clean basic punctuation
		word = strings.Trim(word, ".,!?\"'()[]{}")
		if word == "" {
			continue
		}

		h := fnv.New32a()
		h.Write([]byte(word))
		idx := h.Sum32() % uint32(dims)
		vec[idx] += 1.0
	}

	// Calculate L2 Norm
	for _, v := range vec {
		sumSquares += v * v
	}

	// Normalize
	if sumSquares > 0 {
		norm := float32(math.Sqrt(float64(sumSquares)))
		for i := range vec {
			vec[i] /= norm
		}
	}

	return vec
}
