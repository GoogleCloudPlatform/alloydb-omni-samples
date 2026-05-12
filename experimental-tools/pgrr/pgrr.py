#!/usr/bin/env python3
"""
pgrr - PostgreSQL Record & Replay
A unified tool for capturing and replaying PostgreSQL wire protocol traffic.
"""
import asyncio
import argparse
import csv
import json
import time
import sys
import re
from asyncio.streams import StreamReader, StreamWriter
from datetime import datetime
from typing import Optional, Dict, List, Set
from collections import defaultdict

try:
    import psycopg2
    PSYCOPG2_AVAILABLE = True
except ImportError:
    PSYCOPG2_AVAILABLE = False

# ============================================================================
# PROXY/CAPTURE FUNCTIONALITY
# ============================================================================

# Default values for proxy
REAL_PG_HOST = "127.0.0.1"
REAL_PG_PORT = 5432
PROXY_LISTEN_PORT = 5433
CAPTURE_FILE = "queries.json"

# Proxy globals
DB_META = {
    "db_host": REAL_PG_HOST,
    "db_port": REAL_PG_PORT,
    "db_user": None,
    "db_name": None,
}

TOTAL_RECORDS = 0
SKIPPED_RECORDS = 0


def save_query_json(record: dict, filename=CAPTURE_FILE):
    """Append a JSON record to a file (newline-delimited JSON format)."""
    with open(filename, "a", encoding="utf-8") as f:
        json.dump(record, f)
        f.write("\n")


def try_parse_startup_params(chunk: bytes) -> Optional[Dict[str, str]]:
    """Best-effort parse of a Postgres StartupPacket from a raw chunk."""
    if len(chunk) < 8:
        return None

    proto = chunk[4:8]
    if proto != bytes.fromhex("00030000"):
        return None

    params: Dict[str, str] = {}
    rest = chunk[8:]
    parts = rest.split(b"\x00")
    for i in range(0, len(parts) - 1, 2):
        k = parts[i]
        v = parts[i + 1] if i + 1 < len(parts) else b""
        if not k:
            break
        params[k.decode("utf-8", errors="ignore")] = v.decode("utf-8", errors="ignore")

    return params


def make_in_memory_record(data: bytes):
    """Build record structure from a raw chunk."""
    msg_type = None
    sql = None
    description = None

    if len(data) > 0:
        msg_type = data[0:1].decode("ascii", errors="replace")

        if msg_type == "Q" and len(data) > 5:
            sql_bytes = data[5:]
            sql = sql_bytes.split(b"\x00")[0].decode("utf-8", errors="replace")

        message_descriptions = {
            "Q": "Simple Query",
            "R": "AuthenticationRequest",
            "S": "ParameterStatus",
            "K": "BackendKeyData",
            "Z": "ReadyForQuery",
            "T": "RowDescription",
            "D": "DataRow",
            "C": "CommandComplete",
            "E": "ErrorResponse",
            "N": "NoticeResponse",
            "1": "ParseComplete",
            "2": "BindComplete",
            "3": "CloseComplete",
            "X": "Terminate",
        }

        if msg_type in message_descriptions:
            description = message_descriptions[msg_type]

    return {
        "msg_type": msg_type,
        "description": description,
        "sql": sql,
        "raw_hex": data.hex(),
    }


def add_meta_fields(record: dict) -> dict:
    """Add per-record capture_time + db details to each record."""
    record["capture_time"] = datetime.now().isoformat()
    record.update(DB_META)
    return record


