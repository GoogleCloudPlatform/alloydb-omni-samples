import asyncio
import csv
from asyncio.streams import StreamReader, StreamWriter
from datetime import datetime
import json
from typing import Optional, Dict, List, Tuple

REAL_PG_HOST = "127.0.0.1"
REAL_PG_PORT = 5432

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

# Maximum message size to prevent memory exhaustion (16MB)
MAX_MESSAGE_SIZE = 16 * 1024 * 1024

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


def is_ssl_request(payload: bytes) -> bool:
    """Check if payload is a Postgres SSLRequest (length=8, magic=0x04D2162F)."""
    return len(payload) == 8 and payload[4:8] == bytes.fromhex("04d2162f")


def is_startup_packet(payload: bytes) -> bool:
    """Check if payload is a Postgres StartupPacket (protocol version 0x00030000)."""
    return len(payload) >= 8 and payload[4:8] == bytes.fromhex("00030000")


def make_in_memory_record(data: bytes, is_client_to_server: bool = False):
    """
    Build your record structure from a raw chunk.
    (No timestamp here — capture_time is added later.)
    
    For client->server messages during startup phase (untyped):
    - SSLRequest: 8 bytes, magic 0x04D2162F
    - StartupPacket: length + proto_version 0x00030000 + params
    
    For typed messages:
    - First byte is the message type
    """
    msg_type = None
    sql = None
    description = None

    if len(data) > 0:
        # For client->server messages, check for special startup-phase messages first
        if is_client_to_server:
            if is_ssl_request(data):
                msg_type = "SSLRequest"
                description = "SSL Request"
            elif is_startup_packet(data):
                msg_type = "StartupPacket"
                description = "Startup Packet"
        
        # If not a special message, get type from first byte (for typed messages)
        if msg_type is None and len(data) > 0:
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


class ClientFramer:
    """
    Frames client→server Postgres messages.
    Handles startup-phase (untyped) and normal (typed) messages.
    """
    def __init__(self):
        self.buffer = bytearray()
        self.state = "startup"  # "startup" or "typed"
    
    def feed(self, data: bytes) -> List[bytes]:
        """
        Add data to buffer and extract complete messages.
        Returns list of complete message bytes.
        """
        self.buffer.extend(data)
        messages = []
        
        while True:
            if self.state == "startup":
                # Startup-phase: length(4) + body
                if len(self.buffer) < 4:
                    break
                
                length = int.from_bytes(self.buffer[0:4], "big")
                
                # Sanity check
                if length < 4 or length > MAX_MESSAGE_SIZE:
                    # Invalid length, skip this byte and try to resync
                    self.buffer.pop(0)
                    continue
                
                if len(self.buffer) < length:
                    break
                
                msg = bytes(self.buffer[:length])
                del self.buffer[:length]
                messages.append(msg)
                
                # Check if this is a StartupPacket (protocol version 0x00030000)
                if len(msg) >= 8 and msg[4:8] == bytes.fromhex("00030000"):
                    self.state = "typed"
            
            elif self.state == "typed":
                # Normal messages: type(1) + length(4) + payload
                if len(self.buffer) < 5:
                    break
                
                length = int.from_bytes(self.buffer[1:5], "big")
                total_size = 1 + length
                
                # Sanity check
                if length < 4 or total_size > MAX_MESSAGE_SIZE:
                    # Invalid length, skip this byte and try to resync
                    self.buffer.pop(0)
                    continue
                
                if len(self.buffer) < total_size:
                    break
                
                msg = bytes(self.buffer[:total_size])
                del self.buffer[:total_size]
                messages.append(msg)
        
        return messages
    
    def has_partial(self) -> bool:
        """Returns True if buffer has leftover partial data."""
        return len(self.buffer) > 0


class ServerFramer:
    """
    Frames server→client Postgres messages.
    Always uses typed framing: type(1) + length(4) + payload.
    """
    def __init__(self):
        self.buffer = bytearray()
    
    def feed(self, data: bytes) -> List[bytes]:
        """
        Add data to buffer and extract complete messages.
        Returns list of complete message bytes.
        """
        self.buffer.extend(data)
        messages = []
        
        while True:
            if len(self.buffer) < 5:
                break
            
            length = int.from_bytes(self.buffer[1:5], "big")
            total_size = 1 + length
            
            # Sanity check
            if length < 4 or total_size > MAX_MESSAGE_SIZE:
                # Invalid length, skip this byte and try to resync
                self.buffer.pop(0)
                continue
            
            if len(self.buffer) < total_size:
                break
            
            msg = bytes(self.buffer[:total_size])
            del self.buffer[:total_size]
            messages.append(msg)
        
        return messages
    
    def has_partial(self) -> bool:
        """Returns True if buffer has leftover partial data."""
        return len(self.buffer) > 0


async def forward(src: StreamReader, dst: StreamWriter, direction: str):
    """
    Forward data from src -> dst while logging individual messages.
    Uses proper Postgres wire protocol framing.
    """
    global TOTAL_RECORDS, SKIPPED_RECORDS

    # Determine framer type based on direction
    is_client_to_server = "client \u2192 server" in direction
    framer = ClientFramer() if is_client_to_server else ServerFramer()

    try:
        while True:
            # Read a chunk of bytes (may contain multiple messages or partial messages)
            data = await src.read(4096)
            if not data:
                print(f"[{direction}] connection closed")
                
                # Check for leftover partial data
                if framer.has_partial():
                    SKIPPED_RECORDS += 1
                    print(f"[{direction}] warning: {len(framer.buffer)} bytes of partial data discarded")
                break

            # Feed data to framer and extract complete messages
            messages = framer.feed(data)
            
            # Process each complete message
            for msg_bytes in messages:
                # Try to parse startup parameters (only relevant for client->server)
                if is_client_to_server:
                    params = try_parse_startup_params(msg_bytes)
                    if params:
                        if DB_META["db_user"] is None and params.get("user"):
                            DB_META["db_user"] = params.get("user")
                        if DB_META["db_name"] is None and params.get("database"):
                            DB_META["db_name"] = params.get("database")

                # Build record for this message (pass is_client_to_server for proper type detection)
                record = make_in_memory_record(msg_bytes, is_client_to_server=is_client_to_server)
                record["direction"] = direction
                record = add_meta_fields(record)

                # Save to capture file
                try:
                    save_query_json(record)
                    TOTAL_RECORDS += 1
                except Exception as e:
                    SKIPPED_RECORDS += 1
                    print(f"[{direction}] failed to write record: {e}")

                # Forward message to destination
                dst.write(msg_bytes)
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
    """
    Handle one client connection, forwarding to the configured upstream.
    """
    addr = client_writer.get_extra_info("peername")
    print(f"\n=== New client connection from {addr} ===")

    try:
        print("Connecting to real Postgres server...")
        server_reader, server_writer = await asyncio.open_connection(DB_META["db_host"], DB_META["db_port"])
        print(f"Connected to real Postgres at {DB_META['db_host']}:{DB_META['db_port']}.")

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


async def listen(port: int, real_host: str = REAL_PG_HOST, real_port: int = REAL_PG_PORT):
    # Update DB meta to reflect the chosen upstream.
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
