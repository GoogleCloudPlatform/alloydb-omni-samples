#!/usr/bin/env python3
"""
Extract SQL queries from queries.json and re-execute them.
This is more reliable than raw protocol replay.
"""
import json
import argparse
import time
import psycopg2
from datetime import datetime
from typing import List, Dict, Optional


def parse_iso(ts: str) -> datetime:
    return datetime.fromisoformat(ts)


def extract_sql_queries(capture_path: str, client_port: Optional[int] = None) -> List[Dict]:
    """
    Extract SQL queries from the capture file.
    Returns a list of dicts with timestamp, sql, and client_port.
    """
    queries = []
    
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
            # Format: "('127.0.0.1', 54752) client → server"
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
                "timestamp": rec.get("timestamp"),
                "sql": sql,
                "client_port": rec_port,
                "msg_type": rec.get("msg_type"),
            })
    
    # Sort by timestamp
    queries.sort(key=lambda q: q["timestamp"])
    return queries


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
    """
    Execute the extracted SQL queries.
    """
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
            if first_ts:
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


def main():
    parser = argparse.ArgumentParser(
        description="Extract and re-execute SQL queries from queries.json",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Dry run - show queries without executing
  python sql_replay.py --dry-run

  # Execute queries from a specific client session
  python sql_replay.py --client-port 54752 --dbname chinook --user kourosh

  # Execute at 2x speed
  python sql_replay.py --client-port 54752 --dbname chinook --user kourosh --speed 2.0

  # Execute immediately (no delays)
  python sql_replay.py --client-port 54752 --dbname chinook --user kourosh --speed 0
        """
    )
    
    parser.add_argument(
        "--client-port",
        type=int,
        help="Extract queries only from this client port (recommended)"
    )
    parser.add_argument(
        "--capture-file",
        default="queries.json",
        help="Path to capture file (default: queries.json)"
    )
    parser.add_argument(
        "--dbname",
        required=True,
        help="Database name to connect to"
    )
    parser.add_argument(
        "--user",
        required=True,
        help="Database user"
    )
    parser.add_argument(
        "--password",
        help="Database password (will prompt if not provided)"
    )
    parser.add_argument(
        "--host",
        default="localhost",
        help="Database host (default: localhost)"
    )
    parser.add_argument(
        "--port",
        type=int,
        default=5432,
        help="Database port (default: 5432)"
    )
    parser.add_argument(
        "--speed",
        type=float,
        default=1.0,
        help="Speed multiplier for query timing (default: 1.0, 0 = no delay)"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show queries without executing them"
    )
    
    args = parser.parse_args()
    
    # Extract queries
    print(f"Extracting SQL queries from {args.capture_file}...")
    queries = extract_sql_queries(args.capture_file, client_port=args.client_port)
    
    if not queries:
        print("No SQL queries found in capture file.")
        if not args.client_port:
            print("\nTip: Try specifying --client-port to filter to a specific session.")
            print("     Use 'python replay.py --list' to see available sessions.")
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


if __name__ == "__main__":
    main()
