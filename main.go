package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"m-cpg-go/pkg/config"
	"m-cpg-go/pkg/db"
	"m-cpg-go/pkg/mcp"
	"m-cpg-go/pkg/vector"
)

func main() {
	// Parse CLI commands
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	// Load configuration
	cfg := config.LoadDefaultConfig()

	// Initialize Database
	gdb, err := db.InitDB(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to initialize SQLite database: %v\n", err)
		os.Exit(1)
	}
	defer gdb.Close()

	// Initialize Vector Store
	vStore := vector.NewVectorStore()

	switch command {
	case "mcp":
		// Launch stdio MCP server daemon
		if err := mcp.StartServer(gdb, vStore, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "MCP Server error: %v\n", err)
			os.Exit(1)
		}

	case "index":
		// Index directory command
		indexFlags := flag.NewFlagSet("index", flag.ExitOnError)
		projectIDOpt := indexFlags.String("project", cfg.ProjectID, "Unique identifier for this project")
		
		if err := indexFlags.Parse(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing index flags: %v\n", err)
			os.Exit(1)
		}

		args := indexFlags.Args()
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "Usage: m-cpg-go index [--project <id>] <path-to-directory>")
			os.Exit(1)
		}
		path := args[0]
		projectID := *projectIDOpt

		fmt.Printf("Indexing project '%s' under directory: %s\n", projectID, path)
		files, nodes, edges, err := mcp.RunIndexing(path, projectID, gdb, vStore, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Indexing failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nSUCCESS: Indexing finished!\n- Files Analyzed: %d\n- Graph Nodes Created: %d\n- Relationships Created: %d\n", files, nodes, edges)

	case "search":
		// Query search command
		searchFlags := flag.NewFlagSet("search", flag.ExitOnError)
		topKOpt := searchFlags.String("top_k", "5", "Number of results to retrieve")

		if err := searchFlags.Parse(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing search flags: %v\n", err)
			os.Exit(1)
		}

		args := searchFlags.Args()
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "Usage: m-cpg-go search [--top_k <n>] <query-string>")
			os.Exit(1)
		}
		query := args[0]
		topK, _ := strconv.Atoi(*topKOpt)
		if topK <= 0 {
			topK = 5
		}

		// Load vectors to index for search
		if err := mcp.LoadVectorsIntoMemory(gdb, vStore); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to load existing vectors: %v\n", err)
		}

		fmt.Printf("Searching for query: '%s'...\n\n", query)
		results, err := mcp.RunSearch(query, topK, gdb, vStore, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Search failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(results)

	case "help", "-h", "--help":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Error: Unknown command '%s'\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: m-cpg-go <command> [arguments]")
	fmt.Println("\nCommands:")
	fmt.Println("  mcp                               Launch the stdio MCP server for agent IDE connection")
	fmt.Println("  index [--project <id>] <path>    Index source files (Python and Go) in a directory")
	fmt.Println("  search [--top_k <n>] <query>      Perform a hybrid vector/graph search on the database")
	fmt.Println("  help                              Display this help text")
}
