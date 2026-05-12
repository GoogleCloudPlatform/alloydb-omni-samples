# pgrr.py - Unified PostgreSQL Record & Replay Tool

## Overview

`pgrr.py` is a **unified command-line tool** that combines proxy/capture and replay functionality into a single interface with subcommands.

## Quick Start

### 1. Capture Queries

```bash
# Start proxy (Terminal 1)
python3 pgrr.py capture

# Connect through proxy and run queries (Terminal 2)
psql -h localhost -p 5433 -U myuser -d mydb
# Run your queries...
\q
```

### 2. List Captured Sessions

```bash
# See what was captured
python3 pgrr.py replay --list
```

### 3. Replay Queries

```bash
# Replay queries from a specific session
python3 pgrr.py replay --client-port 54752 --dbname mydb --user myuser
```

---

## Command Reference

### Global Help

```bash
python3 pgrr.py --help
python3 pgrr.py capture --help
python3 pgrr.py replay --help
```

---

## Capture Command

Start a proxy server to capture PostgreSQL wire protocol traffic.

### Syntax

```bash
python3 pgrr.py capture [OPTIONS]
```

### Options

| Option | Description | Default |
|--------|-------------|---------|
| `--listen-port PORT` | Port for proxy to listen on | 5433 |
| `--target-host HOST` | PostgreSQL host to forward to | 127.0.0.1 |
| `--target-port PORT` | PostgreSQL port to forward to | 5432 |
| `--capture-file FILE` | File to save captured traffic | queries.json |

### Examples

```bash
# Default: Listen on 5433, forward to localhost:5432
python3 pgrr.py capture

# Capture from remote database
python3 pgrr.py capture --target-host prod-db.company.com --target-port 5432

# Custom listen port
python3 pgrr.py capture --listen-port 6543

# Custom capture file
python3 pgrr.py capture --capture-file my_capture.json

# Complete custom setup
python3 pgrr.py capture \
  --listen-port 6543 \
  --target-host 192.168.1.100 \
  --target-port 5433 \
  --capture-file prod_queries.json
```

---

## Replay Command

Replay captured SQL queries to a PostgreSQL database.

### Syntax

```bash
python3 pgrr.py replay [OPTIONS]
```

### Options

| Option | Description | Default | Required |
|--------|-------------|---------|----------|
| `--list` | List available sessions and exit | - | No |
| `--client-port PORT` | Filter to specific client session | All | Recommended |
| `--capture-file FILE` | Capture file to read from | queries.json | No |
| `--dbname DATABASE` | Database name | - | Yes* |
| `--user USERNAME` | Database user | - | Yes* |
| `--password PASSWORD` | Database password | Prompt | No |
| `--host HOST` | Database host | localhost | No |
| `--port PORT` | Database port | 5432 | No |
| `--speed MULTIPLIER` | Speed multiplier (0=instant) | 1.0 | No |
| `--dry-run` | Show queries without executing | - | No |

*Not required with `--list`

### Examples

#### List Sessions

```bash
# List all captured sessions
python3 pgrr.py replay --list

# List from custom capture file
python3 pgrr.py replay --list --capture-file my_capture.json
```

#### Dry Run

```bash
# See what would be executed (no database connection)
python3 pgrr.py replay --dry-run --dbname mydb --user myuser

# Dry run from specific session
python3 pgrr.py replay --dry-run --client-port 54752 --dbname mydb --user myuser
```

#### Execute Queries

```bash
# Execute queries from a specific session (instant, no delays)
python3 pgrr.py replay \
  --client-port 54752 \
  --dbname mydb \
  --user myuser \
  --speed 0

# Execute at original timing
python3 pgrr.py replay \
  --client-port 54752 \
  --dbname mydb \
  --user myuser \
  --speed 1.0

# Execute at 2x speed
python3 pgrr.py replay \
  --client-port 54752 \
  --dbname mydb \
  --user myuser \
  --speed 2.0

# Replay to different server
python3 pgrr.py replay \
  --client-port 54752 \
  --host remote-db.company.com \
  --port 5432 \
  --dbname mydb \
  --user myuser

# Replay from custom capture file
python3 pgrr.py replay \
  --capture-file my_capture.json \
  --client-port 54752 \
  --dbname mydb \
  --user myuser
```

---

## Complete Workflows

### Workflow 1: Capture from Production, Replay to Dev

```bash
# Step 1: Capture from production
python3 pgrr.py capture \
  --target-host prod-db.company.com \
  --target-port 5432 \
  --capture-file prod_queries.json

# (In another terminal) Connect and run queries
psql -h localhost -p 5433 -U prod_user -d production
# ... run queries ...
\q

# Step 2: List captured sessions
python3 pgrr.py replay --list --capture-file prod_queries.json

# Step 3: Replay to dev
python3 pgrr.py replay \
  --capture-file prod_queries.json \
  --client-port 54752 \
  --host dev-db.company.com \
  --dbname development \
  --user dev_user \
  --speed 0
```

