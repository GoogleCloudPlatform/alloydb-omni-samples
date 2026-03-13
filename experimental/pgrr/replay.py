import asyncio
import json
import argparse
from datetime import datetime
from typing import Dict, List, Optional, Tuple

CAPTURE_FILE = "queries.json"
TARGET_HOST = "127.0.0.1"
TARGET_PORT = 5432

REPLAY_LOG_FILE = "replay_log.jsonl"


def parse_iso(ts: str) -> datetime:
    return datetime.fromisoformat(ts)


def append_jsonl(path: str, record: Dict):
    """Append one JSON record as a JSONL line."""
    with open(path, "a", encoding="utf-8") as f:
        json.dump(record, f)
        f.write("\n")


def rec_time(rec: Dict) -> datetime:
    # Prefer capture_time if present, else timestamp.
    ts = rec.get("capture_time") or rec.get("timestamp")
    if not ts:
        raise ValueError("record missing capture_time/timestamp")
    return parse_iso(ts)


def load_sessions(path: str) -> Dict[int, List[Dict]]:
    """
    sessions[client_port] = list of client->server records sorted by time.
    """
    sessions: Dict[int, List[Dict]] = {}

    with open(path, "r", encoding="utf-8") as f:
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

            if "raw_hex" not in rec:
                print(f"[load] skipping line {line_no}: missing raw_hex")
                continue
            if "capture_time" not in rec and "timestamp" not in rec:
                print(f"[load] skipping line {line_no}: missing capture_time/timestamp")
                continue

            # direction looks like: "('127.0.0.1', 65098) client → server"
            try:
                inside = direction.split("(", 1)[1].split(")", 1)[0]
                port = int(inside.split(",")[1].strip())
            except Exception:
                print(f"[load] skipping line {line_no}: couldn't parse client port in direction={direction!r}")
                continue

            sessions.setdefault(port, []).append(rec)

    for port, recs in sessions.items():
        recs.sort(key=lambda r: (r.get("capture_time") or r.get("timestamp")))

    return sessions


def is_ssl_request(payload: bytes) -> bool:
    # SSLRequest = length(8) + magic(0x04D2162F)
    return len(payload) == 8 and payload[4:] == bytes.fromhex("04d2162f")


def is_startup_packet(payload: bytes) -> bool:
    # Startup has protocol version 0x00030000 at bytes [4:8]
    return len(payload) >= 8 and payload[4:8] == bytes.fromhex("00030000")


async def read_server_message(reader: asyncio.StreamReader) -> Tuple[bytes, bytes]:
    """
    Read one Postgres protocol message from server:
      1-byte type + 4-byte length + payload(length-4)
    Returns (type_byte, payload_bytes).
    """
    msg_type = await reader.readexactly(1)
    length_bytes = await reader.readexactly(4)
    length = int.from_bytes(length_bytes, "big")
    payload = await reader.readexactly(length - 4)
    return msg_type, payload


async def read_until_ready(reader: asyncio.StreamReader):
    """Read server responses until ReadyForQuery ('Z')."""
    while True:
        msg_type, _payload = await read_server_message(reader)
        if msg_type == b"Z":
            return


def compute_global_first_ts(sessions: Dict[int, List[Dict]]) -> datetime:
    """Earliest time across all sessions (for global ordering)."""
    return min(rec_time(recs[0]) for recs in sessions.values())


