import json
import subprocess
import os
import sys
import time

def ingest_transcripts(log_file):
    """
    Reads a conversation transcript log file, ingests it to the Cold memory layer,
    and clears the file if successful.
    """
    if not os.path.exists(log_file) or os.path.getsize(log_file) == 0:
        return

    with open(log_file, 'r') as f:
        transcript = f.read()

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

    # Construct the JSON-RPC request for m_cpg_ingest_conversation
    req = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": "m_cpg_ingest_conversation",
            "arguments": {
                "transcript": transcript,
                "summary": "Automated background ingestion of chat transcript"
            }
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
            print(f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] Successfully ingested log file.")
            # Clear the log file after successful ingestion
            open(log_file, 'w').close()
        elif "error" in response:
            print(f"MCP Server Error during ingestion: {response['error']}", file=sys.stderr)
    except json.JSONDecodeError:
        print("Failed to decode response from MCP server.", file=sys.stderr)

    process.terminate()
    process.wait()

def daemon_loop(log_file, interval_seconds=300):
    print(f"Starting Background Cold Ingestion Daemon. Monitoring {log_file} every {interval_seconds} seconds.")
    while True:
        try:
            ingest_transcripts(log_file)
        except Exception as e:
            print(f"Error during ingestion cycle: {e}", file=sys.stderr)

        time.sleep(interval_seconds)

if __name__ == "__main__":
    # Default log file path
    log_file_path = os.path.join(os.path.dirname(os.path.dirname(__file__)), "chat_transcript.log")

    # Allow overriding via arguments
    if len(sys.argv) > 1:
        log_file_path = sys.argv[1]

    interval = 60 # Check every 60 seconds by default for testing, in production this might be higher
    if len(sys.argv) > 2:
        try:
            interval = int(sys.argv[2])
        except ValueError:
            pass

    # Create empty file if it doesn't exist
    if not os.path.exists(log_file_path):
        open(log_file_path, 'w').close()

    daemon_loop(log_file_path, interval)
