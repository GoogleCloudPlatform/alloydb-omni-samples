import asyncio
import csv
from asyncio.streams import StreamReader, StreamWriter
from datetime import datetime
import json
from typing import Optional, Dict

REAL_PG_HOST = "127.0.0.1"
REAL_PG_PORT = 5432   # Your actual Postgres server

CAPTURE_FILE = "queries.json"

# --- Per-record capture_time only (no timestamp, no session start time) ---
DB_META = {
    "db_host": REAL_PG_HOST,
    "db_port": REAL_PG_PORT,
    "db_user": None,   # filled in if we can parse StartupPacket
    "db_name": None,   # filled in if we can parse StartupPacket
}

TOTAL_RECORDS = 0
SKIPPED_RECORDS = 0


def save_query_csv(query: str):
    """Append query to CSV with timestamp"""
    with open("queries.csv", "a", newline="") as f:
        writer = csv.writer(f)
        writer.writerow([datetime.now().isoformat(), query])


def save_query_json(record: dict, filename=CAPTURE_FILE):
    """Append a JSON record to a file (newline-delimited JSON format)."""
    with open(filename, "a", encoding="utf-8") as f:
        json.dump(record, f)
        f.write("\n")


def try_parse_startup_params(chunk: bytes) -> Optional[Dict[str, str]]:
    """
    Best-effort parse of a Postgres StartupPacket from a raw chunk.

    StartupPacket on the wire (untyped):
      int32 length
      int32 protocol_version (0x00030000)
      key\0value\0 ... \0

    This only works if the chunk begins exactly at the start of the StartupPacket.
    """
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
    """
    Build your record structure from a raw chunk.
    (No timestamp here — capture_time is added later.)
    """
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
    """
    Add per-record capture_time + db details to each record.
    """
    record["capture_time"] = datetime.now().isoformat()  # <-- single authoritative time field
    record.update(DB_META)
    return record


async def forward(src: StreamReader, dst: StreamWriter, direction: str):
    """
    Forward data from src -> dst while logging the bytes.
    """
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
    addr = client_writer.get_extra_info("peername")
    print(f"\n=== New client connection from {addr} ===")

    try:
        print("Connecting to real Postgres server...")
        server_reader, server_writer = await asyncio.open_connection(REAL_PG_HOST, REAL_PG_PORT)
        print("Connected to real Postgres.")

        client_to_server = forward(client_reader, server_writer, f"{addr} client \u2192 server")
        server_to_client = forward(server_reader, client_writer, f"{addr} server \u2192 client")

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


async def listen(port: int):
    server = await asyncio.start_server(handle_socket, "0.0.0.0", port)
    print(f"Transparent PG proxy listening on 0.0.0.0:{port}")
    print(f"Capture file: {CAPTURE_FILE}")
    print("Capture time: per-record (capture_time field)")
    async with server:
        await server.serve_forever()


def write_summary_record():
    """
    Append ONE summary record at the end (same structure).
    """
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


if __name__ == "__main__":
    try:
        asyncio.run(listen(5433))
    except KeyboardInterrupt:
        print("\nShutting down proxy...")
    finally:
        write_summary_record()
        print(f"[summary] total_records={TOTAL_RECORDS}, skipped_records={SKIPPED_RECORDS}")
