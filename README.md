# m-cpg-go

A lightweight, in-process, zero-dependency Code Graph & Vector Memory System for AI coding agents written in Go.

## Features
- **In-process SQLite Graph Engine**: Stores files, classes, methods, and their relations (`CONTAINS`, `CALLS`, etc.) using SQLite.
- **In-process Vector Store**: Performs vector indexing and concurrent cosine similarity query retrieval.
- **Structural Code Parser**: Extracts class/method layout and docstrings from Python and Go files.
- **MCP Server**: Implements the Model Context Protocol stdio transport for direct connection with AI IDEs (Cursor, Windsurf, Claude Desktop).

## Getting Started

### Building the Project

Ensure you have Go installed (version 1.22+ recommended). You can build the production executable using the standard Go build command:

```bash
# Build the executable
go build -o m-cpg-go .
```

### Usage

Once built, you can run the executable directly from the root of the repository:

```bash
# Display help and available commands
./m-cpg-go help

# Launch the stdio MCP server for agent IDE connection
./m-cpg-go mcp

# Index source files (Python and Go) in a specific directory
./m-cpg-go index <path-to-directory>

# Optional: Index with a specific project ID
./m-cpg-go index --project my-project-id <path-to-directory>

# Perform a hybrid vector/graph search on the database
./m-cpg-go search "your query here"
```
