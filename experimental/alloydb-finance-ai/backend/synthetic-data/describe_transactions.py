"""
Batch-enrich synthetic transactions with 1-sentence descriptions using Gemini.

Examples:
    python backend/synthetic-data/describe_transactions.py
    python backend/synthetic-data/describe_transactions.py --batch-size 150 --model gemini-2.5-flash
    python backend/synthetic-data/describe_transactions.py --in-place
"""

from __future__ import annotations

import argparse
import csv
import json
import os
import re
import time
import random
from pathlib import Path
from typing import Any

import requests
from dotenv import load_dotenv


SCRIPT_DIR = Path(__file__).resolve().parent
DEFAULT_INPUT = SCRIPT_DIR / "output_data" / "transactions.csv"
DEFAULT_OUTPUT = SCRIPT_DIR / "output_data" / "transactions_with_descriptions.csv"
GEMINI_API_URL = "https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent?key={api_key}"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Add 1-sentence descriptions to each transaction via Gemini.")
    parser.add_argument("--input-csv", type=Path, default=DEFAULT_INPUT, help="Input CSV path.")
    parser.add_argument("--output-csv", type=Path, default=DEFAULT_OUTPUT, help="Output CSV path.")
    parser.add_argument("--model", default="gemini-2.5-flash", help="Gemini model name.")
    parser.add_argument("--batch-size", type=int, default=120, help="Rows per Gemini call.")
    parser.add_argument("--sleep-ms", type=int, default=100, help="Pause between API calls (ms).")
    parser.add_argument("--in-place", action="store_true", help="Overwrite the input CSV.")
    parser.add_argument("--skip-existing", action="store_true", help="Skip rows that already have descriptions.")
    return parser.parse_args()


