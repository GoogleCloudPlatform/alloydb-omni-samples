#!/usr/bin/env python3
"""
Smart wrapper for PostgreSQL query replay.
Automatically detects sessions and suggests the best one to replay.
"""
import subprocess
import sys
import json
import re
from collections import defaultdict


def get_sessions(capture_file="queries.json"):
    """Get session information from capture file."""
    sessions = defaultdict(lambda: {"sql_count": 0, "packet_count": 0, "queries": []})
    
    try:
        with open(capture_file, "r") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                
                try:
                    rec = json.loads(line)
                except Exception:
                    continue
                
                # Extract client port
                direction = rec.get("direction", "")
                if "client → server" not in direction:
                    continue
                
                match = re.search(r"\('127\.0\.0\.1', (\d+)\) client → server", direction)
                if not match:
                    continue
                
                port = int(match.group(1))
                sessions[port]["packet_count"] += 1
                
                if rec.get("sql"):
                    sessions[port]["sql_count"] += 1
                    sessions[port]["queries"].append(rec["sql"][:80])
        
        return dict(sessions)
    
    except FileNotFoundError:
        print(f"ERROR: Capture file '{capture_file}' not found.")
        print("\nMake sure you've captured traffic using proxy.py first:")
        print("  1. Start proxy: python proxy.py")
        print("  2. Connect through proxy: psql -h localhost -p 5433 -U youruser -d yourdb")
        print("  3. Run some queries")
        print("  4. Use this tool to replay\n")
        return None


def main():
    print("=" * 70)
    print("PostgreSQL Query Replay - Smart Wrapper")
    print("=" * 70)
    print()
    
    # Get sessions
    sessions = get_sessions()
    if sessions is None:
        sys.exit(1)
    
    if not sessions:
        print("No client sessions found in queries.json")
        sys.exit(1)
    
    # Display sessions
    print(f"Found {len(sessions)} client session(s):\n")
    
    sorted_sessions = sorted(sessions.items(), key=lambda x: x[1]["sql_count"], reverse=True)
    
    for i, (port, info) in enumerate(sorted_sessions, 1):
        print(f"{i}. Client port {port}:")
        print(f"   - {info['packet_count']} total packets")
        print(f"   - {info['sql_count']} SQL queries")
        
        if info["queries"]:
            print(f"   - Sample queries:")
            for query in info["queries"][:3]:
                print(f"     • {query}...")
            if len(info["queries"]) > 3:
                print(f"     ... and {len(info['queries']) - 3} more")
        print()
    
    # Suggest best session
    best_port = sorted_sessions[0][0]
    best_info = sorted_sessions[0][1]
    
    if best_info["sql_count"] == 0:
        print("⚠️  No SQL queries found in any session.")
        print("   The capture may contain only connection handshakes.")
        print()
        choice = input("Continue with raw protocol replay anyway? [y/N]: ").strip().lower()
        if choice != 'y':
            sys.exit(0)
    
    print("=" * 70)
    print("Recommended: SQL Re-execution (reliable, shows results)")
    print("=" * 70)
    print()
    
    # Prompt for database connection details
    print("Enter database connection details:")
    dbname = input(f"  Database name [chinook]: ").strip() or "chinook"
    user = input(f"  User [kourosh]: ").strip() or "kourosh"
    host = input(f"  Host [localhost]: ").strip() or "localhost"
    port_str = input(f"  Port [5432]: ").strip() or "5432"
    port = int(port_str)
    
    speed_str = input(f"  Speed multiplier [0=instant, 1=original, 2=2x]: ").strip() or "0"
    speed = float(speed_str)
    
    print()
    print("=" * 70)
    
    # Build command
    cmd = [
        "python", "sql_replay.py",
        "--client-port", str(best_port),
        "--dbname", dbname,
        "--user", user,
        "--host", host,
        "--port", str(port),
        "--speed", str(speed)
    ]
    
    print(f"Executing: {' '.join(cmd)}")
    print("=" * 70)
    print()
    
    # Execute
    try:
        subprocess.run(cmd)
    except KeyboardInterrupt:
        print("\n\nInterrupted by user.")
    except Exception as e:
        print(f"\nERROR: {e}")
        sys.exit(1)
    
    print()
    print("=" * 70)
    print("Options for different replay methods:")
    print("=" * 70)
    print()
    print("1. SQL Re-execution (what we just used):")
    print(f"   python sql_replay.py --client-port {best_port} --dbname {dbname} --user {user}")
    print()
    print("2. Raw protocol replay (limited, for timing analysis):")
    print(f"   python replay.py --client-port {best_port}")
    print()
    print("3. View all options:")
    print("   python replay.py --help")
    print("   python sql_replay.py --help")
    print()


if __name__ == "__main__":
    main()
