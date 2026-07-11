# m-cpg-go Deployable Package (Ollama Version)

This folder contains pre-compiled binaries and configuration files configured to use a local Ollama instance for the LLM and Embedding models.

## Contents

- `m-cpg-go.exe`: Windows pre-compiled binary.
- `m-cpg-go`: Linux pre-compiled binary (ELF format).
- `mcp_config.json`: Configuration template for MCP-enabled IDEs (Cursor, Windsurf, Claude Desktop, etc.).
- `.env.example`: Configuration template for shell / environment variable setups.

## Integration Guide

### 1. Integrating as an MCP Server in IDEs (Cursor / Windsurf)

To run `m-cpg-go` as an MCP server for your AI coding assistant:

1. Copy the appropriate binary (`m-cpg-go.exe` or `m-cpg-go`) to a stable global location, or leave it in this directory.
2. In the target project where you want to use the Code Graph, copy the configuration from `mcp_config.json` into your local IDE settings.
   - For **Cursor**, you can create a local file `.cursor/mcp.json` in the root of the project.
   - Update the `"command"` field to the absolute path of the executable.
   - Update `"M_CPG_PROJECT_ID"` to a unique identifier for that project.
   - Ensure `"M_CPG_DB_PATH"` points to where you want the SQLite database to live (e.g. `C:\Users\...\project\.m-cpg\m_cpg.db`).

### 2. Manual CLI Operations (Indexing & Search)

You can run the binary directly via command line to index a directory:

```bash
# Index a project directory
./m-cpg-go index --project my-project-id /path/to/source/code

# Query the index (hybrid graph + vector search)
./m-cpg-go search "find all database initialization functions"
```

Refer to the root project README for more details.
