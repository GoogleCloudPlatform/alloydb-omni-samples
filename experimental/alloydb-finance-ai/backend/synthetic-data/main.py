"""
Central entry point for synthetic transaction data generation.

Run from the synthetic-data directory:
    python main.py --rows 5000 --seed 42

Or from the repo root:
    python -m backend.synthetic-data.main --rows 5000 --seed 42
"""

import argparse
from collections import Counter

from config import OUTPUT_DIR
from generator import build_transactions, transactions_to_csv_text, validate_transactions
from writers import save_all, write_raw_debug


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Generate deterministic synthetic transaction CSV/JSON data.")
    parser.add_argument("--rows", type=int, default=5000, help="Number of rows to generate.")
    parser.add_argument("--seed", type=int, default=42, help="Random seed for reproducible data.")
    parser.add_argument("--months-back", type=int, default=6, help="Generate timestamps across last N months.")
    return parser.parse_args()


def run(rows: int, seed: int, months_back: int) -> None:
    """Generate transactions and save CSV + JSON to output_data."""
    print(f"Generating {rows} deterministic transactions (seed={seed}, months_back={months_back})...")
    transactions = build_transactions(rows=rows, seed=seed, months_back=months_back)
    validate_transactions(transactions, expected_rows=rows)
    raw_csv = transactions_to_csv_text(transactions)

    try:
        csv_path, json_path = save_all(raw_csv)
        num_rows = len([line for line in raw_csv.split("\n") if line.strip()]) - 1
        print(f"Saved {num_rows} records to {OUTPUT_DIR}/")
        print(f"  - {csv_path.name}")
        print(f"  - {json_path.name}")
        category_counts = Counter(row["spending_category"] for row in transactions)
        print("Category distribution:")
        for category, count in sorted(category_counts.items()):
            print(f"  - {category}: {count}")
    except Exception as e:
        print(f"Error saving output: {e}")
        debug_path = write_raw_debug(raw_csv)
        print(f"Raw response saved to {debug_path}")


if __name__ == "__main__":
    args = parse_args()
    run(rows=args.rows, seed=args.seed, months_back=args.months_back)