def load_transactions(path: Path) -> list[dict[str, str]]:
    if not path.exists():
        raise FileNotFoundError(f"Input CSV not found: {path}")
    with path.open(newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def chunked(items: list[dict[str, str]], size: int) -> list[list[dict[str, str]]]:
    return [items[i : i + size] for i in range(0, len(items), size)]


ITEMS_BY_CATEGORY: dict[str, list[str]] = {
    "groceries": ["chocolate milk and fruit", "vegetables and eggs", "bread and yogurt", "rice and pantry staples"],
    "dining": ["lunch bowl and iced tea", "coffee and breakfast sandwich", "dinner takeout", "quick bite after class"],
    "shopping": ["household supplies", "new running socks", "phone accessories", "basic clothing essentials"],
    "subscriptions": ["monthly streaming plan", "music subscription renewal", "cloud storage subscription", "premium app plan"],
    "utilities": ["electricity bill", "internet bill", "phone service bill", "water and utility charges"],
    "rent": ["monthly apartment rent", "rent payment for this month", "housing payment", "rent transfer"],
    "healthcare": ["cold medicine and vitamins", "pharmacy essentials", "pain relief and personal care", "health items refill"],
    "transportation": ["rideshare trip", "commute fare", "ride to campus", "late-night ride home"],
    "entertainment": ["movie night purchase", "weekend entertainment spend", "streaming add-on", "event ticket"],
    "income": ["paycheck deposit", "salary payment", "monthly payroll deposit", "income credit"],
    "travel": ["flight booking", "hotel reservation", "trip transport expense", "travel-related purchase"],
}


def fallback_description(row: dict[str, str], rng: random.Random) -> str:
    amount = float(row["amount"])
    category = row["spending_category"]
    merchant = row["merchant_name"]
    city = row.get("city", "")
    items = ITEMS_BY_CATEGORY.get(category, ["everyday expenses"])
    picked = rng.choice(items)

    if amount > 0:
        return f"Got paid ${abs(amount):.2f} from {merchant} as {picked}."
    city_suffix = f" in {city}" if city else ""
    return f"Spent ${abs(amount):.2f} at {merchant}{city_suffix} for {picked}."


def build_prompt(batch: list[dict[str, str]]) -> str:
    lines = []
    for idx, row in enumerate(batch):
        lines.append(
            "|".join(
                [
                    str(idx),
                    row.get("timestamp", ""),
                    row.get("amount", ""),
                    row.get("merchant_name", ""),
                    row.get("spending_category", ""),
                    row.get("transaction_type", ""),
                    row.get("payment_method", ""),
                    row.get("city", ""),
                ]
            )
        )

    joined_rows = "\n".join(lines)
    return (
        "You create concise personal finance transaction summaries.\n"
        "For each input row, generate exactly one sentence in plain English.\n"
        "Rules:\n"
        "- Keep each sentence 8-18 words.\n"
        "- Mention merchant and category; optionally mention city or payment method.\n"
        "- Do not invent extra entities.\n"
        "- No markdown, no bullets.\n"
        "- Return ONLY a JSON array of strings, in the same order as input.\n\n"
        "Input rows use pipe format:\n"
        "index|timestamp|amount|merchant_name|spending_category|transaction_type|payment_method|city\n\n"
        f"{joined_rows}\n"
    )


def extract_json_array(text: str) -> list[str]:
    stripped = text.strip()
    try:
        parsed = json.loads(stripped)
        if isinstance(parsed, list) and all(isinstance(x, str) for x in parsed):
            return parsed
    except json.JSONDecodeError:
        pass

    fence_match = re.search(r"```(?:json)?\s*(\[[\s\S]*?\])\s*```", stripped)
    if fence_match:
        parsed = json.loads(fence_match.group(1))
        if isinstance(parsed, list) and all(isinstance(x, str) for x in parsed):
            return parsed

    array_match = re.search(r"(\[[\s\S]*\])", stripped)
    if array_match:
        parsed = json.loads(array_match.group(1))
        if isinstance(parsed, list) and all(isinstance(x, str) for x in parsed):
            return parsed

    raise ValueError("Gemini output did not contain a valid JSON string array.")


def call_gemini(prompt: str, api_key: str, model: str, timeout_s: int = 60) -> list[str]:
    url = GEMINI_API_URL.format(model=model, api_key=api_key)
    payload: dict[str, Any] = {
        "contents": [{"parts": [{"text": prompt}]}],
        "generationConfig": {
            "temperature": 0.2,
            "responseMimeType": "application/json",
        },
    }
    response = requests.post(url, json=payload, timeout=timeout_s)
    response.raise_for_status()
    body = response.json()
    text = body["candidates"][0]["content"]["parts"][0]["text"]
    return extract_json_array(text)


def write_transactions(path: Path, rows: list[dict[str, str]]) -> None:
    if not rows:
        raise ValueError("No rows to write.")
    fieldnames = list(rows[0].keys())
    if "description" not in fieldnames:
        fieldnames.append("description")
    with path.open("w", newline="", encoding="utf-8") as f:
        writer = csv.DictWriter(f, fieldnames=fieldnames)
        writer.writeheader()
        writer.writerows(rows)


def enrich_descriptions(
    rows: list[dict[str, str]],
    api_key: str,
    model: str,
    batch_size: int,
    sleep_ms: int,
    skip_existing: bool,
) -> tuple[int, int]:
    updated = 0
    fallback_used = 0
    rng = random.Random(42)
    batches = chunked(rows, batch_size)
    total_batches = len(batches)

    for batch_index, batch in enumerate(batches, start=1):
        rows_for_model = [row for row in batch if not (skip_existing and row.get("description", "").strip())]
        if not rows_for_model:
            print(f"[{batch_index}/{total_batches}] skipped batch (all rows already have description)")
            continue

        prompt = build_prompt(rows_for_model)
        try:
            descriptions = call_gemini(prompt=prompt, api_key=api_key, model=model)
            if len(descriptions) != len(rows_for_model):
                raise ValueError(
                    f"Gemini returned {len(descriptions)} descriptions for {len(rows_for_model)} rows."
                )
            for row, description in zip(rows_for_model, descriptions):
                row["description"] = " ".join(description.strip().split())
                updated += 1
            print(f"[{batch_index}/{total_batches}] enriched {len(rows_for_model)} rows")
        except Exception as exc:
            print(f"[{batch_index}/{total_batches}] Gemini failed ({exc}); using deterministic fallback.")
            fallback_used += len(rows_for_model)
            for row in rows_for_model:
                row["description"] = fallback_description(row, rng)
                updated += 1

        if sleep_ms > 0:
            time.sleep(sleep_ms / 1000)

    return updated, fallback_used


def main() -> None:
    load_dotenv()
    args = parse_args()
    api_key = os.getenv("GEMINI_API_KEY", "").strip()
    if not api_key:
        raise ValueError("GEMINI_API_KEY is not set in environment/.env")

    input_csv = args.input_csv.resolve()
    output_csv = input_csv if args.in_place else args.output_csv.resolve()

    rows = load_transactions(input_csv)
    if not rows:
        print("No rows found in CSV; nothing to enrich.")
        return

    print(f"Loaded {len(rows)} rows from {input_csv}")
    updated, fallback_used = enrich_descriptions(
        rows=rows,
        api_key=api_key,
        model=args.model,
        batch_size=max(1, args.batch_size),
        sleep_ms=max(0, args.sleep_ms),
        skip_existing=args.skip_existing,
    )
    write_transactions(output_csv, rows)
    print(f"Wrote {len(rows)} rows to {output_csv}")
    print(f"Descriptions updated: {updated}")
    print(f"Fallback descriptions used: {fallback_used}")


if __name__ == "__main__":
    main()
