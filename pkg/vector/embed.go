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
	"sync"
	"time"
)

// EmbeddingSource records whether a vector came from the embedding API.
// Pseudo vectors live in a different geometry and must not be written to vec0.
type EmbeddingSource string

const (
	EmbeddingSourceAPI    EmbeddingSource = "api"
	EmbeddingSourcePseudo EmbeddingSource = "pseudo"
)

var embedClient = &http.Client{Timeout: 10 * time.Second}

var (
	embedMu        sync.Mutex
	embedDownUntil time.Time
)

func embeddingAPIDown() bool {
	embedMu.Lock()
	defer embedMu.Unlock()
	return time.Now().Before(embedDownUntil)
}

func markEmbeddingAPIDown() {
	embedMu.Lock()
	embedDownUntil = time.Now().Add(30 * time.Second)
	embedMu.Unlock()
}

type embedRequest struct {
	Input any    `json:"input"`
	Model string `json:"model"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// GetEmbedding fetches one embedding. API failures return a deterministic
// pseudo vector and a nil error so callers can choose not to index it.
func GetEmbedding(text, provider, model, endpoint, apiKey string, dimension int) ([]float32, EmbeddingSource, error) {
	vecs, sources, err := GetEmbeddings([]string{text}, provider, model, endpoint, apiKey, dimension)
	if err != nil {
		return nil, "", err
	}
	if len(vecs) == 0 {
		return GeneratePseudoEmbedding(text, dimension), EmbeddingSourcePseudo, nil
	}
	return vecs[0], sources[0], nil
}

// GetEmbeddings fetches embeddings for a batch. The OpenAI-compatible
// endpoint accepts an array input. A transport failure marks the endpoint
// down for 30 seconds so a large index does not open one connection per symbol.
func GetEmbeddings(texts []string, provider, model, endpoint, apiKey string, dimension int) ([][]float32, []EmbeddingSource, error) {
	out := make([][]float32, len(texts))
	sources := make([]EmbeddingSource, len(texts))
	if len(texts) == 0 {
		return out, sources, nil
	}

	fillPseudo := func() ([][]float32, []EmbeddingSource, error) {
		for i, text := range texts {
			out[i] = GeneratePseudoEmbedding(text, dimension)
			sources[i] = EmbeddingSourcePseudo
		}
		return out, sources, nil
	}

	if provider == "" || provider == "mock" || embeddingAPIDown() {
		return fillPseudo()
	}

	reqBody := embedRequest{Input: texts, Model: model}
	if len(texts) == 1 {
		reqBody.Input = texts[0]
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}

	req, err := http.NewRequest("POST", endpoint+"/embeddings", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" && apiKey != "ollama" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := embedClient.Do(req)
	if err != nil {
		markEmbeddingAPIDown()
		fmt.Fprintf(os.Stderr, "[Warning] Embedding API call failed: %v. Falling back to pseudo-embedding.\n", err)
		return fillPseudo()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		markEmbeddingAPIDown()
		fmt.Fprintf(os.Stderr, "[Warning] Embedding API returned status %d: %s. Falling back to pseudo-embedding.\n", resp.StatusCode, string(bodyBytes))
		return fillPseudo()
	}

	var res embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		fmt.Fprintf(os.Stderr, "[Warning] Failed to decode embedding response: %v. Falling back to pseudo-embedding.\n", err)
		return fillPseudo()
	}

	useOrder := len(res.Data) > 1
	for _, item := range res.Data {
		if item.Index != 0 {
			useOrder = false
			break
		}
	}
	for i, item := range res.Data {
		idx := item.Index
		if useOrder {
			idx = i
		}
		if idx < 0 || idx >= len(texts) || len(item.Embedding) == 0 {
			continue
		}
		out[idx] = item.Embedding
		sources[idx] = EmbeddingSourceAPI
	}
	for i, text := range texts {
		if sources[i] == "" {
			out[i] = GeneratePseudoEmbedding(text, dimension)
			sources[i] = EmbeddingSourcePseudo
		}
	}
	return out, sources, nil
}

// GeneratePseudoEmbedding creates a feature-hashing bag-of-words vector.
// It preserves a little lexical overlap when the embedding API is unavailable.
func GeneratePseudoEmbedding(text string, dims int) []float32 {
	if dims <= 0 {
		dims = 768
	}
	vec := make([]float32, dims)

	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		return vec
	}

	var sumSquares float32
	for _, word := range words {
		word = strings.Trim(word, ".,!?\"'()[]{}")
		if word == "" {
			continue
		}

		h := fnv.New32a()
		h.Write([]byte(word))
		idx := h.Sum32() % uint32(dims)
		vec[idx] += 1.0
	}

	for _, v := range vec {
		sumSquares += v * v
	}

	if sumSquares > 0 {
		norm := float32(math.Sqrt(float64(sumSquares)))
		for i := range vec {
			vec[i] /= norm
		}
	}

	return vec
}
