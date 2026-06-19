"""Deterministic synthetic transaction generation."""

from __future__ import annotations

import csv
import random
import uuid
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from io import StringIO


US_CITIES = [
    "New York",
    "San Francisco",
    "Los Angeles",
    "Seattle",
    "Austin",
    "Chicago",
    "Boston",
    "Denver",
    "Miami",
    "Atlanta",
    "Portland",
    "San Diego",
]

PAYMENT_METHODS = ["credit_card", "debit_card", "bank_transfer", "cash", "mobile_wallet"]


@dataclass(frozen=True)
class CategoryRule:
    merchant_category: str
    transaction_type: str
    min_amount: float
    max_amount: float
    payment_methods: list[str]


CATEGORY_RULES: dict[str, CategoryRule] = {
    "groceries": CategoryRule("supermarket", "debit", -250.0, -40.0, ["debit_card", "credit_card", "mobile_wallet"]),
    "dining": CategoryRule("restaurant", "debit", -180.0, -15.0, ["credit_card", "debit_card", "mobile_wallet"]),
    "subscriptions": CategoryRule(
        "subscription_service", "debit", -40.0, -5.0, ["credit_card", "debit_card", "bank_transfer"]
    ),
    "shopping": CategoryRule("ecommerce", "debit", -600.0, -20.0, ["credit_card", "debit_card", "mobile_wallet"]),
    "healthcare": CategoryRule("pharmacy", "debit", -300.0, -20.0, ["debit_card", "credit_card", "cash"]),
    "utilities": CategoryRule("utility_provider", "debit", -280.0, -60.0, ["bank_transfer", "debit_card"]),
    "rent": CategoryRule("landlord", "debit", -3500.0, -800.0, ["bank_transfer"]),
    "income": CategoryRule("employer", "credit", 2000.0, 8000.0, ["bank_transfer"]),
    "transportation": CategoryRule("rideshare", "debit", -120.0, -8.0, ["credit_card", "debit_card", "mobile_wallet"]),
    "entertainment": CategoryRule("streaming_service", "debit", -160.0, -10.0, ["credit_card", "debit_card"]),
    "travel": CategoryRule("airline", "debit", -2000.0, -200.0, ["credit_card", "debit_card", "bank_transfer"]),
}

# 25 recognizable merchants mapped to allowed merchant categories and spending categories.
MERCHANTS = [
    {"name": "Trader Joes", "merchant_category": "supermarket", "spending_category": "groceries"},
    {"name": "Whole Foods", "merchant_category": "supermarket", "spending_category": "groceries"},
    {"name": "Safeway", "merchant_category": "supermarket", "spending_category": "groceries"},
    {"name": "Costco", "merchant_category": "supermarket", "spending_category": "groceries"},
    {"name": "Starbucks", "merchant_category": "restaurant", "spending_category": "dining"},
    {"name": "Chipotle", "merchant_category": "restaurant", "spending_category": "dining"},
    {"name": "Shake Shack", "merchant_category": "restaurant", "spending_category": "dining"},
    {"name": "Spotify", "merchant_category": "subscription_service", "spending_category": "subscriptions"},
    {"name": "Disney Plus", "merchant_category": "streaming_service", "spending_category": "subscriptions"},
    {"name": "Netflix", "merchant_category": "streaming_service", "spending_category": "subscriptions"},
    {"name": "Hulu", "merchant_category": "streaming_service", "spending_category": "entertainment"},
    {"name": "Amazon", "merchant_category": "ecommerce", "spending_category": "shopping"},
    {"name": "Target", "merchant_category": "ecommerce", "spending_category": "shopping"},
    {"name": "Nike", "merchant_category": "ecommerce", "spending_category": "shopping"},
    {"name": "CVS", "merchant_category": "pharmacy", "spending_category": "healthcare"},
    {"name": "Walgreens", "merchant_category": "pharmacy", "spending_category": "healthcare"},
    {"name": "PG and E", "merchant_category": "utility_provider", "spending_category": "utilities"},
    {"name": "Comcast", "merchant_category": "utility_provider", "spending_category": "utilities"},
    {"name": "AT and T", "merchant_category": "utility_provider", "spending_category": "utilities"},
    {"name": "Sunset Property Management", "merchant_category": "landlord", "spending_category": "rent"},
    {"name": "Bayview Apartments", "merchant_category": "landlord", "spending_category": "rent"},
    {"name": "Cedar Grove Leasing", "merchant_category": "landlord", "spending_category": "rent"},
    {"name": "BART", "merchant_category": "rideshare", "spending_category": "transportation"},
    {"name": "Uber", "merchant_category": "rideshare", "spending_category": "transportation"},
    {"name": "Lyft", "merchant_category": "rideshare", "spending_category": "transportation"},
    {"name": "Delta", "merchant_category": "airline", "spending_category": "travel"},
    {"name": "Marriott", "merchant_category": "hotel", "spending_category": "travel"},
    {"name": "Payroll Direct", "merchant_category": "employer", "spending_category": "income"},
]