async def replay_session(
    session_port: int,
    records: List[Dict],
    global_first_ts: datetime,
    global_start: float,
    host: str,
    port: int,
    speed: float,
) -> Tuple[int, int, int]:
    """
    Replay a single session over its own outgoing connection.
    Returns: (session_port, sent_count, skipped_count)
    """
    loop = asyncio.get_event_loop()

    sent = 0
    skipped = 0

    # --- IMPORTANT: don't connect until it's time for this session's FIRST packet ---
    first_t = rec_time(records[0])
    first_offset = (first_t - global_first_ts).total_seconds() / speed
    now_offset = loop.time() - global_start
    sleep_s = first_offset - now_offset
    if sleep_s > 0:
        await asyncio.sleep(sleep_s)

    print(f"[replay] session={session_port}: connecting to target {host}:{port} ...")
    reader, writer = await asyncio.open_connection(host, port)
    print(f"[replay] session={session_port}: connected.")

    try:
        for rec in records:
            # Global timeline pacing (speed-adjusted)
            try:
                t = rec_time(rec)
            except Exception:
                skipped += 1
                continue

            target_offset = (t - global_first_ts).total_seconds() / speed
            now_offset = loop.time() - global_start
            sleep_s = target_offset - now_offset
            if sleep_s > 0:
                await asyncio.sleep(sleep_s)

            raw_hex = rec["raw_hex"]
            try:
                payload = bytes.fromhex(raw_hex)
            except Exception as e:
                skipped += 1
                print(f"[replay] session={session_port}: skipping bad hex ({e})")
                continue

            mt = rec.get("msg_type")

            # --- send ---
            try:
                writer.write(payload)
                await writer.drain()
                sent += 1
            except (ConnectionResetError, BrokenPipeError) as e:
                print(f"[replay] session={session_port}: connection lost after sent={sent}, skipped={skipped}: {e}")
                break

            # --- optional replay log ---
            replay_log = {
                "replay_time": datetime.now().isoformat(),
                "session_port": session_port,
                "direction": rec.get("direction"),
                "msg_type": mt,
                "description": rec.get("description"),
                "sql": rec.get("sql"),
                "raw_hex": raw_hex,
                "capture_time": rec.get("capture_time"),
                "timestamp": rec.get("timestamp"),
            }
            try:
                append_jsonl(REPLAY_LOG_FILE, replay_log)
            except Exception as e:
                print(f"[replay] session={session_port}: failed to write replay log: {e}")

            # --- protocol sync ---
            # After SSLRequest, server responds with 1 byte: b'S' or b'N'
            if mt == "\u0000" and is_ssl_request(payload):
                try:
                    await reader.readexactly(1)
                except Exception:
                    pass

            # After StartupPacket, wait until ReadyForQuery
            if mt == "\u0000" and is_startup_packet(payload):
                try:
                    await read_until_ready(reader)
                except Exception as e:
                    print(f"[replay] session={session_port}: failed during startup/auth: {e}")
                    break

            # After each Simple Query, wait until ReadyForQuery
            if mt == "Q":
                try:
                    await read_until_ready(reader)
                except Exception as e:
                    print(f"[replay] session={session_port}: failed waiting for ReadyForQuery: {e}")
                    break

            # After Terminate, stop this session
            if mt == "X":
                break

    finally:
        try:
            writer.close()
            await writer.wait_closed()
        except Exception:
            pass

    print(f"[replay] session={session_port}: done. sent={sent}, skipped={skipped}")
    return session_port, sent, skipped


async def replay_all_sessions(
    capture_path: str,
    host: str,
    port: int,
    speed: float,
):
    sessions = load_sessions(capture_path)
    if not sessions:
        print("No client → server records found. Nothing to replay.")
        return

    global_first_ts = compute_global_first_ts(sessions)

    ports_sorted = sorted(sessions.keys())
    print(f"[replay] multi-session: sessions={ports_sorted}")
    print(f"[replay] replay log file: {REPLAY_LOG_FILE}")
    print(f"[replay] speed multiplier: {speed}x")

    loop = asyncio.get_event_loop()
    global_start = loop.time()

    tasks = [
        replay_session(p, sessions[p], global_first_ts, global_start, host, port, speed)
        for p in ports_sorted
    ]

    results = await asyncio.gather(*tasks, return_exceptions=True)

    total_sent = 0
    total_skipped = 0
    for r in results:
        if isinstance(r, Exception):
            print(f"[replay] session task error: {r}")
            continue
        _p, sent, skipped = r
        total_sent += sent
        total_skipped += skipped

    print(f"[replay] ALL DONE. total_sent={total_sent}, total_skipped={total_skipped}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("-s", "--speed", type=float, default=1.0,
                        help="Replay speed multiplier (e.g., 2.0 = twice as fast). Default 1.0")
    parser.add_argument("--capture", type=str, default=CAPTURE_FILE,
                        help="Path to capture JSONL (default: queries.json)")
    parser.add_argument("--host", type=str, default=TARGET_HOST,
                        help="Target Postgres host (default: 127.0.0.1)")
    parser.add_argument("--port", type=int, default=TARGET_PORT,
                        help="Target Postgres port (default: 5432)")
    args = parser.parse_args()

    if args.speed <= 0:
        raise SystemExit("Speed must be > 0")

    asyncio.run(replay_all_sessions(args.capture, args.host, args.port, args.speed))


if __name__ == "__main__":
    main()
