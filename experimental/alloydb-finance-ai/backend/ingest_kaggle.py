"""
Ingest Kaggle retail transaction data into the transactions_2 table.

Deduplicates to one row per unique ItemDescription (~3,866 rows), enriches
merchant_name and spending_category via Gemini, re-dates rows to the last 6
months, and bulk-embeds using AlloyDB's google_ml.embedding().

Writes a detailed timing log to ingest_kaggle_timings.csv alongside this script.

Usage:
    cd backend
    source .venv/bin/activate
    python -u ingest_kaggle.py
"""
from __future__ import annotations

import asyncio
import csv
import json
import os
import random
import statistics
import sys
import uuid
from collections import defaultdict
from datetime import datetime, timezone, timedelta
from pathlib import Path
import time

import asyncpg
import google.auth
import google.auth.transport.requests
import requests
from dotenv import load_dotenv

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

SCRIPT_DIR = Path(__file__).resolve().parent
load_dotenv(SCRIPT_DIR.parent / ".env")

DATABASE_URL = os.environ["DATABASE_URL"].replace("host.docker.internal", "localhost")
DATABASE_SSL_MODE = os.getenv("DATABASE_SSL_MODE", "disable").lower()
GEMINI_MODEL = os.getenv("GEMINI_MODEL", "gemini-2.5-flash").strip()
GOOGLE_CLOUD_PROJECT = os.getenv("GOOGLE_CLOUD_PROJECT", "").strip()
GOOGLE_CLOUD_LOCATION = os.getenv("GOOGLE_CLOUD_LOCATION", "us-central1").strip()

CSV_PATH = SCRIPT_DIR / "transaction_data.csv"
TIMING_LOG = SCRIPT_DIR / "ingest_kaggle_timings.csv"
BATCH_SIZE = 50
DEV_USER_EMAIL = "foo@bar.com"
DEBUG_FIRST_BATCH = True  # print raw Gemini response for batch 1

SPENDING_CATEGORIES = [
    "shopping", "gifts", "household", "office", "clothing",
    "hobbies", "food", "beauty", "toys", "stationery",
]


# ---------------------------------------------------------------------------
# Timing logger
# ---------------------------------------------------------------------------

class TimingLogger:
    """Writes one CSV row per timed event to TIMING_LOG."""

    FIELDS = [
        "run_started_at", "stage", "detail",
        "started_at", "finished_at", "duration_s",
        "status", "rows_affected", "notes",
    ]

    def __init__(self, run_started_at: str) -> None:
        self.run_started_at = run_started_at
        self._fh = TIMING_LOG.open("a", newline="", encoding="utf-8")
        self._writer = csv.DictWriter(self._fh, fieldnames=self.FIELDS)
        # Write header only if file is new/empty
        if TIMING_LOG.stat().st_size == 0:
            self._writer.writeheader()
        self._fh.flush()

    def log(
        self,
        stage: str,
        started_at: float,
        finished_at: float,
        *,
        detail: str = "",
        status: str = "ok",
        rows_affected: int | None = None,
        notes: str = "",
    ) -> None:
        duration = round(finished_at - started_at, 3)
        self._writer.writerow({
            "run_started_at": self.run_started_at,
            "stage": stage,
            "detail": detail,
            "started_at": datetime.fromtimestamp(started_at, tz=timezone.utc).isoformat(),
            "finished_at": datetime.fromtimestamp(finished_at, tz=timezone.utc).isoformat(),
            "duration_s": duration,
            "status": status,
            "rows_affected": "" if rows_affected is None else rows_affected,
            "notes": notes,
        })
        self._fh.flush()
        label = f"[{stage}]" + (f" {detail}" if detail else "")
        print(f"  {label}: {duration}s — {status}" + (f" ({notes})" if notes else ""), flush=True)

    def close(self) -> None:
        self._fh.close()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _random_ts_in_window(rng: random.Random, start: datetime, end: datetime) -> datetime:
    delta = int((end - start).total_seconds())
    return start + timedelta(seconds=rng.randint(0, max(delta, 1)))


def _build_timestamps(n: int, months_back: int = 6) -> list[datetime]:
    rng = random.Random(42)
    now = datetime.now(timezone.utc)
    start = now - timedelta(days=30 * months_back)
    return [_random_ts_in_window(rng, start, now) for _ in range(n)]


