import asyncio

import socket
import csv
from asyncio.streams import StreamReader, StreamWriter
from datetime import datetime
import json

REAL_PG_HOST = "127.0.0.1"
REAL_PG_PORT = 5432   # Your actual Postgres server


def save_query_csv(query: str):
    """
    Append query to CSV with timestamp
    """
    with open("queries.csv", "a", newline="") as f:
        writer = csv.writer(f)
        writer.writerow([datetime.now().isoformat(), query])
def save_query_json(record: dict, filename="queries.json"):
    """
    Append a JSON record to a file (newline-delimited JSON format).
    """
    with open(filename, "a") as f:
        json.dump(record, f)
        f.write("\n")  # add newline so each record is separate



def make_in_memory_record(data: bytes):
    msg_type = None
    sql = None
    description = None

    if len(data) > 0:
        # First byte = message type
        msg_type = data[0:1].decode("ascii", errors="replace")

        # Decode SQL if it's a simple query
        if msg_type == "Q" and len(data) > 5:
            sql_bytes = data[5:]
            sql = sql_bytes.split(b"\x00")[0].decode("utf-8", errors="replace")

        # Add human-friendly description for common server messages
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
        "timestamp": datetime.now().isoformat(),
        "msg_type": msg_type,
        "description": description,
        "sql": sql,
        #raw bytes and raw hex is the same context, however, raw hex is better format for replay because it encodes each byte exactly 
        #"raw bytes": str(data),
       # "length": len(data),
        "raw_hex": data.hex()
    }


async def forward(src: StreamReader, dst: StreamWriter, direction: str):
    """
    Forward data from src -> dst while logging the bytes.
    """
    try:
        while True:
            # Read up to 4096 bytes at a timepsql -h localhost -p 5432 -U postgres
            data = await src.read(4096)
            # If no data received, connection is closed
            if not data:
                # Connection closed in this direction
                print(f"[{direction}] connection closed")
                break

            print(f"\n[{direction}] {len(data)} bytes")
            print(f"Raw bytes: {data}")


            record = make_in_memory_record(data)
            record["direction"] = direction  # optional, helps track flow
            print("\nIN MEMORY:", record)

            save_query_json(record)
            print("\nON DISK:", record)

            try:
                decoded = data.decode("utf-8", errors="replace")
                print(f"\nDecoded: {decoded}")
            except:
                pass

            dst.write(data)
            await dst.drain()

    except Exception as e:
        print(f"[{direction}] error: {e}")

    finally:
        try:
            dst.close()
            await dst.wait_closed()
        except:
            pass


async def handle_socket(client_reader: StreamReader, client_writer: StreamWriter):
    """
    Handle one incoming client connection.
    Create a connection to the real Postgres server and forward data both ways.
    """
    addr = client_writer.get_extra_info("peername")
    print(f"\n=== New client connection from {addr} ===")

    try:
        # Connect to real Postgres server
        print("Connecting to real Postgres server...")
        server_reader, server_writer = await asyncio.open_connection(
            REAL_PG_HOST, REAL_PG_PORT
        )
        print("Connected to real Postgres.")

        # Two forwarding tasks:
        # client → server
        # server → client
        client_to_server = forward(
            client_reader, server_writer, f"{addr} client → server"
        )
        server_to_client = forward(
            server_reader, client_writer, f"{addr} server → client"
        )

        # Run both directions until one side closes
        await asyncio.gather(client_to_server, server_to_client)

    except Exception as e:
        print(f"Error handling connection from {addr}: {e}")

    finally:
        print(f"=== Closing connection for {addr} ===")
        try:
            client_writer.close()
            await client_writer.wait_closed()
        except:
            pass


async def listen(port: int):
    """
    Listen on our proxy port and accept connections.
    """
    server = await asyncio.start_server(handle_socket, "0.0.0.0", port)
    print(f"Transparent PG proxy listening on 0.0.0.0:{port}")
    async with server:
        await server.serve_forever()


if __name__ == "__main__":
    try:
        asyncio.run(listen(5433))
    except KeyboardInterrupt:
        print("\nShutting down proxy...")
