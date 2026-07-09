import json
import subprocess
import os
import sys

def run_bootstrap():
    """
    Wrapper script to initialize an AI session by calling the m-cpg-go MCP server
    and injecting the hot context into the environment.
    """
    executable = os.path.join(os.path.dirname(os.path.dirname(__file__)), "m-cpg-go")

    if not os.path.exists(executable):
        print(f"Error: Executable not found at {executable}. Run 'go build -o m-cpg-go .' first.", file=sys.stderr)
        return

    # Start the MCP server process
    process = subprocess.Popen(
        [executable, "mcp"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True
    )

    # Construct the JSON-RPC request for m_cpg_kb_bootstrap
    req = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": "m_cpg_kb_bootstrap",
            "arguments": {}
        }
    }

    req_str = json.dumps(req) + "\n"

    # Send request and read response
    process.stdin.write(req_str)
    process.stdin.flush()

    response_str = process.stdout.readline()
    process.stdin.close()

    try:
        response = json.loads(response_str)
        if "result" in response and "content" in response["result"]:
            for content_item in response["result"]["content"]:
                if content_item.get("type") == "text":
                    print("--- SESSION HOT CONTEXT ---")
                    print(content_item["text"])
                    print("---------------------------")
                    # In a real environment, you might write this to a file or inject it directly
                    # into the system prompt configuration depending on the Agent framework.
        elif "error" in response:
            print(f"MCP Server Error: {response['error']}", file=sys.stderr)
    except json.JSONDecodeError:
        print("Failed to decode response from MCP server.", file=sys.stderr)

    process.terminate()
    process.wait()

if __name__ == "__main__":
    run_bootstrap()