# ADC credentials — refreshed once and reused across all batches
_adc_credentials: google.auth.credentials.Credentials | None = None
_adc_session: google.auth.transport.requests.Request | None = None


def _get_access_token() -> str:
    global _adc_credentials, _adc_session
    if _adc_credentials is None:
        _adc_credentials, _ = google.auth.default(
            scopes=["https://www.googleapis.com/auth/cloud-platform"]
        )
        _adc_session = google.auth.transport.requests.Request()
    if not _adc_credentials.valid:
        _adc_credentials.refresh(_adc_session)
    return _adc_credentials.token


def _gemini_enrich(descriptions: list[str]) -> list[dict]:
    prompt = (
        "For each item description below, return a JSON array where each element has:\n"
        '  "merchant_name": a realistic store name that would sell this item '
        '(e.g. "Pottery Barn", "WHSmith", "Paperchase")\n'
        f'  "spending_category": one of {SPENDING_CATEGORIES}\n'
        "Return ONLY a valid JSON array with no prose, markdown, or code fences.\n\n"
        "Descriptions:\n" + json.dumps(descriptions)
    )
    url = (
        f"https://{GOOGLE_CLOUD_LOCATION}-aiplatform.googleapis.com/v1/projects/"
        f"{GOOGLE_CLOUD_PROJECT}/locations/{GOOGLE_CLOUD_LOCATION}/"
        f"publishers/google/models/{GEMINI_MODEL}:generateContent"
    )
    headers = {
        "Authorization": f"Bearer {_get_access_token()}",
        "Content-Type": "application/json",
    }
    body = {"contents": [{"role": "user", "parts": [{"text": prompt}]}]}
    resp = requests.post(url, json=body, headers=headers, timeout=60)
    resp.raise_for_status()
    text = resp.json()["candidates"][0]["content"]["parts"][0]["text"].strip()
    if DEBUG_FIRST_BATCH and not getattr(_gemini_enrich, "_debug_done", False):
        print("\n--- Gemini raw response (batch 1) ---", flush=True)
        print(text[:2000], flush=True)
        print("--- end ---\n", flush=True)
        _gemini_enrich._debug_done = True
    if text.startswith("```"):
        text = text.split("\n", 1)[1]
        text = text.rsplit("```", 1)[0]
    return json.loads(text)


def _enrich_all(descriptions: list[str], tl: TimingLogger) -> dict[str, dict]:
    lookup: dict[str, dict] = {}
    total = len(descriptions)
    num_batches = (total + BATCH_SIZE - 1) // BATCH_SIZE
    for i in range(0, total, BATCH_SIZE):
        batch = descriptions[i : i + BATCH_SIZE]
        batch_num = i // BATCH_SIZE + 1
        print(f"  Gemini batch {batch_num}/{num_batches} ({len(batch)} descriptions)...", flush=True)
        t0 = time.time()
        success = False
        for attempt in range(3):
            try:
                results = _gemini_enrich(batch)
                for desc, res in zip(batch, results):
                    lookup[desc] = {
                        "merchant_name": res.get("merchant_name", "General Retailer"),
                        "spending_category": res.get("spending_category", "shopping"),
                    }
                tl.log(
                    "gemini_batch", t0, time.time(),
                    detail=f"batch {batch_num}/{num_batches}",
                    status="ok",
                    rows_affected=len(batch),
                    notes=f"attempt {attempt + 1}",
                )
                success = True
                break
            except requests.exceptions.HTTPError as e:
                if e.response is not None and e.response.status_code == 429:
                    wait = 30 * (attempt + 1)
                    print(f"    Rate limited, waiting {wait}s (attempt {attempt + 1}/3)...", flush=True)
                    time.sleep(wait)
                else:
                    tl.log(
                        "gemini_batch", t0, time.time(),
                        detail=f"batch {batch_num}/{num_batches}",
                        status="fallback",
                        rows_affected=len(batch),
                        notes=str(e),
                    )
                    for desc in batch:
                        lookup.setdefault(desc, {"merchant_name": "General Retailer", "spending_category": "shopping"})
                    break
            except Exception as e:
                if attempt == 2:
                    tl.log(
                        "gemini_batch", t0, time.time(),
                        detail=f"batch {batch_num}/{num_batches}",
                        status="fallback",
                        rows_affected=len(batch),
                        notes=str(e),
                    )
                    for desc in batch:
                        lookup.setdefault(desc, {"merchant_name": "General Retailer", "spending_category": "shopping"})
        if not success and attempt == 2:
            tl.log(
                "gemini_batch", t0, time.time(),
                detail=f"batch {batch_num}/{num_batches}",
                status="fallback_429",
                rows_affected=len(batch),
                notes="all 3 attempts rate-limited",
            )
            for desc in batch:
                lookup.setdefault(desc, {"merchant_name": "General Retailer", "spending_category": "shopping"})
        time.sleep(2)
    return lookup


