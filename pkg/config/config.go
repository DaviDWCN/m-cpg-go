package config

import (
	"os"
	"path/filepath"
)

type LLMConfig struct {
	Provider string
	Model    string
	Endpoint string
	APIKey   string
}

type EmbeddingConfig struct {
	Provider string
	Model    string
	Endpoint string
	APIKey   string
}

type Config struct {
	DBPath    string
	ProjectID string
	LLM       LLMConfig
	Embedding EmbeddingConfig
}

// LoadDefaultConfig returns a standard local-first configuration
func LoadDefaultConfig() *Config {
	dbPath := os.Getenv("M_CPG_DB_PATH")
	if dbPath == "" {
		dbPath = "./m_cpg.db"
	}
	// Make path absolute if it's local
	if !filepath.IsAbs(dbPath) {
		if cwd, err := os.Getwd(); err == nil {
			dbPath = filepath.Clean(filepath.Join(cwd, dbPath))
		}
	}

	projectID := os.Getenv("M_CPG_PROJECT_ID")
	if projectID == "" {
		projectID = "default-project"
	}

	llmProvider := os.Getenv("M_CPG_LLM_PROVIDER")
	if llmProvider == "" {
		llmProvider = "ollama"
	}
	llmModel := os.Getenv("M_CPG_LLM_MODEL")
	if llmModel == "" {
		llmModel = "llama3"
	}
	llmEndpoint := os.Getenv("M_CPG_LLM_ENDPOINT")
	if llmEndpoint == "" {
		llmEndpoint = "http://localhost:11434/v1"
	}
	llmKey := os.Getenv("M_CPG_LLM_API_KEY")
	if llmKey == "" {
		llmKey = "ollama"
	}

	embedProvider := os.Getenv("M_CPG_EMBEDDING_PROVIDER")
	if embedProvider == "" {
		embedProvider = "ollama"
	}
	embedModel := os.Getenv("M_CPG_EMBEDDING_MODEL")
	if embedModel == "" {
		embedModel = "nomic-embed-text"
	}
	embedEndpoint := os.Getenv("M_CPG_EMBEDDING_ENDPOINT")
	if embedEndpoint == "" {
		embedEndpoint = "http://localhost:11434/v1"
	}
	embedKey := os.Getenv("M_CPG_EMBEDDING_API_KEY")
	if embedKey == "" {
		embedKey = "ollama"
	}

	return &Config{
		DBPath:    dbPath,
		ProjectID: projectID,
		LLM: LLMConfig{
			Provider: llmProvider,
			Model:    llmModel,
			Endpoint: llmEndpoint,
			APIKey:   llmKey,
		},
		Embedding: EmbeddingConfig{
			Provider: embedProvider,
			Model:    embedModel,
			Endpoint: embedEndpoint,
			APIKey:   embedKey,
		},
	}
}
