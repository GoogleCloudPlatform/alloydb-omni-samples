import asyncio
import json
from datetime import datetime
from typing import Dict, List, Optional
import argparse

CAPTURE_FILE = "queries.json"

TARGET_HOST = "127.0.0.1"
TARGET_PORT = 5432  # target Postgres server port


def parse_iso(ts: str) -> datetime:
    # your capture uses datetime.now().isoformat()
    return datetime.fromisoformat(ts)


def load_client_to_server_records(path: str, client_port: Optional[int] = None) -> List[Dict]:
    """
    Load newline-delimited JSON records from `queries.json`
    and keep only client → server direction records.

    If client_port is provided, only keep records from that client connection.
    """
    out: List[Dict] = []
    with open(path, "r") as f:
        for line_no, line in enumerate(f, start=1):
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except Exception as e:
                print(f"[load] skipping bad json line {line_no}: {e}")
                continue

            direction = rec.get("direction", "")
            if "client \u2192 server" not in direction:
                continue

            if client_port is not None and f", {client_port})" not in direction:
                continue

            if "timestamp" not in rec or "raw_hex" not in rec:
                print(f"[load] skipping line {line_no}: missing timestamp/raw_hex")
                continue

            out.append(rec)

    out.sort(key=lambda r: r["timestamp"])
    return out



async def drain_server(reader: asyncio.StreamReader):
    """
    Drain server responses so buffers don't fill up and stall replay.
    """
    try:
        while True:
            data = await reader.read(4096)
            if not data:
                break
    except Exception:
        pass


async def replay_one_connection(
    capture_path: str = CAPTURE_FILE,
    host: str = TARGET_HOST,
    port: int = TARGET_PORT,
    speed_multiplier: float = 1.0
):
    records = load_client_to_server_records(capture_path, client_port=50280)
    if not records:
        print("No client → server records found. Nothing to replay.")
        return

    first_t = parse_iso(records[0]["timestamp"])

    print(f"[replay] connecting to target {host}:{port} ...")
    reader, writer = await asyncio.open_connection(host, port)
    print("[replay] connected.")

    drain_task = asyncio.create_task(drain_server(reader))

    loop = asyncio.get_event_loop()
    start = loop.time()

    sent = 0
    skipped = 0

    try:
        for rec in records:
            # compute when this packet should be sent relative to first packet
            t = parse_iso(rec["timestamp"])
            target_offset = (t - first_t).total_seconds()
            target_offset /= speed_multiplier

            now_offset = loop.time() - start
            sleep_s = target_offset - now_offset
            if sleep_s > 0:
                await asyncio.sleep(sleep_s)

            raw_hex = rec["raw_hex"]
            try:
                payload = bytes.fromhex(raw_hex)
            except Exception as e:
                skipped += 1
                print(f"[replay] skipping record: bad hex ({e})")
                continue
            if rec.get("sql"):
                print("[replay sql]", rec["sql"])

            writer.write(payload)
            await writer.drain()
            sent += 1

        print(f"[replay] done. sent={sent}, skipped={skipped}")

    finally:
        try:
            writer.close()
            await writer.wait_closed()
        except Exception:
            pass
        drain_task.cancel()

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("-s", "--speed", type = float, default = 1.0, help = "Speed Modifier for replay. Default speed set to 1.0")
    args = parser.parse_args()

    asyncio.run(replay_one_connection())
