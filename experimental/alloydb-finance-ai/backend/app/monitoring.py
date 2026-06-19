"""Performance metrics store with rolling window + CSV persistence on disk."""

from __future__ import annotations

import asyncio
import csv
import math
import os
import statistics
import time
import uuid
from collections import deque
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional

MAX_RECORDS = 500

_MS_KEYS = (
    "mcp_routing_ms",
    "nl2sql_translation_ms",
    "nl2sql_execution_ms",
    "nl2sql_total_ms",
    "semantic_embedding_ms",
    "semantic_vector_search_ms",
    "semantic_total_ms",
    "gemini_call_ms",
    "context_fetch_ms",
    "e2e_total_ms",
)

_NUMERIC_SUMMARY_KEYS = ("nl2sql_row_count", "semantic_match_count", "gemini_response_bytes")

# Stable CSV column order (wide format; empty cells for missing optional fields)
CSV_FIELDS = [
    "id",
    "timestamp",
    "tool",
    "mcp_routing_ms",
    "nl2sql_translation_ms",
    "nl2sql_execution_ms",
    "nl2sql_total_ms",
    "nl2sql_row_count",
    "nl2sql_truncated",
    "semantic_embedding_ms",
    "semantic_vector_search_ms",
    "semantic_total_ms",
    "semantic_match_count",
    "gemini_call_ms",
    "gemini_response_bytes",
    "context_fetch_ms",
    "e2e_total_ms",
    "nl2sql_pct",
    "gemini_pct",
    "other_pct",
]

_INT_FIELDS = frozenset({"nl2sql_row_count", "semantic_match_count", "gemini_response_bytes"})


def _default_csv_path() -> Path:
    return Path(__file__).resolve().parent / "data" / "performance_metrics.csv"


def _persist_path() -> Path:
    raw = os.getenv("METRICS_PERSIST_PATH", "").strip()
    return Path(raw) if raw else _default_csv_path()


def utc_now_iso() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def compute_pct_fields(record: dict[str, Any]) -> None:
    """Fill nl2sql_pct, gemini_pct, other_pct in place (spec: three-way split; routing/semantic in other)."""
    e2e = record.get("e2e_total_ms")
    if not e2e or e2e <= 0:
        return
    nl2sql_total = record.get("nl2sql_total_ms")
    gemini = record.get("gemini_call_ms")
    if nl2sql_total is not None:
        record["nl2sql_pct"] = round(100.0 * float(nl2sql_total) / float(e2e), 2)
    if gemini is not None:
        record["gemini_pct"] = round(100.0 * float(gemini) / float(e2e), 2)
    n = float(record.get("nl2sql_pct") or 0)
    g = float(record.get("gemini_pct") or 0)
    record["other_pct"] = round(max(0.0, 100.0 - n - g), 2)


def _drop_nones(d: dict[str, Any]) -> dict[str, Any]:
    return {k: v for k, v in d.items() if v is not None}


def _percentile_nearest(sorted_vals: list[float], p: float) -> Optional[float]:
    if not sorted_vals:
        return None
    if len(sorted_vals) == 1:
        return float(sorted_vals[0])
    k = (len(sorted_vals) - 1) * (p / 100.0)
    f = int(math.floor(k))
    c = min(f + 1, len(sorted_vals) - 1)
    if f == c:
        return float(sorted_vals[f])
    d0 = sorted_vals[f] * (c - k)
    d1 = sorted_vals[c] * (k - f)
    return float(d0 + d1)


def _stats_ms(values: list[float]) -> dict[str, Optional[float]]:
    if not values:
        return {
            "count": 0,
            "avg": None,
            "p50": None,
            "p95": None,
            "p99": None,
            "min": None,
            "max": None,
        }
    s = sorted(values)
    return {
        "count": len(values),
        "avg": round(statistics.mean(values), 3),
        "p50": round(_percentile_nearest(s, 50) or 0, 3),
        "p95": round(_percentile_nearest(s, 95) or 0, 3),
        "p99": round(_percentile_nearest(s, 99) or 0, 3),
        "min": round(min(values), 3),
        "max": round(max(values), 3),
    }