async def forward(src: StreamReader, dst: StreamWriter, direction: str):
    """Forward data from src -> dst while logging the bytes."""
    global TOTAL_RECORDS, SKIPPED_RECORDS

    try:
        while True:
            data = await src.read(4096)
            if not data:
                print(f"[{direction}] connection closed")
                break

            params = try_parse_startup_params(data)
            if params:
                if DB_META["db_user"] is None and params.get("user"):
                    DB_META["db_user"] = params.get("user")
                if DB_META["db_name"] is None and params.get("database"):
                    DB_META["db_name"] = params.get("database")

            record = make_in_memory_record(data)
            record["direction"] = direction
            record = add_meta_fields(record)

            try:
                save_query_json(record)
                TOTAL_RECORDS += 1
            except Exception as e:
                SKIPPED_RECORDS += 1
                print(f"[{direction}] failed to write record: {e}")

            dst.write(data)
            await dst.drain()

    except Exception as e:
        print(f"[{direction}] error: {e}")
    finally:
        try:
            dst.close()
            await dst.wait_closed()
        except Exception:
            pass


async def handle_socket(client_reader: StreamReader, client_writer: StreamWriter):
    """Handle one client connection, forwarding to the configured upstream."""
    addr = client_writer.get_extra_info("peername")
    print(f"\n=== New client connection from {addr} ===")

    try:
        print("Connecting to real Postgres server...")
        server_reader, server_writer = await asyncio.open_connection(DB_META["db_host"], DB_META["db_port"])
        print(f"Connected to real Postgres at {DB_META['db_host']}:{DB_META['db_port']}.")

        client_to_server = forward(client_reader, server_writer, f"{addr} client → server")
        server_to_client = forward(server_reader, client_writer, f"{addr} server → client")

        await asyncio.gather(client_to_server, server_to_client)

    except Exception as e:
        print(f"Error handling connection from {addr}: {e}")
    finally:
        print(f"=== Closing connection for {addr} ===")
        try:
            client_writer.close()
            await client_writer.wait_closed()
        except Exception:
            pass


async def listen(port: int, real_host: str = REAL_PG_HOST, real_port: int = REAL_PG_PORT):
    """Start the proxy server."""
    DB_META["db_host"] = real_host
    DB_META["db_port"] = real_port

    async def handler(client_reader: StreamReader, client_writer: StreamWriter):
        await handle_socket(client_reader, client_writer)

    server = await asyncio.start_server(handler, "0.0.0.0", port)
    print(f"Transparent PG proxy listening on 0.0.0.0:{port}")
    print(f"Upstream Postgres: {real_host}:{real_port}")
    print(f"Capture file: {CAPTURE_FILE}")
    print("Capture time: per-record (capture_time field)")
    async with server:
        await server.serve_forever()


def write_summary_record():
    """Append ONE summary record at the end."""
    summary = {
        "capture_time": datetime.now().isoformat(),
        "msg_type": "SUMMARY",
        "description": "capture_summary",
        "sql": None,
        "raw_hex": "",
        "direction": "meta",
        **DB_META,
        "total_records": TOTAL_RECORDS,
        "skipped_records": SKIPPED_RECORDS,
    }
    try:
        save_query_json(summary)
    except Exception as e:
        print(f"[summary] failed to write summary: {e}")


# ============================================================================
# REPLAY FUNCTIONALITY
# ============================================================================

def parse_iso(ts: str) -> datetime:
    """Parse ISO timestamp."""
    return datetime.fromisoformat(ts)


def extract_client_ports(path: str) -> Set[int]:
    """Extract all unique client ports from the capture file."""
    ports = set()
    port_pattern = re.compile(r"\('127\.0\.0\.1', (\d+)\) client → server")
    
    try:
        with open(path, "r") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                    direction = rec.get("direction", "")
                    match = port_pattern.search(direction)
                    if match:
                        ports.add(int(match.group(1)))
                except Exception:
                    continue
    except FileNotFoundError:
        print(f"ERROR: Capture file '{path}' not found.")
        return set()
    
    return ports


