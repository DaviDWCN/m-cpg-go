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

## Per-Project Database Isolation (MCP Configuration)

By default, `m-cpg-go` creates its SQLite database (`m_cpg.db`) in the current working directory where the server is started. If you use a global configuration (e.g., in Claude Desktop or a global IDE setting), all your projects might end up sharing a single, global database file.

To achieve **physical isolation** so that each project gets its own independent database, we highly recommend using **workspace-specific (local) MCP configurations** in your AI IDEs (like Cursor, Windsurf, or Cline) rather than a global one.

When configured locally, the IDE launches the MCP server from the root of your project directory, ensuring `m_cpg.db` is created safely inside that specific project.

### Example: Cursor Configuration
Create a `.cursor/mcp.json` file in the root of your project:

```json
{
  "mcpServers": {
    "m-cpg": {
      "command": "m-cpg-go",
      "args": ["mcp"],
      "env": {
        "M_CPG_DB_PATH": "./.m-cpg/m_cpg.db"
      }
    }
  }
}
```

*Note: In this example, we set `M_CPG_DB_PATH` to `./.m-cpg/m_cpg.db`. You should add `.m-cpg/` to your project's `.gitignore` to avoid tracking the database file in your version control system.*