### Workflow 2: Local Testing

```bash
# Step 1: Capture locally
python3 pgrr.py capture

# Step 2: Run queries
psql -h localhost -p 5433 -U myuser -d mydb
SELECT * FROM users LIMIT 10;
\q

# Step 3: List sessions
python3 pgrr.py replay --list

# Step 4: Dry run
python3 pgrr.py replay --dry-run --client-port 54752 --dbname mydb --user myuser

# Step 5: Execute
python3 pgrr.py replay --client-port 54752 --dbname mydb --user myuser --speed 0
```

---

## Comparison with Old Scripts

| Task | Old Way | New Way |
|------|---------|---------|
| Start capture | `python3 proxy.py` | `python3 pgrr.py capture` |
| Capture to custom file | `python3 proxy.py --capture-file X` | `python3 pgrr.py capture --capture-file X` |
| List sessions | `python3 replay.py --list` | `python3 pgrr.py replay --list` |
| Replay queries | `python3 sql_replay.py --client-port X ...` | `python3 pgrr.py replay --client-port X ...` |
| Dry run | `python3 sql_replay.py --dry-run ...` | `python3 pgrr.py replay --dry-run ...` |

---

## Benefits of Unified Tool

✅ **Single Entry Point** - One script for all operations
✅ **Consistent Interface** - Same command structure for capture and replay
✅ **Better Help** - Contextual help for each subcommand
✅ **Easier to Remember** - `pgrr capture` and `pgrr replay` instead of multiple scripts
✅ **Professional CLI** - Follows standard CLI conventions with subcommands

---

## Installation

### Option 1: Direct Use

```bash
# Make executable
chmod +x pgrr.py

# Use directly
./pgrr.py capture
./pgrr.py replay --list
```

### Option 2: Create Alias

```bash
# Add to ~/.zshrc or ~/.bashrc
alias pgrr='python3 /path/to/pgrr.py'

# Then use as:
pgrr capture
pgrr replay --list
```

### Option 3: Install to PATH

```bash
# Copy to a directory in PATH
sudo cp pgrr.py /usr/local/bin/pgrr
sudo chmod +x /usr/local/bin/pgrr

# Use anywhere
pgrr capture
pgrr replay --list
```

---

## Requirements

- Python 3.7+
- `psycopg2-binary` (for replay functionality)

```bash
pip install psycopg2-binary
```

---

## Tips & Best Practices

### Always Filter by Client Port

```bash
# ✅ GOOD - Filters to single session
pgrr replay --client-port 54752 --dbname mydb --user myuser

# ❌ BAD - May replay mixed sessions
pgrr replay --dbname mydb --user myuser
```

### Use Dry Run First

```bash
# Always check what will be executed
pgrr replay --dry-run --client-port 54752 --dbname mydb --user myuser

# Then execute
pgrr replay --client-port 54752 --dbname mydb --user myuser --speed 0
```

### Speed Control

```bash
# Instant (no delays) - good for testing
--speed 0

# Original timing - good for performance analysis
--speed 1.0

# 2x faster - moderate stress test
--speed 2.0

# 10x faster - aggressive stress test
--speed 10.0
```

---

## Troubleshooting

### "psycopg2 is not installed"

```bash
pip install psycopg2-binary
```

### "No client sessions found"

```bash
# Check the capture file exists and has content
cat queries.json

# Make sure you captured traffic through the proxy
```

### "Connection failed"

```bash
# Test database connection first
psql -h target-host -p target-port -U user -d database
```

---

## Quick Reference Card

```bash
# CAPTURE
pgrr capture                                    # Start proxy (default settings)
pgrr capture --target-host X --target-port Y   # Capture from remote DB

# REPLAY
pgrr replay --list                              # List sessions
pgrr replay --dry-run --dbname X --user Y       # Preview queries
pgrr replay --client-port Z --dbname X --user Y # Execute queries

# COMMON OPTIONS
--capture-file FILE        # Custom capture file
--speed N                  # Speed multiplier (0=instant)
--host HOST               # Database host
--port PORT               # Database port
```

---

## Summary

The unified `pgrr.py` tool provides a **professional, easy-to-use interface** for PostgreSQL query capture and replay:

- 🎯 **Simple** - Two commands: `capture` and `replay`
- 🔧 **Flexible** - Works with any PostgreSQL database
- 📊 **Powerful** - Speed control, dry-run, session filtering
- 💡 **Intuitive** - Follows standard CLI patterns

Use it for migration testing, performance benchmarking, cross-environment validation, and more!