def extract_sql_queries(capture_path: str, client_port: Optional[int] = None) -> List[Dict]:
    """Extract SQL queries from the capture file."""
    queries = []
    
    try:
        with open(capture_path, "r") as f:
            for line_no, line in enumerate(f, start=1):
                line = line.strip()
                if not line:
                    continue
                
                try:
                    rec = json.loads(line)
                except Exception as e:
                    print(f"[warn] skipping bad json line {line_no}: {e}")
                    continue
                
                # Only process client → server packets
                direction = rec.get("direction", "")
                if "client → server" not in direction:
                    continue
                
                # Extract client port from direction string
                if ", " in direction:
                    port_str = direction.split(", ")[1].split(")")[0]
                    rec_port = int(port_str)
                else:
                    continue
                
                # Filter by client port if specified
                if client_port is not None and rec_port != client_port:
                    continue
                
                # Only include records with SQL
                sql = rec.get("sql")
                if not sql:
                    continue
                
                queries.append({
                    "timestamp": rec.get("timestamp") or rec.get("capture_time"),
                    "sql": sql,
                    "client_port": rec_port,
                    "msg_type": rec.get("msg_type"),
                })
    except FileNotFoundError:
        print(f"ERROR: Capture file '{capture_path}' not found.")
        return []
    
    # Sort by timestamp
    queries.sort(key=lambda q: q["timestamp"])
    return queries


def list_sessions(capture_file: str):
    """List all captured client sessions."""
    print(f"Scanning {capture_file} for client sessions...")
    ports = extract_client_ports(capture_file)
    
    if ports:
        print(f"\nFound {len(ports)} client session(s):")
        for p in sorted(ports):
            queries = extract_sql_queries(capture_file, client_port=p)
            print(f"  - Client port {p}: {len(queries)} SQL queries")
        print(f"\nTo replay a specific session, use:")
        print(f"  pgrr replay --client-port {sorted(ports)[0]} --dbname <db> --user <user>")
    else:
        print("No client sessions found in capture file.")


def execute_queries(
    queries: List[Dict],
    dbname: str,
    user: str,
    password: Optional[str] = None,
    host: str = "localhost",
    port: int = 5432,
    delay_multiplier: float = 1.0,
    dry_run: bool = False,
):
    """Execute the extracted SQL queries."""
    if not PSYCOPG2_AVAILABLE:
        print("ERROR: psycopg2 is not installed. Install it with:")
        print("  pip install psycopg2-binary")
        sys.exit(1)
    
    if not queries:
        print("No queries to execute.")
        return
    
    print(f"Found {len(queries)} SQL queries to execute.")
    
    if dry_run:
        print("\n[DRY RUN MODE - Not connecting to database]\n")
        for i, q in enumerate(queries, 1):
            print(f"{i}. {q['sql']}")
        return
    
    # Connect to database
    print(f"Connecting to {host}:{port}/{dbname} as {user}...")
    try:
        conn_params = {
            "dbname": dbname,
            "user": user,
            "host": host,
            "port": port,
        }
        if password:
            conn_params["password"] = password
        
        conn = psycopg2.connect(**conn_params)
        cur = conn.cursor()
        print("Connected.\n")
    except Exception as e:
        print(f"ERROR: Failed to connect: {e}")
        return
    
    # Execute queries with timing
    first_ts = parse_iso(queries[0]["timestamp"]) if queries else None
    start_time = time.time()
    
    executed = 0
    failed = 0
    
    try:
        for i, q in enumerate(queries, 1):
            # Calculate delay based on original timing
            if first_ts and delay_multiplier != float('inf'):
                current_ts = parse_iso(q["timestamp"])
                target_offset = (current_ts - first_ts).total_seconds() / delay_multiplier
                elapsed = time.time() - start_time
                sleep_time = target_offset - elapsed
                
                if sleep_time > 0:
                    time.sleep(sleep_time)
            
            sql = q["sql"]
            print(f"[{i}/{len(queries)}] Executing: {sql[:80]}{'...' if len(sql) > 80 else ''}")
            
            try:
                cur.execute(sql)
                
                # Fetch and display results for SELECT queries
                if sql.strip().upper().startswith("SELECT"):
                    rows = cur.fetchall()
                    print(f"  → {len(rows)} rows returned")
                    
                    # Show first few rows
                    for row in rows[:3]:
                        print(f"     {row}")
                    if len(rows) > 3:
                        print(f"     ... {len(rows) - 3} more rows")
                else:
                    conn.commit()
                    print(f"  → OK")
                
                executed += 1
                
            except Exception as e:
                print(f"  → ERROR: {e}")
                conn.rollback()
                failed += 1
            
            print()
    
    finally:
        cur.close()
        conn.close()
    
    print(f"\nDone. Executed: {executed}, Failed: {failed}")


