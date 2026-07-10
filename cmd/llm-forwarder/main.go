package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

func main() {
	// Parse command line arguments
	var (
		port          int
		targetAPI     string
		targetAPIKey  string
	)

	flag.IntVar(&port, "port", 8080, "Port to listen on")
	flag.StringVar(&targetAPI, "target", "", "Target embedding API endpoint (e.g., https://api.openai.com/v1, or http://localhost:11434/api)")
	flag.StringVar(&targetAPIKey, "key", "", "API key for the target API (optional for local Ollama)")
	flag.Parse()

	// If not provided via flag, check environment variables
	if targetAPI == "" {
		targetAPI = os.Getenv("TARGET_EMBEDDING_API")
	}
	if targetAPIKey == "" {
		targetAPIKey = os.Getenv("TARGET_EMBEDDING_API_KEY")
	}

	if targetAPI == "" {
		log.Fatalf("Error: Target API endpoint is required. Provide it via -target flag or TARGET_EMBEDDING_API env variable.")
	}

	// Remove trailing slash if present
	targetAPI = strings.TrimRight(targetAPI, "/")

	log.Printf("Starting LLM Embedding Forwarder on port %d...", port)
	log.Printf("Target API endpoint: %s", targetAPI)
	if targetAPIKey != "" {
		log.Printf("API Key: [Provided]")
	} else {
		log.Printf("API Key: [Not Provided]")
	}

	// Set up multiplexer
	mux := http.NewServeMux()

	// Handle /embeddings endpoint (which is what m-cpg-go calls)
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Create a reverse proxy for forwarding the request dynamically
		// We'll use httputil.NewSingleHostReverseProxy in the standard library
		// But since we want to modify headers and route cleanly without changing the base

		// Note: Since we are routing specific endpoint, manual proxying is fine,
		// but using ReverseProxy is more robust.
		targetURLParsed, err := url.Parse(targetAPI)
		if err != nil {
			http.Error(w, "Invalid target API", http.StatusInternalServerError)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(targetURLParsed)

		// Modify the request before sending
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			// Ensure it always hits /embeddings on the target
			req.URL.Path = "/embeddings"

			// Inject API Key if provided locally
			if targetAPIKey != "" {
				req.Header.Set("Authorization", "Bearer "+targetAPIKey)
			}

			req.Host = targetURLParsed.Host
			log.Printf("Forwarding request to %s", req.URL.String())
		}

		proxy.ServeHTTP(w, r)
	})

	// Add a simple health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}
