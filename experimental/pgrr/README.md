# PGRR: PostgreSQL Query Recorder & Replayer

**PGRR** is a unified command-line tool for capturing and replaying PostgreSQL wire-protocol traffic. It runs as a transparent proxy to record client ↔ server messages and can later replay them against a target PostgreSQL instance for debugging, testing, and analysis.

## ⭐ Key Features

- **🎯 Unified Tool**: Single `pgrr.py` command for both capture and replay
- **🔍 Session Filtering**: List and filter captured sessions by client port
- **⚡ Speed Control**: Replay at original speed, faster, slower, or instantly
- **🧪 Dry Run Mode**: Preview queries before executing
- **📊 Smart Parsing**: Extracts SQL queries from PostgreSQL wire protocol
- **� Flexible Replay**: Target different databases, hosts, or users

---

## Installation

```bash
# Clone or navigate to the pgrr directory
cd pgrr

# Install dependencies
pip install -e .

# Or install psycopg2 manually if needed (for replay)
pip install psycopg2-binary
```

---

## Quick Start

### 1. Start a PostgreSQL Database

```bash
# Using Docker (recommended for testing)
docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16-alpine
```

### 2. Capture Traffic

```bash
# Start the proxy (listens on 5433, forwards to localhost:5432)
python3 pgrr.py capture
```

From another terminal, connect through the proxy:

```bash
# Connect to the proxy instead of the real database
psql -h localhost -p 5433 -U postgres -d postgres

# Run some queries
SELECT 1;
SELECT current_timestamp;
SELECT * FROM pg_tables LIMIT 5;
\q
```

Stop the proxy with `Ctrl+C`. Captured traffic is saved to `pgrr/queries.json`.

### 3. List Captured Sessions

```bash
# See what was captured
python3 pgrr.py replay --list --capture-file pgrr/queries.json
```

Output:
```
Found 1 client session(s):
  - Client port 54752: 3 SQL queries
```

### 4. Replay Queries

```bash
# Dry run first (see queries without executing)
python3 pgrr.py replay --dry-run --client-port 54752 --dbname postgres --user postgres

# Replay to the database
python3 pgrr.py replay --client-port 54752 --dbname postgres --user postgres

# Replay at 2x speed
python3 pgrr.py replay --client-port 54752 --dbname postgres --user postgres --speed 2.0

# Replay instantly (no delays)
python3 pgrr.py replay --client-port 54752 --dbname postgres --user postgres --speed 0
```

---

## Usage Guide

### Capture Command

Start a transparent proxy to capture PostgreSQL traffic:

```bash
# Default: Listen on 5433, forward to localhost:5432
python3 pgrr.py capture

# Forward to remote database
python3 pgrr.py capture --target-host db.example.com --target-port 5432

# Listen on custom port
python3 pgrr.py capture --listen-port 6543

# Custom capture file
python3 pgrr.py capture --capture-file my_queries.json
```

**Capture Options:**
- `--listen-port` - Port for proxy to listen on (default: 5433)
- `--target-host` - Target PostgreSQL host to forward to (default: 127.0.0.1)
- `--target-port` - Target PostgreSQL port to forward to (default: 5432)
- `--capture-file` - File to save captured traffic (default: pgrr/queries.json)

### Replay Command

Replay captured queries against a PostgreSQL database:

```bash
# List available sessions first
python3 pgrr.py replay --list --capture-file pgrr/queries.json

# Dry run (preview queries without executing)
python3 pgrr.py replay --dry-run --client-port 54752 --dbname postgres --user postgres

# Replay a specific session
python3 pgrr.py replay --client-port 54752 --dbname postgres --user postgres

# Replay at 2x speed
python3 pgrr.py replay --client-port 54752 --dbname postgres --user postgres --speed 2.0

# Replay instantly (no timing delays)
python3 pgrr.py replay --client-port 54752 --dbname postgres --user postgres --speed 0

# Replay to a different database/server
python3 pgrr.py replay --client-port 54752 --host remote-db.com --port 5433 --dbname mydb --user myuser
```

**Replay Options:**
- `--list` - List all captured sessions and exit
- `--client-port` - Replay only queries from this client port (recommended)
- `--capture-file` - Path to capture file (default: pgrr/queries.json)
- `--dbname` - Database name to connect to
- `--user` - Database user
- `--password` - Database password (optional, will prompt if needed)
- `--host` - Database host (default: localhost)
- `--port` - Database port (default: 5432)
- `--speed` - Speed multiplier for query timing (default: 1.0, 0 = instant)
- `--dry-run` - Show queries without executing them

---

## Common Workflows

### Workflow 1: Test Migration Script

1. **Capture from production (read-only):**
   ```bash
   python3 pgrr.py capture --target-host prod-db.example.com --capture-file prod_queries.json
   # Run your application or migration script
   # Press Ctrl+C when done
   ```

2. **Replay on staging:**
   ```bash
   python3 pgrr.py replay --list --capture-file prod_queries.json
   python3 pgrr.py replay --client-port <port> --host staging-db.example.com --dbname mydb --user myuser
   ```

### Workflow 2: Performance Testing

1. **Capture baseline workload:**
   ```bash
   python3 pgrr.py capture --capture-file baseline.json
   # Let application run for desired period
   ```