def _stats_numeric(values: list[float]) -> dict[str, Optional[float]]:
    if not values:
        return {"count": 0, "avg": None, "min": None, "max": None}
    return {
        "count": len(values),
        "avg": round(statistics.mean(values), 3),
        "min": round(min(values), 3),
        "max": round(max(values), 3),
    }


def _parse_csv_row(row: dict[str, str]) -> dict[str, Any]:
    rec: dict[str, Any] = {}
    for k in CSV_FIELDS:
        if k not in row:
            continue
        s = (row.get(k) or "").strip()
        if s == "":
            continue
        if k == "nl2sql_truncated":
            rec[k] = s.lower() in ("true", "1", "yes")
            continue
        if k in ("id", "timestamp", "tool"):
            rec[k] = s
            continue
        try:
            if k in _INT_FIELDS:
                rec[k] = int(float(s))
            else:
                rec[k] = float(s)
        except ValueError:
            rec[k] = s
    return rec


def _write_csv_atomic(path: Path, records: list[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    with open(tmp, "w", newline="", encoding="utf-8") as fh:
        writer = csv.DictWriter(fh, fieldnames=CSV_FIELDS, extrasaction="ignore")
        writer.writeheader()
        for rec in records:
            out_row: dict[str, str] = {}
            for key in CSV_FIELDS:
                v = rec.get(key)
                if v is None:
                    out_row[key] = ""
                elif isinstance(v, bool):
                    out_row[key] = "true" if v else "false"
                else:
                    out_row[key] = str(v)
            writer.writerow(out_row)
    tmp.replace(path)


def _load_csv(path: Path) -> list[dict[str, Any]]:
    if not path.exists():
        return []
    rows: list[dict[str, Any]] = []
    with open(path, newline="", encoding="utf-8") as fh:
        reader = csv.DictReader(fh)
        for row in reader:
            if not any((row.get(k) or "").strip() for k in ("id", "timestamp")):
                continue
            rows.append(_parse_csv_row(row))
    return rows[-MAX_RECORDS:]


class MetricsStore:
    def __init__(self) -> None:
        self._path = _persist_path()
        self._records: deque[dict[str, Any]] = deque(maxlen=MAX_RECORDS)
        self._lock = asyncio.Lock()
        loaded = _load_csv(self._path)
        self._records.extend(loaded)

    async def append(self, record: dict[str, Any]) -> dict[str, Any]:
        compute_pct_fields(record)
        rec = {
            "id": str(uuid.uuid4()),
            "timestamp": utc_now_iso(),
            **record,
        }
        rec = _drop_nones(rec)
        async with self._lock:
            self._records.append(rec)
            snapshot = list(self._records)
        await asyncio.to_thread(_write_csv_atomic, self._path, snapshot)
        return rec

    async def clear(self) -> None:
        async with self._lock:
            self._records.clear()
        await asyncio.to_thread(_write_csv_atomic, self._path, [])

    async def get_records(self) -> list[dict[str, Any]]:
        async with self._lock:
            return list(self._records)

    async def summary(self) -> dict[str, Any]:
        async with self._lock:
            rows = list(self._records)

        out: dict[str, Any] = {"sample_size": len(rows)}

        for key in _MS_KEYS:
            vals = [float(r[key]) for r in rows if key in r and r[key] is not None]
            out[key] = _stats_ms(vals)

        for key in _NUMERIC_SUMMARY_KEYS:
            vals = [float(r[key]) for r in rows if key in r and r[key] is not None]
            out[key] = _stats_numeric(vals)

        trunc_vals = [r["nl2sql_truncated"] for r in rows if "nl2sql_truncated" in r]
        if trunc_vals:
            true_n = sum(1 for v in trunc_vals if v is True)
            out["nl2sql_truncation_rate_pct"] = round(100.0 * true_n / len(trunc_vals), 2)
        else:
            out["nl2sql_truncation_rate_pct"] = None

        for pct_key in ("nl2sql_pct", "gemini_pct", "other_pct"):
            vals = [float(r[pct_key]) for r in rows if pct_key in r and r[pct_key] is not None]
            out[pct_key] = _stats_numeric(vals) if vals else {"count": 0, "avg": None, "min": None, "max": None}

        return out


# Module singleton used by FastAPI routes
metrics_store = MetricsStore()


def monotonic_ms(start: float) -> float:
    return (time.perf_counter() - start) * 1000.0