CATEGORY_WEIGHTS = {
    "groceries": 18,
    "dining": 16,
    "subscriptions": 8,
    "shopping": 14,
    "healthcare": 8,
    "utilities": 8,
    "rent": 4,
    "income": 5,
    "transportation": 10,
    "entertainment": 5,
    "travel": 4,
}


def _random_timestamp(rng: random.Random, months_back: int) -> str:
    now = datetime.now(timezone.utc)
    start = now - timedelta(days=30 * months_back)
    delta_seconds = int((now - start).total_seconds())
    ts = start + timedelta(seconds=rng.randint(0, max(delta_seconds, 1)))
    return ts.isoformat().replace("+00:00", "Z")


def _sample_category(rng: random.Random) -> str:
    categories = list(CATEGORY_WEIGHTS.keys())
    weights = list(CATEGORY_WEIGHTS.values())
    return rng.choices(categories, weights=weights, k=1)[0]


def _sample_merchant(rng: random.Random, spending_category: str, merchant_category: str) -> str:
    matches = [
        m["name"]
        for m in MERCHANTS
        if m["spending_category"] == spending_category and m["merchant_category"] == merchant_category
    ]
    if not matches:
        matches = [m["name"] for m in MERCHANTS if m["merchant_category"] == merchant_category]
    if not matches:
        matches = [m["name"] for m in MERCHANTS]
    return rng.choice(matches)


def _sample_amount(rng: random.Random, rule: CategoryRule) -> float:
    value = round(rng.uniform(rule.min_amount, rule.max_amount), 2)
    if rule.transaction_type == "credit":
        return abs(value)
    if rule.transaction_type == "debit":
        return -abs(value)
    return value


def build_transactions(rows: int = 5000, seed: int = 42, months_back: int = 6) -> list[dict]:
    rng = random.Random(seed)
    transactions: list[dict] = []
    for _ in range(rows):
        spending_category = _sample_category(rng)
        rule = CATEGORY_RULES[spending_category]
        merchant_name = _sample_merchant(rng, spending_category, rule.merchant_category)
        payment_method = rng.choice(rule.payment_methods)
        transaction = {
            "transaction_id": str(uuid.uuid4()),
            "timestamp": _random_timestamp(rng, months_back),
            "amount": _sample_amount(rng, rule),
            "merchant_name": merchant_name,
            "merchant_category": rule.merchant_category,
            "spending_category": spending_category,
            "transaction_type": rule.transaction_type,
            "payment_method": payment_method,
            "city": rng.choice(US_CITIES),
            "country": "US",
            "currency": "USD",
        }
        transactions.append(transaction)
    return transactions


def validate_transactions(transactions: list[dict], expected_rows: int) -> None:
    if len(transactions) != expected_rows:
        raise ValueError(f"Expected {expected_rows} rows but generated {len(transactions)}")

    seen_ids: set[str] = set()
    for row in transactions:
        tx_id = row["transaction_id"]
        if tx_id in seen_ids:
            raise ValueError(f"Duplicate transaction_id generated: {tx_id}")
        seen_ids.add(tx_id)

        spending_category = row["spending_category"]
        if spending_category not in CATEGORY_RULES:
            raise ValueError(f"Invalid spending_category: {spending_category}")
        rule = CATEGORY_RULES[spending_category]

        if row["merchant_category"] != rule.merchant_category:
            raise ValueError(f"Invalid merchant_category for {spending_category}: {row['merchant_category']}")
        if row["transaction_type"] != rule.transaction_type:
            raise ValueError(f"Invalid transaction_type for {spending_category}: {row['transaction_type']}")
        if row["payment_method"] not in PAYMENT_METHODS:
            raise ValueError(f"Invalid payment_method: {row['payment_method']}")

        amount = float(row["amount"])
        if not (min(rule.min_amount, rule.max_amount) <= amount <= max(rule.min_amount, rule.max_amount)):
            raise ValueError(f"Amount out of range for {spending_category}: {amount}")
        if row["transaction_type"] == "credit" and amount <= 0:
            raise ValueError(f"Credit must be positive: {amount}")
        if row["transaction_type"] == "debit" and amount >= 0:
            raise ValueError(f"Debit must be negative: {amount}")


def transactions_to_csv_text(transactions: list[dict]) -> str:
    if not transactions:
        return ""
    out = StringIO()
    fieldnames = [
        "transaction_id",
        "timestamp",
        "amount",
        "merchant_name",
        "merchant_category",
        "spending_category",
        "transaction_type",
        "payment_method",
        "city",
        "country",
        "currency",
    ]
    writer = csv.DictWriter(out, fieldnames=fieldnames)
    writer.writeheader()
    writer.writerows(transactions)
    return out.getvalue().strip()