2. **Replay at different speeds:**
   ```bash
   # Normal speed
   python3 pgrr.py replay --client-port <port> --dbname test --user test --speed 1.0
   
   # 2x load
   python3 pgrr.py replay --client-port <port> --dbname test --user test --speed 0.5
   
   # 10x load
   python3 pgrr.py replay --client-port <port> --dbname test --user test --speed 0.1
   ```

### Workflow 3: Debug Issues

1. **Capture problematic queries:**
   ```bash
   python3 pgrr.py capture
   # Reproduce the issue
   ```

2. **Review captured queries:**
   ```bash
   python3 pgrr.py replay --dry-run --dbname postgres --user postgres
   ```

3. **Replay step-by-step:**
   ```bash
   # Edit pgrr/queries.json to isolate specific queries
   python3 pgrr.py replay --client-port <port> --dbname postgres --user postgres
   ```

---

## File Structure

After running PGRR, you'll have:

```
pgrr/
├── pgrr.py                 # Unified tool (capture + replay)
├── pgrr/
│   └── queries.json       # Captured traffic (default location)
├── requirements.txt       # Python dependencies
├── setup.py              # Package configuration
└── README.md             # This file
```

### Capture File Format

The capture file (`pgrr/queries.json`) contains newline-delimited JSON records:

```json
{
  "msg_type": "Q",
  "description": "Simple Query",
  "sql": "SELECT 1;",
  "raw_hex": "51000000...",
  "direction": "('127.0.0.1', 54752) client → server",
  "capture_time": "2024-01-15T10:30:45.123456",
  "db_host": "127.0.0.1",
  "db_port": 5432,
  "db_user": "postgres",
  "db_name": "postgres"
}
```

---

## Advanced Tips

### Multiple Sessions

If multiple clients connected during capture, filter by client port:

```bash
# List all sessions
python3 pgrr.py replay --list

# Replay only session from port 54752
python3 pgrr.py replay --client-port 54752 --dbname postgres --user postgres
```

### Custom Capture Location

```bash
# Capture to custom location
python3 pgrr.py capture --capture-file ~/captures/$(date +%Y%m%d_%H%M%S).json

# Replay from custom location
python3 pgrr.py replay --capture-file ~/captures/20240115_103045.json --list
```

### Remote Database Setup

```bash
# Capture from remote production (via SSH tunnel recommended)
ssh -L 5432:localhost:5432 prod-server
python3 pgrr.py capture --target-host localhost --target-port 5432

# Or capture directly (if network allows)
python3 pgrr.py capture --target-host prod-db.example.com --target-port 5432
```

---

## Troubleshooting

### Issue: "No SQL queries found"

**Cause:** The capture file may not contain SQL queries, or session filtering is too restrictive.

**Solution:**
```bash
# Check what's in the capture file
python3 pgrr.py replay --list --capture-file pgrr/queries.json

# Try without session filtering
python3 pgrr.py replay --dbname postgres --user postgres
```

### Issue: "Failed to connect during replay"

**Cause:** Database connection parameters are incorrect.

**Solution:**
```bash
# Verify database is running
psql -h localhost -p 5432 -U postgres -d postgres

# Check replay parameters match
python3 pgrr.py replay --host localhost --port 5432 --dbname postgres --user postgres
```

### Issue: "psycopg2 not found"

**Cause:** psycopg2 is not installed (only needed for replay).

**Solution:**
```bash
pip install psycopg2-binary
```

---

## Legacy Scripts

For backward compatibility, the following scripts are still available:

- `pgrr/proxy.py` - Standalone proxy (use `pgrr.py capture` instead)
- `sql_replay.py` - Standalone SQL replay (use `pgrr.py replay` instead)
- `replay.py` - Raw protocol replay (experimental)

**Recommendation:** Use the unified `pgrr.py` tool for all new workflows.

---

## What to Push to Git

**Essential files:**
```
✅ pgrr.py
✅ pgrr/__init__.py
✅ pgrr/cli.py (if separate)
✅ requirements.txt
✅ setup.py
✅ pyproject.toml
✅ README.md
✅ *.md (documentation)
```

**Don't push:**
```
❌ pgrr/queries.json (captured data)
❌ *.dump (database dumps)
❌ __pycache__/
❌ *.pyc
❌ *.egg-info/
```

**Recommended `.gitignore`:**
```gitignore
# Captured data
queries.json
pgrr/queries.json
*.dump

# Python
__pycache__/
*.py[cod]
*.egg-info/
.pytest_cache/
.venv/
venv/

# OS
.DS_Store
```

---

## Contributing

Contributions are welcome! Please ensure:
- Code follows existing style and structure
- New features include documentation
- Tests pass (if applicable)

---

## License

[Your License Here]

---

## Support & Documentation

- **Main Guide:** This README
- **Detailed Workflow:** [COMPLETE_FLOW.md](COMPLETE_FLOW.md)
- **Step-by-Step Tutorial:** [END_TO_END_FLOW.md](END_TO_END_FLOW.md)
- **Technical Details:** [IMPROVEMENTS_SUMMARY.md](IMPROVEMENTS_SUMMARY.md)

For issues or questions, please open a GitHub issue.
