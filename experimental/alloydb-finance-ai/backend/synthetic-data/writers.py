"""Write generated data to CSV and JSON in the output directory."""

import csv
import json
import uuid
from io import StringIO
from pathlib import Path

from config import CSV_FILENAME, JSON_FILENAME, OUTPUT_DIR, RAW_DEBUG_FILENAME


def ensure_output_dir() -> Path:
    """Create output_data if it doesn't exist. Returns OUTPUT_DIR."""
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    return OUTPUT_DIR


def csv_to_transactions(raw_csv: str) -> list[dict]:
    """Parse raw CSV text into a list of transaction dicts. Coerces numeric fields."""
    reader = csv.DictReader(StringIO(raw_csv))
    transactions = list(reader)

    for row in transactions:
        if "amount" in row and row["amount"]:
            try:
                row["amount"] = float(row["amount"])
            except ValueError:
                pass

    return transactions


def write_csv(raw_text: str, out_dir: Path | None = None) -> Path:
    """Save raw CSV to output_data/transactions.csv."""
    out_dir = out_dir or ensure_output_dir()
    path = out_dir / CSV_FILENAME
    with open(path, "w") as f:
        f.write(raw_text)
    return path


def write_json(transactions: list[dict], out_dir: Path | None = None) -> Path:
    """Save transactions as JSON to output_data/transactions.json."""
    out_dir = out_dir or ensure_output_dir()
    path = out_dir / JSON_FILENAME
    with open(path, "w") as f:
        json.dump({"transactions": transactions}, f, indent=2)
    return path


def write_raw_debug(raw_text: str, out_dir: Path | None = None) -> Path:
    """Save raw API response for debugging (e.g. on error)."""
    out_dir = out_dir or ensure_output_dir()
    path = out_dir / RAW_DEBUG_FILENAME
    with open(path, "w") as f:
        f.write(raw_text)
    return path


def deduplicate_ids(transactions: list[dict]) -> int:
    """Replace duplicate transaction_id values with fresh UUIDs.
    Returns the number of IDs that were replaced."""
    seen: set[str] = set()
    replaced = 0
    for row in transactions:
        tid = row.get("transaction_id", "")
        if tid in seen:
            row["transaction_id"] = str(uuid.uuid4())
            replaced += 1
        else:
            seen.add(tid)
    return replaced


def transactions_to_csv(transactions: list[dict]) -> str:
    """Serialize a list of transaction dicts back to CSV text."""
    if not transactions:
        return ""
    out = StringIO()
    writer = csv.DictWriter(out, fieldnames=transactions[0].keys())
    writer.writeheader()
    writer.writerows(transactions)
    return out.getvalue()


def save_all(raw_csv: str) -> tuple[Path, Path]:
    """
    Save CSV and JSON to output_data. Creates dir if needed.
    Deduplicates transaction IDs before writing.
    Returns (csv_path, json_path).
    """
    ensure_output_dir()
    transactions = csv_to_transactions(raw_csv)

    replaced = deduplicate_ids(transactions)
    if replaced:
        print(f"  Fixed {replaced} duplicate transaction_id(s) with new UUIDs.")

    csv_path = write_csv(transactions_to_csv(transactions))
    json_path = write_json(transactions)
    return csv_path, json_path