# ---------------------------------------------------------------------------
# Main (async to use asyncpg)
# ---------------------------------------------------------------------------

async def main() -> None:
    run_started_at = datetime.now(timezone.utc).isoformat()
    run_t0 = time.time()

    # Ensure timing log file exists before opening
    TIMING_LOG.touch(exist_ok=True)
    tl = TimingLogger(run_started_at)
    print(f"Run started at {run_started_at}", flush=True)
    print(f"Timing log: {TIMING_LOG}", flush=True)

    print(f"Reading CSV: {CSV_PATH}", flush=True)
    if not CSV_PATH.exists():
        raise FileNotFoundError(f"CSV not found: {CSV_PATH}")

    # --- 1. Read and filter CSV ------------------------------------------------
    t0 = time.time()
    groups: dict[str, list[dict]] = defaultdict(list)
    total_raw = 0
    total_filtered = 0

    with CSV_PATH.open(newline="", encoding="utf-8") as f:
        reader = csv.DictReader(f)
        for row in reader:
            total_raw += 1
            uid = row.get("UserId", "").strip()
            desc = row.get("ItemDescription", "").strip()
            try:
                qty = float(row["NumberOfItemsPurchased"])
                cost = float(row["CostPerItem"])
            except (KeyError, ValueError):
                continue
            if uid == "-1" or not desc or qty * cost <= 0:
                continue
            total_filtered += 1
            groups[desc].append({
                "qty": qty,
                "cost": cost,
                "country": row.get("Country", "").strip() or "United Kingdom",
            })

    tl.log(
        "csv_read", t0, time.time(),
        rows_affected=total_filtered,
        notes=f"raw={total_raw}, filtered={total_filtered}, unique_descs={len(groups)}",
    )
    print(f"Unique ItemDescriptions: {len(groups)}", flush=True)

    # --- 2. Aggregate per unique description -----------------------------------
    t0 = time.time()
    agg_rows: list[dict] = []
    for desc, occurrences in groups.items():
        qtys = [o["qty"] for o in occurrences]
        amounts = [o["qty"] * o["cost"] for o in occurrences]
        country_counts: dict[str, int] = defaultdict(int)
        for o in occurrences:
            country_counts[o["country"]] += 1
        mode_country = max(country_counts, key=lambda k: country_counts[k])
        agg_rows.append({
            "item_description": desc,
            "quantity": max(1, round(statistics.median(qtys))),
            "amount": round(statistics.median(amounts), 2),
            "country": mode_country,
        })

    tl.log("aggregation", t0, time.time(), rows_affected=len(agg_rows))

    # --- 3. Re-date rows across last 6 months ---------------------------------
    t0 = time.time()
    timestamps = _build_timestamps(len(agg_rows), months_back=6)
    for row, ts in zip(agg_rows, timestamps):
        row["timestamp"] = ts
    tl.log("timestamp_gen", t0, time.time(), rows_affected=len(agg_rows))

    # --- 4. Gemini enrichment --------------------------------------------------
    print(f"\nEnriching {len(agg_rows)} descriptions via Gemini...", flush=True)
    gemini_t0 = time.time()
    all_descs = [r["item_description"] for r in agg_rows]
    enrichment = _enrich_all(all_descs, tl)
    tl.log(
        "gemini_total", gemini_t0, time.time(),
        rows_affected=len(enrichment),
        notes=f"{len(all_descs)} descriptions, {(len(all_descs) + BATCH_SIZE - 1) // BATCH_SIZE} batches",
    )

    for row in agg_rows:
        info = enrichment.get(row["item_description"], {})
        row["merchant_name"] = info.get("merchant_name", "General Retailer")
        row["spending_category"] = info.get("spending_category", "shopping")

    # --- 5. Connect to DB, create table, insert --------------------------------
    ssl = "require" if DATABASE_SSL_MODE == "require" else False

    t0 = time.time()
    conn = await asyncpg.connect(DATABASE_URL, ssl=ssl)
    tl.log("db_connect", t0, time.time())

    try:
        user_id = await conn.fetchval("SELECT id FROM users WHERE email = $1", DEV_USER_EMAIL)
        if not user_id:
            raise RuntimeError(f"User {DEV_USER_EMAIL} not found. Start the app first to seed the user.")
        print(f"\nUsing user_id={user_id} ({DEV_USER_EMAIL})", flush=True)

        t0 = time.time()
        await conn.execute("""
            CREATE TABLE IF NOT EXISTS transactions_2 (
                transaction_id    UUID PRIMARY KEY,
                user_id           INTEGER NOT NULL,
                timestamp         TIMESTAMPTZ NOT NULL,
                amount            NUMERIC(18, 2) NOT NULL,
                merchant_name     TEXT NOT NULL,
                spending_category TEXT NOT NULL,
                item_description  TEXT NOT NULL,
                quantity          INTEGER NOT NULL,
                country           TEXT NOT NULL
            )
        """)
        await conn.execute("""
            ALTER TABLE transactions_2
            ADD COLUMN IF NOT EXISTS embedding vector(768)
        """)
        tl.log("ddl_create_table", t0, time.time())

        before = await conn.fetchval("SELECT COUNT(*) FROM transactions_2")

        t0 = time.time()
        await conn.executemany(
            """
            INSERT INTO transactions_2 (
                transaction_id, user_id, timestamp, amount,
                merchant_name, spending_category, item_description,
                quantity, country
            ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
            ON CONFLICT (transaction_id) DO NOTHING
            """,
            [
                (
                    uuid.uuid4(),
                    user_id,
                    r["timestamp"],
                    r["amount"],
                    r["merchant_name"],
                    r["spending_category"],
                    r["item_description"],
                    r["quantity"],
                    r["country"],
                )
                for r in agg_rows
            ],
        )
        after = await conn.fetchval("SELECT COUNT(*) FROM transactions_2")
        inserted = after - before
        tl.log("db_insert", t0, time.time(), rows_affected=inserted, notes=f"total_in_table={after}")
        print(f"Inserted {inserted} new rows (total: {after})", flush=True)

        # --- 6. Chunked embed (500 rows per chunk) --------------------------------
        print("Backfilling embeddings in chunks of 500...", flush=True)
        embed_t0 = time.time()
        total_embedded = 0
        chunk_size = 500
        chunk_num = 0
        while True:
            chunk_num += 1
            t0 = time.time()
            result = await conn.execute("""
                UPDATE transactions_2
                SET embedding = google_ml.embedding(
                    'text-embedding-005',
                    'Purchased ' || quantity::text || ' ' || item_description ||
                    '. Category: ' || spending_category ||
                    '. Store type: ' || merchant_name ||
                    '. Country: ' || country || '.'
                )::vector
                WHERE transaction_id IN (
                    SELECT transaction_id FROM transactions_2
                    WHERE embedding IS NULL
                    LIMIT $1
                )
            """, chunk_size, timeout=300)
            n = int(result.split()[-1]) if result else 0
            total_embedded += n
            tl.log(
                "embedding_chunk", t0, time.time(),
                detail=f"chunk {chunk_num}",
                rows_affected=n,
                notes=f"total_so_far={total_embedded}",
            )
            print(f"  Chunk {chunk_num}: embedded {n} rows (total: {total_embedded})", flush=True)
            if n == 0:
                break
        tl.log("embedding_total", embed_t0, time.time(), rows_affected=total_embedded)

    finally:
        await conn.close()

    tl.log("total_run", run_t0, time.time(), notes="full pipeline complete")
    tl.close()
    print(f"\nDone. Timing log written to {TIMING_LOG}", flush=True)


if __name__ == "__main__":
    asyncio.run(main())