# ============================================================================
# COMMAND HANDLERS
# ============================================================================

def cmd_capture(args):
    """Handle the 'capture' subcommand."""
    global CAPTURE_FILE
    CAPTURE_FILE = args.capture_file
    
    print("=" * 70)
    print("PostgreSQL Query Capture Proxy")
    print("=" * 70)
    print(f"Listening on:    0.0.0.0:{args.listen_port}")
    print(f"Forwarding to:   {args.target_host}:{args.target_port}")
    print(f"Capture file:    {args.capture_file}")
    print("=" * 70)
    print()
    print("Connect to the proxy using:")
    print(f"  psql -h localhost -p {args.listen_port} -U <user> -d <database>")
    print()
    print("Press Ctrl+C to stop the proxy")
    print("=" * 70)
    print()
    
    try:
        asyncio.run(listen(args.listen_port, args.target_host, args.target_port))
    except KeyboardInterrupt:
        print("\nShutting down proxy...")
    finally:
        write_summary_record()
        print(f"[summary] total_records={TOTAL_RECORDS}, skipped_records={SKIPPED_RECORDS}")


def cmd_replay(args):
    """Handle the 'replay' subcommand."""
    if args.list:
        list_sessions(args.capture_file)
        return
    
    # Extract queries
    print(f"Extracting SQL queries from {args.capture_file}...")
    queries = extract_sql_queries(args.capture_file, client_port=args.client_port)
    
    if not queries:
        print("No SQL queries found in capture file.")
        if not args.client_port:
            print("\nTip: Try specifying --client-port to filter to a specific session.")
            print("     Use 'pgrr replay --list' to see available sessions.")
        return
    
    print(f"Found {len(queries)} SQL queries.\n")
    
    # Execute queries
    execute_queries(
        queries=queries,
        dbname=args.dbname,
        user=args.user,
        password=args.password,
        host=args.host,
        port=args.port,
        delay_multiplier=args.speed if args.speed > 0 else float('inf'),
        dry_run=args.dry_run,
    )


# ============================================================================
# MAIN CLI
# ============================================================================

