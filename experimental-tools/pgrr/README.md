# pgrr - PostgreSQL Query Replay & Recording

A tool for capturing and replaying PostgreSQL query traffic.

## Successfully Running Replay

### Terminal 1: Start the Proxy

```bash
python3 proxy.py
```

This starts a proxy server on `localhost:5433` that forwards to `localhost:5432` and captures all traffic to `queries.json`.

### Terminal 2: Run Queries to Capture

Connect to your database through the proxy:

```bash
psql -h 127.0.0.1 -p 5433 chinook
```

Add whatever queries you want to replay:

```sql
SELECT COUNT(*) FROM track;
SELECT * FROM album LIMIT 10;
-- Add any other queries you want to capture
```

### Terminal 3: Replay the Captured Queries

```bash
python3 replay.py
```

## Speed Control

Speed can be modified by using the `-s` flag:

```bash
# 2x faster
python3 replay.py -s 2

# 5x faster
python3 replay.py -s 5

# Half speed
python3 replay.py -s 0.5
```