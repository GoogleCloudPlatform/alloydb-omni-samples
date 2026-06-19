#!/usr/bin/env python3
"""
Fire mixed NL2SQL / semantic-style queries against a running API, then print
/metrics/summary and save results to benchmark_results_<timestamp>.json.

Requires: requests (same venv as backend) or: pip install requests
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime, timezone
from typing import Any, Optional

try:
    import requests
except ImportError:
    print("Install requests: pip install requests", file=sys.stderr)
    sys.exit(1)

# Default prompts: router should pick nl2sql vs semantic_search (see mcp/tool_router)
NL2SQL_PROMPTS = [
    "How much did I spend on dining last month?",
    "Show total spending by category this month",
    "Top 5 merchants by total spend",
]

SEMANTIC_PROMPTS = [
    "Find transactions similar to my gym membership",
    "Things like my coffee purchases",
    "Charges similar to streaming subscriptions",
]


def _post_json(
    base: str,
    path: str,
    body: dict[str, Any],
    token: Optional[str],
    timeout: float = 120.0,
) -> tuple[int, Any]:
    url = base.rstrip("/") + path
    headers: dict[str, str] = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    r = requests.post(
        url,
        json=body,
        headers=headers,
        timeout=timeout,
    )
    try:
        data = r.json()
    except Exception:
        data = r.text
    return r.status_code, data


def _get_json(base: str, path: str, token: str, timeout: float = 30.0) -> tuple[int, Any]:
    url = base.rstrip("/") + path
    r = requests.get(
        url,
        headers={"Authorization": f"Bearer {token}"},
        timeout=timeout,
    )
    try:
        data = r.json()
    except Exception:
        data = r.text
    return r.status_code, data


def login(base: str, email: str, password: str) -> str:
    code, data = _post_json(
        base,
        "/auth/login",
        {"email": email, "password": password},
        token=None,
    )
    if code != 200 or not isinstance(data, dict) or "token" not in data:
        raise SystemExit(f"Login failed HTTP {code}: {data}")
    return str(data["token"])


def print_stats_table(summary: dict[str, Any]) -> None:
    """Print metric | avg | p50 | p95 | p99 | min | max for _ms and compatible dicts."""
    keys = sorted(summary.keys())
    row_fmt = "{:<32} {:>10} {:>10} {:>10} {:>10} {:>10} {:>10}"
    print(row_fmt.format("metric", "avg", "p50", "p95", "p99", "min", "max"))
    print("-" * 96)
    for name in keys:
        if name in ("sample_size", "nl2sql_truncation_rate_pct"):
            continue
        block = summary[name]
        if not isinstance(block, dict):
            continue
        if "p50" not in block and "avg" not in block:
            continue
        if "p50" in block:
            print(
                row_fmt.format(
                    name[:32],
                    _fmt(block.get("avg")),
                    _fmt(block.get("p50")),
                    _fmt(block.get("p95")),
                    _fmt(block.get("p99")),
                    _fmt(block.get("min")),
                    _fmt(block.get("max")),
                )
            )
        else:
            print(
                row_fmt.format(
                    name[:32],
                    _fmt(block.get("avg")),
                    "—",
                    "—",
                    "—",
                    _fmt(block.get("min")),
                    _fmt(block.get("max")),
                )
            )
    tr = summary.get("nl2sql_truncation_rate_pct")
    if tr is not None:
        print(f"\nnl2sql_truncation_rate_pct: {tr}")


def _fmt(v: Any) -> str:
    if v is None:
        return "—"
    if isinstance(v, (int, float)):
        return f"{v:.3f}".rstrip("0").rstrip(".")
    return str(v)


def print_pct_breakdown(summary: dict[str, Any]) -> None:
    print("\nAverage E2E breakdown (%), from summary averages:")
    for key in ("nl2sql_pct", "gemini_pct", "other_pct"):
        block = summary.get(key)
        if isinstance(block, dict) and block.get("avg") is not None:
            print(f"  {key}: avg={block['avg']:.3f}")
        else:
            print(f"  {key}: (no data)")


def main() -> None:
    p = argparse.ArgumentParser(description="Benchmark customer ask + metrics summary")
    p.add_argument(
        "--base-url",
        default=os.getenv("ALLOYFINANCE_API", "http://127.0.0.1:8000"),
        help="API base URL",
    )
    p.add_argument("--email", default=os.getenv("ALLOYFINANCE_EMAIL", "foo@bar.com"))
    p.add_argument("--password", default=os.getenv("ALLOYFINANCE_PASSWORD", "password"))
    p.add_argument("--token", default=os.getenv("ALLOYFINANCE_TOKEN"), help="Skip login if set")
    p.add_argument("-n", "--iterations", type=int, default=2, help="Repeat full prompt set N times")
    p.add_argument(
        "--nl2sql-prompts",
        nargs="*",
        default=None,
        help="Override NL2SQL-style prompts",
    )
    p.add_argument(
        "--semantic-prompts",
        nargs="*",
        default=None,
        help="Override semantic-style prompts",
    )
    args = p.parse_args()

    base = args.base_url.rstrip("/")
    token: Optional[str] = args.token
    if not token:
        token = login(base, args.email, args.password)

    nl_list = args.nl2sql_prompts if args.nl2sql_prompts is not None else NL2SQL_PROMPTS
    sem_list = args.semantic_prompts if args.semantic_prompts is not None else SEMANTIC_PROMPTS

    errors: list[str] = []
    for _ in range(max(1, args.iterations)):
        for msg in nl_list:
            code, data = _post_json(base, "/api/customer/ask", {"message": msg}, token)
            if code != 200:
                errors.append(f"NL2SQL prompt failed HTTP {code}: {msg!r} -> {data}")
        for msg in sem_list:
            code, data = _post_json(base, "/api/customer/ask", {"message": msg}, token)
            if code != 200:
                errors.append(f"Semantic prompt failed HTTP {code}: {msg!r} -> {data}")

    code, summary = _get_json(base, "/metrics/summary", token)
    if code != 200:
        raise SystemExit(f"GET /metrics/summary failed HTTP {code}: {summary}")

    print_stats_table(summary if isinstance(summary, dict) else {})
    print_pct_breakdown(summary if isinstance(summary, dict) else {})

    if errors:
        print("\nWarnings during benchmark:")
        for e in errors:
            print(f"  {e}")

    ts = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
    out_path = os.path.join(os.getcwd(), f"benchmark_results_{ts}.json")
    payload = {
        "completed_at": datetime.now(timezone.utc).isoformat(),
        "base_url": base,
        "iterations": args.iterations,
        "nl2sql_prompts": nl_list,
        "semantic_prompts": sem_list,
        "errors": errors,
        "summary": summary,
    }
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2, default=str)
    print(f"\nSaved: {out_path}")


if __name__ == "__main__":
    main()
