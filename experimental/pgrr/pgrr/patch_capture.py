"""
patch_capture.py — rewrite startup-packet fields in a capture file.

Usage:
  python -m pgrr.patch_capture queries.json patched.json --db testdb2 --user kourosh
"""
import json
import argparse
import struct
import sys


def patch_startup_packet(raw_hex: str, new_db: str | None, new_user: str | None) -> str:
    """
    Rewrite the database and/or user fields in a Postgres StartupPacket.
    StartupPacket layout: int32 length | int32 protocol | key\0value\0... \0
    """
    data = bytearray(bytes.fromhex(raw_hex))

    if len(data) < 8:
        return raw_hex
    if data[4:8] != bytes.fromhex("00030000"):
        return raw_hex  # not a startup packet

    # Parse key/value pairs from offset 8
    pairs: list[tuple[str, str]] = []
    rest = data[8:]
    parts = rest.split(b"\x00")
    i = 0
    while i < len(parts) - 1:
        k = parts[i].decode("utf-8", errors="ignore")
        v = parts[i + 1].decode("utf-8", errors="ignore") if i + 1 < len(parts) else ""
        if not k:
            break
        pairs.append((k, v))
        i += 2

    # Apply overrides
    new_pairs = []
    for k, v in pairs:
        if k == "database" and new_db:
            v = new_db
        if k == "user" and new_user:
            v = new_user
        new_pairs.append((k, v))

    # Rebuild body: key\0value\0 ... \0
    body = b""
    for k, v in new_pairs:
        body += k.encode() + b"\x00" + v.encode() + b"\x00"
    body += b"\x00"  # terminating NUL

    # Rebuild full packet: length(4) + protocol(4) + body
    protocol = bytes(data[4:8])
    total_len = 4 + 4 + len(body)
    packet = struct.pack(">I", total_len) + protocol + body
    return packet.hex()


def patch_file(src: str, dst: str | None, new_db: str | None, new_user: str | None):
    """
    Patch startup packet(s) in src.
    If dst is None, patch src in-place (only the matching lines are rewritten).
    If dst is provided, write the full patched copy to dst.
    """
    patched = 0

    if dst is None:
        # In-place: read all lines, rewrite only changed ones back
        with open(src, "r", encoding="utf-8") as f:
            lines = f.readlines()

        new_lines = []
        for line in lines:
            stripped = line.strip()
            if not stripped:
                new_lines.append(line)
                continue
            rec = json.loads(stripped)
            raw_hex = rec.get("raw_hex", "")
            new_hex = patch_startup_packet(raw_hex, new_db, new_user)
            if new_hex != raw_hex:
                rec["raw_hex"] = new_hex
                if new_db:
                    rec["db_name"] = new_db
                if new_user:
                    rec["db_user"] = new_user
                patched += 1
                new_lines.append(json.dumps(rec) + "\n")
            else:
                new_lines.append(line)

        with open(src, "w", encoding="utf-8") as f:
            f.writelines(new_lines)
        print(f"[patch] updated {src!r} in-place ({patched} startup packet(s) patched)")
    else:
        with open(src, "r", encoding="utf-8") as fin, open(dst, "w", encoding="utf-8") as fout:
            for line in fin:
                stripped = line.strip()
                if not stripped:
                    continue
                rec = json.loads(stripped)
                raw_hex = rec.get("raw_hex", "")
                new_hex = patch_startup_packet(raw_hex, new_db, new_user)
                if new_hex != raw_hex:
                    rec["raw_hex"] = new_hex
                    if new_db:
                        rec["db_name"] = new_db
                    if new_user:
                        rec["db_user"] = new_user
                    patched += 1
                fout.write(json.dumps(rec) + "\n")
        print(f"[patch] wrote {dst!r} ({patched} startup packet(s) patched)")


def main():
    parser = argparse.ArgumentParser(description="Patch database/user in a pgrr capture file")
    parser.add_argument("input", help="Source capture JSONL file")
    parser.add_argument("output", nargs="?", default=None,
                        help="Destination file (omit to patch in-place)")
    parser.add_argument("--db", default=None, help="Override target database name")
    parser.add_argument("--user", default=None, help="Override target user name")
    args = parser.parse_args()

    if not args.db and not args.user:
        print("Nothing to patch — specify --db and/or --user", file=sys.stderr)
        sys.exit(1)

    patch_file(args.input, args.output, args.db, args.user)


if __name__ == "__main__":
    main()
