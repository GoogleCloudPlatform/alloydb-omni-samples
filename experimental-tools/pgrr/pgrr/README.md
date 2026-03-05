# pgrr: postgres record & replay

pgrr -- command-line tool that captures and replays Postgres wire-protocol traffic. 
- Runs as a transparent proxy to record client ↔ server messages and can later replay them against a target Postgres instance for debugging and testing.

## Installation

### Python
```bash
  pip install -e .
```

## Quick Start
1. **Start a Postgres Instance (Docker Example)**
```bash
docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16-alpine
```

2. **Capture Traffic Via The Proxy**
```bash
# listen on 5433, forward to the Postgres above on 5432
pgrr capture --port 5433
# to forward elsewhere, supply --upstream-host/--upstream-port
# pgrr capture --port 5433 --upstream-host 10.0.0.5 --upstream-port 5432
```
From another terminal, set client to the proxy:
```bash
PGPASSWORD=postgres psql -h 127.0.0.1 -p 5433 -U postgres postgres
SELECT 1;
SELECT now();
\q
```
`queries.json` fills with the captured wire messages.

3. **Replay Into a Target (same DB or a test DB)**
```bash
# replay into default target 127.0.0.1:5432
pgrr replay --capture queries.json --speed 1.0

# or replay into a dedicated test Postgres on a different port
pgrr replay --capture queries.json --host 127.0.0.1 --port 5434 --speed 0.5
```
Each captured client session is replayed on its own connection with timing preserved (scaled by `--speed`), and replay activity is logged to `replay_log.jsonl`.

## TLDR: Running PGRR

- One Terminal => pgrr capture --port 5433
- Second Terminal => psql -h 127.0.0.1 -p 5433 postgres
- Add All Queries for Replay.
- Stop Capture (Ctrl+C)
- Third Terminal => pgrr replay --speed 1.0


## Usage

- `pgrr --help` to see all commands.
- `pgrr capture --port <proxy_port> [--upstream-host <host>] [--upstream-port <port>]` starts the proxy (default listen `5433`) and forwards to `127.0.0.1:5432` by default. Captured records include `capture_time`, `direction`, `msg_type`, `raw_hex`, and decoded SQL when present.
- `pgrr replay --capture <file> --host <host> --port <port> --speed <x>` replays the captured sessions. Defaults: capture `queries.json`, host `127.0.0.1`, port `5432`, speed `1.0`.

## Notes

- To run a disposable Postgres for testing: `docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16-alpine`
- If you want to keep your real database untouched, run another Postgres on a different port (e.g., `-p 5434:5432`) and point replay at it with `--host 127.0.0.1 --port 5434`.
- Ensure the target Postgres authentication settings accept the user/password used during capture, otherwise replay will fail during the startup/auth sequence.