def main():
    """Main entry point for pgrr CLI."""
    parser = argparse.ArgumentParser(
        prog="pgrr",
        description="PostgreSQL Record & Replay - Capture and replay PostgreSQL wire protocol traffic",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Capture queries
  pgrr capture
  pgrr capture --target-host prod-db.com --capture-file prod_queries.json
  
  # List captured sessions
  pgrr replay --list
  
  # Replay queries (dry-run)
  pgrr replay --dry-run --dbname mydb --user myuser
  
  # Replay queries
  pgrr replay --client-port 54752 --dbname mydb --user myuser
  pgrr replay --client-port 54752 --dbname mydb --user myuser --speed 0
  
  # Replay to different server
  pgrr replay --client-port 54752 --host remote.com --dbname mydb --user myuser

For more help on a specific command, use:
  pgrr capture --help
  pgrr replay --help
        """
    )
    
    subparsers = parser.add_subparsers(dest="command", help="Available commands")
    
    # ========================================================================
    # CAPTURE subcommand
    # ========================================================================
    capture_parser = subparsers.add_parser(
        "capture",
        help="Start proxy to capture PostgreSQL traffic",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Default: Listen on 5433, forward to localhost:5432
  pgrr capture
  
  # Forward to remote database
  pgrr capture --target-host db.example.com --target-port 5432
  
  # Custom listen port
  pgrr capture --listen-port 6543
  
  # Custom capture file
  pgrr capture --capture-file my_queries.json
        """
    )
    capture_parser.add_argument(
        "--listen-port",
        type=int,
        default=PROXY_LISTEN_PORT,
        help=f"Port for proxy to listen on (default: {PROXY_LISTEN_PORT})"
    )
    capture_parser.add_argument(
        "--target-host",
        default=REAL_PG_HOST,
        help=f"Target PostgreSQL host to forward to (default: {REAL_PG_HOST})"
    )
    capture_parser.add_argument(
        "--target-port",
        type=int,
        default=REAL_PG_PORT,
        help=f"Target PostgreSQL port to forward to (default: {REAL_PG_PORT})"
    )
    capture_parser.add_argument(
        "--capture-file",
        default=CAPTURE_FILE,
        help=f"File to save captured traffic (default: {CAPTURE_FILE})"
    )
    
    # ========================================================================
    # REPLAY subcommand
    # ========================================================================
    replay_parser = subparsers.add_parser(
        "replay",
        help="Replay captured queries to a PostgreSQL database",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # List available sessions
  pgrr replay --list
  
  # Dry run (see queries without executing)
  pgrr replay --dry-run --dbname mydb --user myuser
  
  # Execute queries from a specific session
  pgrr replay --client-port 54752 --dbname mydb --user myuser
  
  # Execute immediately (no delays)
  pgrr replay --client-port 54752 --dbname mydb --user myuser --speed 0
  
  # Execute at 2x speed
  pgrr replay --client-port 54752 --dbname mydb --user myuser --speed 2.0
  
  # Replay to different server
  pgrr replay --client-port 54752 --host remote-db.com --dbname mydb --user myuser
        """
    )
    replay_parser.add_argument(
        "--list",
        action="store_true",
        help="List available client sessions in the capture file and exit"
    )
    replay_parser.add_argument(
        "--client-port",
        type=int,
        help="Replay only queries from this client port (filters to a single session)"
    )
    replay_parser.add_argument(
        "--capture-file",
        default="queries.json",
        help="Path to capture file (default: queries.json)"
    )
    replay_parser.add_argument(
        "--dbname",
        help="Database name to connect to (required unless --list or --dry-run)"
    )
    replay_parser.add_argument(
        "--user",
        help="Database user (required unless --list or --dry-run)"
    )
    replay_parser.add_argument(
        "--password",
        help="Database password (will prompt if not provided)"
    )
    replay_parser.add_argument(
        "--host",
        default="localhost",
        help="Database host (default: localhost)"
    )
    replay_parser.add_argument(
        "--port",
        type=int,
        default=5432,
        help="Database port (default: 5432)"
    )
    replay_parser.add_argument(
        "--speed",
        type=float,
        default=1.0,
        help="Speed multiplier for query timing (default: 1.0, 0 = instant)"
    )
    replay_parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show queries without executing them"
    )
    
    # Parse arguments
    args = parser.parse_args()
    
    # Handle no command
    if not args.command:
        parser.print_help()
        sys.exit(1)
    
    # Validate replay arguments
    if args.command == "replay":
        if not args.list and not args.dry_run:
            if not args.dbname or not args.user:
                replay_parser.error("--dbname and --user are required unless using --list or --dry-run")
        elif args.dry_run and (not args.dbname or not args.user):
            replay_parser.error("--dbname and --user are required for --dry-run")
    
    # Execute command
    if args.command == "capture":
        cmd_capture(args)
    elif args.command == "replay":
        cmd_replay(args)


if __name__ == "__main__":
    main()
