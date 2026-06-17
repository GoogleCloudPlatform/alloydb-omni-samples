import csv
import json
import os
import re
import time
import uuid
import logging
import asyncio
from collections import Counter, defaultdict
from datetime import datetime, timezone, timedelta
from enum import Enum
from functools import partial
from pathlib import Path
from typing import Any, Optional

import numpy as np
from sklearn.cluster import KMeans

import jwt
from dotenv import load_dotenv
import asyncpg
import requests
import google.auth
import google.auth.transport.requests
from fastapi import FastAPI, HTTPException, Query, Depends, Header
from fastapi.concurrency import run_in_threadpool
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field
from passlib.context import CryptContext

from app.ai_analysis import (
    generate_fallback_finance_insight,
    generate_finance_insight_with_gemini,
    generate_monthly_report_comments_with_gemini,
    post_vertex_generate_content,
)

load_dotenv()

# Standard logger for any backend errors or debugging information.
logger = logging.getLogger(__name__)

# FastAPI application instance that defines our HTTP API surface.
app = FastAPI(title="AlloyFinance API", version="0.1.0")

# Allowed origins for browser-based requests. We read these from env vars
# so the same backend can be deployed locally and in production safely.
CORS_ORIGINS = [
    origin.strip()
    for origin in os.getenv(
        "CORS_ORIGINS",
        "http://localhost:5173,http://127.0.0.1:5173,http://localhost:4173,http://127.0.0.1:4173",
    ).split(",")
    if origin.strip()
]

app.add_middleware(
    CORSMiddleware,
    allow_origins=CORS_ORIGINS,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

from starlette.middleware.base import BaseHTTPMiddleware

from mcp.constants import (
    CLUSTERING,
    GENERAL,
    MONTHLY_REPORTS,
    NL2SQL,
    ROUTER_TOOLS,
    SEMANTIC_SEARCH,
)
from mcp.tool_router import CustomerToolRouterError, route_user_message

from app.monitoring import metrics_store, monotonic_ms

class COOPMiddleware(BaseHTTPMiddleware):
    async def dispatch(self, request, call_next):
        response = await call_next(request)
        response.headers["Cross-Origin-Opener-Policy"] = "unsafe-none"
        return response

app.add_middleware(COOPMiddleware)

DATABASE_URL = os.getenv("DATABASE_URL")
if not DATABASE_URL:
    raise ValueError("DATABASE_URL is not set")

JWT_SECRET = os.getenv("JWT_SECRET")
if not JWT_SECRET:
    raise ValueError("JWT_SECRET is not set")

DATABASE_SSL_MODE = os.getenv("DATABASE_SSL_MODE", "disable").lower()
GEMINI_API_KEY = os.getenv("GEMINI_API_KEY", "").strip()
GEMINI_MODEL = os.getenv("GEMINI_MODEL", "").strip() or "gemini-2.0-flash"
GOOGLE_CLOUD_PROJECT = os.getenv("GOOGLE_CLOUD_PROJECT", "").strip()
GOOGLE_CLOUD_LOCATION = os.getenv("GOOGLE_CLOUD_LOCATION", "us-central1").strip()

_adc_creds = None
_adc_session = None

def _get_access_token() -> str:
    global _adc_creds, _adc_session
    if _adc_creds is None:
        _adc_creds, _ = google.auth.default(scopes=["https://www.googleapis.com/auth/cloud-platform"])
        _adc_session = google.auth.transport.requests.Request()
    if not _adc_creds.valid:
        _adc_creds.refresh(_adc_session)
    return _adc_creds.token

def _embed_text(text: str) -> list[float]:
    url = (
        f"https://{GOOGLE_CLOUD_LOCATION}-aiplatform.googleapis.com/v1/projects/{GOOGLE_CLOUD_PROJECT}"
        f"/locations/{GOOGLE_CLOUD_LOCATION}/publishers/google/models/text-embedding-005:predict"
    )
    resp = requests.post(
        url,
        json={"instances": [{"content": text}]},
        headers={"Authorization": f"Bearer {_get_access_token()}", "Content-Type": "application/json"},
        timeout=30,
    )
    resp.raise_for_status()
    return resp.json()["predictions"][0]["embeddings"]["values"]

# JWT configuration for user session tokens.
JWT_ALGORITHM = "HS256"
JWT_EXPIRATION_HOURS = 24
pwd_context = CryptContext(schemes=["bcrypt"], deprecated="auto")

DEV_USER_EMAIL = "foo@bar.com"
DEV_USER_PASSWORD = "password"

# Path to the shared transaction seed dataset used for new users.
# We only need this file when a user logs in for the first time.
SEED_TRANSACTIONS_FILE = Path(__file__).resolve().parents[1] / "synthetic-data" / "output_data" / "transactions.csv"
_cached_seed_transactions: list[dict] = []

# Connection pool for the PostgreSQL/AlloyDB database.
pool: asyncpg.Pool = None

def _load_seed_transactions() -> list[dict]:
    """Read the seed CSV once and cache it for use by subsequent users.

    This function converts the CSV rows into Python dicts with typed
    values so they can be inserted into the database directly.
    """
    global _cached_seed_transactions
    if _cached_seed_transactions:
        return _cached_seed_transactions

    if not SEED_TRANSACTIONS_FILE.exists():
        return []

    with open(SEED_TRANSACTIONS_FILE, newline="", encoding="utf-8") as fh:
        reader = csv.DictReader(fh)
        for row in reader:
            timestamp = row["timestamp"]
            if timestamp.endswith("Z"):
                timestamp = timestamp[:-1] + "+00:00"
            _cached_seed_transactions.append({
                "timestamp": datetime.fromisoformat(timestamp),
                "amount": float(row["amount"]),
                "merchant_name": row["merchant_name"],
                "merchant_category": row["merchant_category"],
                "spending_category": row["spending_category"],
                "transaction_type": row["transaction_type"],
                "payment_method": row["payment_method"],
                "city": row["city"],
                "country": row["country"],
                "currency": row["currency"],
                "description": None,
            })

    return _cached_seed_transactions

async def _seed_transactions_for_user(conn: asyncpg.Connection, user_id: int) -> None:
    # transactions_2 is the active table; no seeding needed
    return

    seed_rows = _load_seed_transactions()
    if not seed_rows:
        return

    await conn.executemany(
        """INSERT INTO transactions (
               transaction_id, user_id, timestamp, amount, merchant_name,
               merchant_category, spending_category, transaction_type,
               payment_method, city, country, currency, description
           ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)""",
        [
            (
                uuid.uuid4(),
                user_id,
                row["timestamp"],
                row["amount"],
                row["merchant_name"],
                row["merchant_category"],
                row["spending_category"],
                row["transaction_type"],
                row["payment_method"],
                row["city"],
                row["country"],
                row["currency"],
                row["description"],
            )
            for row in seed_rows
        ],
    )
def hash_password(password: str) -> str:
    return pwd_context.hash(password)


def verify_password(plain_password: str, hashed_password: str) -> bool:
    return pwd_context.verify(plain_password, hashed_password)


def validate_password(password: str) -> None:
    if len(password) < 8:
        raise HTTPException(status_code=422, detail="Password must be at least 8 characters")

@app.on_event("startup")
async def startup():
    """Initialize the database connection pool and ensure required schema exists.

    This runs once when the FastAPI app starts, so the app can safely assume
    that the tables exist for users, items, transactions, and budget preferences.
    """
    global pool
    pool = await asyncpg.create_pool(
        DATABASE_URL,
        ssl="require" if DATABASE_SSL_MODE == "require" else False,
        min_size=5,
        max_size=20,
        command_timeout=60,
        max_inactive_connection_lifetime=300,
    )
    async with pool.acquire() as conn:
        await conn.execute("""
            CREATE TABLE IF NOT EXISTS items (
                id SERIAL PRIMARY KEY,
                name TEXT NOT NULL,
                value TEXT,
                created_at TIMESTAMPTZ DEFAULT NOW()
            )
        """)
        await conn.execute("""
            CREATE TABLE IF NOT EXISTS users (
                id SERIAL PRIMARY KEY,
                google_id TEXT UNIQUE,
                email TEXT UNIQUE NOT NULL,
                password_hash TEXT,
                name TEXT,
                picture TEXT,
                created_at TIMESTAMPTZ DEFAULT NOW()
            )
        """)
        await conn.execute("""
            ALTER TABLE users
            ADD COLUMN IF NOT EXISTS password_hash TEXT
        """)
        await conn.execute("""
            ALTER TABLE users
            ALTER COLUMN google_id DROP NOT NULL
        """)
        await conn.execute("""
            CREATE TABLE IF NOT EXISTS transactions (
                transaction_id UUID PRIMARY KEY,
                user_id INTEGER NOT NULL,
                timestamp TIMESTAMPTZ NOT NULL,
                amount NUMERIC(18, 2) NOT NULL,
                merchant_name TEXT NOT NULL,
                merchant_category TEXT NOT NULL,
                spending_category TEXT NOT NULL,
                transaction_type TEXT NOT NULL,
                payment_method TEXT NOT NULL,
                city TEXT NOT NULL,
                country TEXT NOT NULL,
                currency TEXT NOT NULL,
                description TEXT
            )
        """)
        await conn.execute("""
            ALTER TABLE transactions ADD COLUMN IF NOT EXISTS description TEXT
        """)
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
            ALTER TABLE transactions_2 ADD COLUMN IF NOT EXISTS embedding vector(768)
        """)
        await conn.execute("""
            ALTER TABLE transactions ADD COLUMN IF NOT EXISTS user_id INTEGER
        """)
        await conn.execute("""
            UPDATE transactions
            SET user_id = (SELECT id FROM users WHERE email = 'nikhil.m@berkeley.edu' LIMIT 1)
            WHERE user_id IS NULL
        """)
        await conn.execute(
            """INSERT INTO users (email, password_hash, name, picture)
               VALUES ($1, $2, $3, $4)
               ON CONFLICT (email) DO UPDATE SET
                 password_hash = EXCLUDED.password_hash,
                 name = EXCLUDED.name
            """,
            DEV_USER_EMAIL,
            hash_password(DEV_USER_PASSWORD),
            "Dev User",
            None,
        )
        # Budget preferences: one row per category, amount is the user's spending limit
        await conn.execute("""
            CREATE TABLE IF NOT EXISTS "BudgetPrefs" (
                category TEXT PRIMARY KEY,
                amount   NUMERIC(18, 2) NOT NULL
            )
        """)
        await conn.execute("""
            CREATE TABLE IF NOT EXISTS monthly_reports (
                user_id INTEGER NOT NULL,
                year_month TEXT NOT NULL,
                total_spent NUMERIC(18, 2) NOT NULL,
                comments TEXT NOT NULL,
                suggestions_json JSONB NOT NULL DEFAULT '[]'::jsonb,
                category_breakdown_json JSONB NOT NULL DEFAULT '[]'::jsonb,
                merchant_breakdown_json JSONB NOT NULL DEFAULT '[]'::jsonb,
                generated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                PRIMARY KEY (user_id, year_month)
            )
        """)
        # Backward-compatible migration for existing environments that already had
        # a monthly_reports table with an older shape.
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS total_spent NUMERIC(18, 2)
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS comments TEXT
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS suggestions_json JSONB DEFAULT '[]'::jsonb
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS category_breakdown_json JSONB DEFAULT '[]'::jsonb
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS merchant_breakdown_json JSONB DEFAULT '[]'::jsonb
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS generated_at TIMESTAMPTZ DEFAULT NOW()
        """)
        # Legacy monthly report columns from older workflow versions.
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS summary TEXT
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS highlights JSONB DEFAULT '[]'::jsonb
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS suggestions JSONB DEFAULT '[]'::jsonb
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ADD COLUMN IF NOT EXISTS totals JSONB DEFAULT '{}'::jsonb
        """)
        await conn.execute("""
            UPDATE monthly_reports
            SET total_spent = COALESCE(total_spent, 0),
                suggestions_json = COALESCE(suggestions_json, '[]'::jsonb),
                category_breakdown_json = COALESCE(category_breakdown_json, '[]'::jsonb),
                merchant_breakdown_json = COALESCE(merchant_breakdown_json, '[]'::jsonb),
                summary = COALESCE(summary, comments, 'No monthly commentary available yet.'),
                comments = COALESCE(comments, summary, 'No monthly commentary available yet.'),
                highlights = COALESCE(highlights, '[]'::jsonb),
                suggestions = COALESCE(suggestions, suggestions_json, '[]'::jsonb),
                totals = COALESCE(totals, '{}'::jsonb),
                generated_at = COALESCE(generated_at, NOW())
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN total_spent SET NOT NULL
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN comments SET NOT NULL
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN suggestions_json SET NOT NULL
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN category_breakdown_json SET NOT NULL
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN merchant_breakdown_json SET NOT NULL
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN generated_at SET NOT NULL
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN summary SET NOT NULL
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN highlights SET NOT NULL
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN suggestions SET NOT NULL
        """)
        await conn.execute("""
            ALTER TABLE monthly_reports
            ALTER COLUMN totals SET NOT NULL
        """)
        await conn.execute("CREATE EXTENSION IF NOT EXISTS vector")
        await conn.execute("""
            ALTER TABLE transactions
            ADD COLUMN IF NOT EXISTS embedding vector(768)
        """)
        await conn.execute("CREATE EXTENSION IF NOT EXISTS vector")
        await conn.execute("""
            ALTER TABLE transactions
            ADD COLUMN IF NOT EXISTS embedding vector(768)
        """)

@app.on_event("shutdown")
async def shutdown():
    """Close the database pool cleanly when the application shuts down."""
    await pool.close()

# --- Auth ---
# Authentication is based on Google sign-in and JWT session tokens.

class SignupRequest(BaseModel):
    email: str
    password: str
    name: Optional[str] = None


class LoginRequest(BaseModel):
    email: str
    password: str

def create_jwt(user_id: str, email: str) -> str:
    """Build a signed JWT for the authenticated user.

    The token contains the user id and email and expires after a short period.
    This token is returned to the frontend and used for all subsequent API calls.
    """
    payload = {
        "sub": user_id,
        "email": email,
        "exp": datetime.now(timezone.utc) + timedelta(hours=JWT_EXPIRATION_HOURS),
        "iat": datetime.now(timezone.utc),
    }
    return jwt.encode(payload, JWT_SECRET, algorithm=JWT_ALGORITHM)

async def get_current_user(authorization: str = Header(...)) -> dict:
    """Extract and verify the bearer token from the Authorization header.

    If the token is valid, return a minimal user identity dict for route dependencies.
    If invalid, raise a 401 so protected endpoints reject the request.
    """
    if not authorization.startswith("Bearer "):
        raise HTTPException(
            status_code=401,
            detail="Authorization header must be in format: Bearer <token>",
        )
    token = authorization[len("Bearer "):]
    try:
        payload = jwt.decode(token, JWT_SECRET, algorithms=[JWT_ALGORITHM])
    except jwt.ExpiredSignatureError:
        raise HTTPException(status_code=401, detail="Token expired")
    except jwt.InvalidTokenError:
        raise HTTPException(status_code=401, detail="Invalid token")
    return {"user_id": payload["sub"], "email": payload["email"]}

@app.post("/auth/signup")
async def signup(body: SignupRequest):
    email = body.email.strip().lower()
    if not email or "@" not in email:
        raise HTTPException(status_code=422, detail="Valid email is required")
    validate_password(body.password)
    name = body.name.strip() if body.name else None
    password_hash = hash_password(body.password)

    async with pool.acquire() as conn:
        try:
            row = await conn.fetchrow(
                """INSERT INTO users (email, password_hash, name, picture)
                   VALUES ($1, $2, $3, $4)
                   RETURNING id, email, name, picture""",
                email, password_hash, name, None,
            )
        except asyncpg.UniqueViolationError:
            raise HTTPException(status_code=409, detail="User already exists")
        except Exception as exc:
            logger.exception("Signup failed")
            raise HTTPException(status_code=500, detail="Signup failed") from exc

        await _seed_transactions_for_user(conn, int(row["id"]))

    user_id = str(row["id"])
    token = create_jwt(user_id, email)
    return {
        "token": token,
        "user": {
            "id": user_id,
            "email": row["email"],
            "name": row["name"],
            "picture": row["picture"],
        },
    }


@app.post("/auth/login")
async def login(body: LoginRequest):
    email = body.email.strip().lower()
    if not email or "@" not in email:
        raise HTTPException(status_code=422, detail="Valid email is required")

    async with pool.acquire() as conn:
        row = await conn.fetchrow(
            """SELECT id, email, name, picture, password_hash
               FROM users
               WHERE email = $1""",
            email,
        )
        if not row or not row["password_hash"]:
            raise HTTPException(status_code=401, detail="Invalid credentials")

        if not verify_password(body.password, row["password_hash"]):
            raise HTTPException(status_code=401, detail="Invalid credentials")

    user_id = str(row["id"])
    token = create_jwt(user_id, row["email"])
    return {
        "token": token,
        "user": {
            "id": user_id,
            "email": row["email"],
            "name": row["name"],
            "picture": row["picture"],
        },
    }

@app.get("/auth/me")
async def get_me(user: dict = Depends(get_current_user)):
    """Return the current authenticated user's profile.

    Also trigger transaction seeding for any existing users who have not yet
    received the shared dataset. This makes first-login behavior idempotent.
    """
    async with pool.acquire() as conn:
        row = await conn.fetchrow(
            "SELECT id, email, name, picture FROM users WHERE id = $1",
            int(user["user_id"]),
        )
        if not row:
            raise HTTPException(status_code=404, detail="User not found")

        await _seed_transactions_for_user(conn, int(row["id"]))

        return {
            "id": str(row["id"]),
            "email": row["email"],
            "name": row["name"],
            "picture": row["picture"],
        }

# --- Spending Category Enum ---
# Define the allowed spending categories that can be used in transactions
# and budget preference requests.
class SpendingCategory(str, Enum):
    """Valid spending categories — shared by transactions and budget preferences."""
    shopping    = "shopping"
    gifts       = "gifts"
    household   = "household"
    office      = "office"
    clothing    = "clothing"
    hobbies     = "hobbies"
    food        = "food"
    beauty      = "beauty"
    toys        = "toys"
    stationery  = "stationery"


# --- Item Models ---
# Request payload definitions for the generic items CRUD endpoints.

class ItemCreate(BaseModel):
    name: str
    value: Optional[str] = None

class ItemUpdate(BaseModel):
    name: Optional[str] = None
    value: Optional[str] = None


# --- Budget Models ---
# Request payload definitions for budget preference operations.

class BudgetCreate(BaseModel):
    """Request body for creating or updating a budget limit."""
    category: SpendingCategory  # validated against the enum; FastAPI returns 422 on invalid value
    amount: float               # spending limit in USD; must be > 0


# --- Transaction Models ---

class TransactionCreate(BaseModel):
    amount: float
    merchant_name: str
    spending_category: str
    quantity: int = 1
    country: str = "United Kingdom"
    item_description: Optional[str] = None


def _serialize_transaction(row: asyncpg.Record) -> dict:
    d = dict(row)
    d["transaction_id"] = str(d["transaction_id"])
    d["timestamp"] = d["timestamp"].isoformat()
    d["amount"] = float(d["amount"])
    d.pop("embedding", None)
    d.pop("total_matched", None)
    return d


# --- Clustering helpers ---

def _resolve_time_range(time_range: str | None, start_date: str | None, end_date: str | None):
    """Return (start_dt, end_dt, label) as UTC-aware datetimes, or (None, None, 'all')."""
    now = datetime.now(timezone.utc)
    if time_range and time_range != "all":
        if time_range == "7d":
            return now - timedelta(days=7), now, "7d"
        if time_range == "30d":
            return now - timedelta(days=30), now, "30d"
        if time_range == "90d":
            return now - timedelta(days=90), now, "90d"
        if time_range == "ytd":
            return now.replace(month=1, day=1, hour=0, minute=0, second=0, microsecond=0), now, "ytd"
    if start_date or end_date:
        start = datetime.fromisoformat(start_date).replace(tzinfo=timezone.utc) if start_date else None
        end = datetime.fromisoformat(end_date).replace(tzinfo=timezone.utc) if end_date else now
        label = f"{start_date or 'start'}..{end_date or 'now'}"
        return start, end, label
    return None, None, "all"


def _prior_period(start_dt, end_dt):
    if start_dt is None:
        return None, None
    delta = end_dt - start_dt
    return start_dt - delta, start_dt


def _derive_cluster_label(members: list) -> tuple:
    """Return (fallback_label, majority_category, top_merchants, top_terms).

    majority_category is used as a stable period-comparison key.
    fallback_label is used when Gemini is unavailable.
    """
    categories = [row["spending_category"] for _, row in members]
    merchants = [row["merchant_name"] for _, row in members]
    majority_cat = Counter(categories).most_common(1)[0][0]
    top_merchants = [m for m, _ in Counter(merchants).most_common(5)]
    fallback_label = majority_cat.replace("_", " ").title()
    if top_merchants:
        fallback_label = f"{fallback_label} – {', '.join(top_merchants[:3])}"
    return fallback_label, majority_cat, top_merchants


_gemini_label_cache: dict[str, list[str]] = {}


def _gemini_post(prompt: str, temperature: float = 0.3) -> str:
    """POST to Vertex Gemini and return the text response. Retries up to 3x on 429/503."""
    if not GOOGLE_CLOUD_PROJECT:
        raise RuntimeError("GOOGLE_CLOUD_PROJECT is not configured for Vertex Gemini")
    for attempt in range(3):
        try:
            body, _response_bytes = post_vertex_generate_content(
                prompt=prompt,
                generation_config={
                    "temperature": temperature,
                    "responseMimeType": "application/json",
                },
                model=GEMINI_MODEL,
                timeout_s=30,
            )
            return body["candidates"][0]["content"]["parts"][0]["text"]
        except requests.HTTPError as exc:
            status_code = exc.response.status_code if exc.response is not None else None
            if status_code not in {429, 503}:
                raise
            wait = 2 ** attempt  # 1s, 2s, 4s
            logger.warning("Vertex Gemini %s rate limit, retrying in %ds (attempt %d/3)", status_code, wait, attempt + 1)
            time.sleep(wait)
            continue
        raise
    raise RuntimeError("Vertex Gemini retries exhausted")


def _vertex_gemini_post(prompt: str, temperature: float = 0.3) -> str:
    """Call Gemini via Vertex AI using ADC and return response text."""
    if not GOOGLE_CLOUD_PROJECT:
        raise ValueError("GOOGLE_CLOUD_PROJECT is not configured")
    url = (
        f"https://{GOOGLE_CLOUD_LOCATION}-aiplatform.googleapis.com/v1/projects/{GOOGLE_CLOUD_PROJECT}"
        f"/locations/{GOOGLE_CLOUD_LOCATION}/publishers/google/models/{GEMINI_MODEL}:generateContent"
    )
    body = {
        "contents": [{"role": "user", "parts": [{"text": prompt}]}],
        "generationConfig": {"temperature": temperature, "responseMimeType": "application/json"},
    }
    for attempt in range(3):
        resp = requests.post(
            url,
            json=body,
            headers={
                "Authorization": f"Bearer {_get_access_token()}",
                "Content-Type": "application/json",
            },
            timeout=30,
        )
        if resp.status_code in (429, 503):
            wait = 2 ** attempt  # 1s, 2s, 4s
            logger.warning("Vertex Gemini %s, retrying in %ds (attempt %d/3)", resp.status_code, wait, attempt + 1)
            time.sleep(wait)
            continue
        resp.raise_for_status()
        return resp.json()["candidates"][0]["content"]["parts"][0]["text"]
    resp.raise_for_status()  # re-raise after exhausting retries
    return ""  # unreachable


def _gemini_label_clusters(clusters_meta: list[dict]) -> list[str]:
    """Call Gemini once with all cluster summaries; return a label per cluster.

    clusters_meta: list of {"merchants": [...], "terms": [...], "majority_category": str}
    Falls back to majority_category title if Gemini unavailable or fails.
    """
    fallbacks = [
        c["majority_category"].replace("_", " ").title()
        for c in clusters_meta
    ]
    if not GOOGLE_CLOUD_PROJECT:
        return fallbacks

    cache_key = json.dumps(clusters_meta, sort_keys=True)
    if cache_key in _gemini_label_cache:
        return _gemini_label_cache[cache_key]

    payload = [
        {
            "cluster_index": i,
            "top_merchants": c["merchants"][:5],
            "top_terms": c["terms"][:6],
            "majority_category": c["majority_category"],
        }
        for i, c in enumerate(clusters_meta)
    ]
    prompt = (
        "You are a personal finance assistant analysing spending clusters.\n"
        "Each cluster was formed by semantic embedding similarity — merchants "
        "landed together because their transaction descriptions are semantically related, "
        "NOT just because they share a category tag.\n\n"
        "For each cluster below, write an EXACTLY 2-word human-readable label that "
        "captures the specific spending pattern in a clear, professional, descriptive way, "
        "NOT the generic category name.\n\n"
        "STRICT RULES (violating any rule is wrong):\n"
        "1. Every label must be EXACTLY 2 words.\n"
        "2. All labels across the entire list must be UNIQUE — no two clusters may share "
        "   the same label or even the same first word.\n"
        "3. No label may be a plain category name or a synonym of one "
        "(e.g. 'Dining Out', 'Food Delivery', 'Transportation', 'Entertainment', 'Shopping' are banned).\n"
        "4. Labels must be specific and contextual — they should reflect the actual merchants/terms.\n"
        "5. Tone must be professional, neutral, and concise. Avoid whimsical, playful, emotional, or slang phrasing.\n\n"
        "Think about the functional spending pattern, not a lifestyle slogan.\n"
        "Great examples: 'Home Decor', 'Kitchen Supplies', 'Office Equipment', "
        "'Pet Care', 'Home Improvement', 'Personal Care', 'Coffee Purchases', 'Grocery Essentials'.\n"
        "Bad examples: 'Dining', 'Food Delivery', 'Transportation', 'Entertainment', "
        "'Shopping', 'Online Shopping', 'Coffee Ritual', 'Weekend Getaways' — too generic or too whimsical.\n\n"
        "You are labelling ALL clusters at once. Read ALL of them before writing any label "
        "so that you can ensure uniqueness across the full list.\n\n"
        "Return ONLY a JSON array of strings, one label per cluster, in the same order.\n"
        "No explanations, no markdown, just the JSON array.\n\n"
        f"Clusters:\n{json.dumps(payload, indent=2)}"
    )
    try:
        logger.warning("Gemini cluster labelling: sending %d clusters, payload=%s", len(clusters_meta), json.dumps(payload[:2]))
        text = _gemini_post(prompt, temperature=0.3)
        logger.warning("Gemini cluster labelling response: %s", text[:500])
        labels = json.loads(text)
        if isinstance(labels, list) and len(labels) == len(clusters_meta):
            result = [str(l).strip() or fallbacks[i] for i, l in enumerate(labels)]
            # Deduplicate: if any label appears more than once, append a
            # distinguishing merchant suffix to all but the first occurrence.
            seen: dict[str, int] = {}
            for i, lbl in enumerate(result):
                key = lbl.lower()
                if key in seen:
                    merchant = clusters_meta[i]["merchants"][0] if clusters_meta[i]["merchants"] else str(i + 1)
                    result[i] = f"{lbl} ({merchant[:12]})"
                else:
                    seen[key] = i
            _gemini_label_cache[cache_key] = result
            return result
        logger.warning("Gemini returned %d labels for %d clusters — falling back", len(labels) if isinstance(labels, list) else -1, len(clusters_meta))
    except Exception:
        logger.exception("Gemini cluster label generation failed, using fallbacks")
    return fallbacks


def _gemini_summarize_clusters(clusters: list[dict]) -> list[str]:
    """Generate a bullet-pointed spending summary from cluster labels and trends.

    Returns a list of bullet strings (without leading dashes).
    Falls back to a simple generated list if Gemini is unavailable.
    """
    def _fallback():
        bullets = []
        for c in clusters:
            label = c["label"]
            spend = f"${c['total_spend']:,.2f}"
            trend = c.get("trend")
            if trend and trend["direction"] == "up":
                bullets.append(f"{label}: {spend} spent, up {trend['percent_change']}% vs prior period")
            elif trend and trend["direction"] == "down":
                bullets.append(f"{label}: {spend} spent, down {abs(trend['percent_change'])}% vs prior period")
            elif trend and trend["direction"] == "new":
                bullets.append(f"{label}: {spend} spent — new this period")
            else:
                bullets.append(f"{label}: {spend} spent")
        return bullets

    if not GOOGLE_CLOUD_PROJECT:
        return _fallback()

    payload = []
    for c in clusters:
        entry = {
            "label": c["label"],
            "total_spend": c["total_spend"],
            "transaction_count": c["transaction_count"],
        }
        if c.get("trend"):
            entry["trend"] = c["trend"]
        payload.append(entry)

    prompt = (
        "You are a personal finance assistant. Given a user's spending clusters for a time period, "
        "write a concise, insightful bullet-point summary of their spending habits.\n\n"
        "Rules:\n"
        "- One bullet per cluster.\n"
        "- Each bullet should highlight the spend amount AND give a brief, human observation about the habit "
        "(e.g. whether it's growing, shrinking, or noteworthy).\n"
        "- If trend data is provided, mention whether spending went up or down.\n"
        "- Use a conversational, slightly casual tone — like a savvy friend reviewing your bank statement.\n"
        "- Keep each bullet under 20 words.\n"
        "- Do NOT include leading dashes or bullet characters — just the text.\n"
        "- Return ONLY a JSON array of strings, one per cluster, in the same order.\n\n"
        f"Clusters:\n{json.dumps(payload, indent=2)}"
    )
    try:
        text = _gemini_post(prompt, temperature=0.4)
        bullets = json.loads(text)
        if isinstance(bullets, list) and len(bullets) == len(clusters):
            return [str(b).strip() for b in bullets]
        logger.warning("Gemini summary returned wrong count — falling back")
    except Exception:
        logger.exception("Gemini cluster summary failed, using fallback")
    return _fallback()


def _cluster_top_terms(members: list) -> list:
    stopwords = {"the", "and", "of", "in", "at", "to", "a", "for", "co", "llc", "inc"}
    tokens = []
    for _, row in members:
        for field in (row["merchant_name"], row["spending_category"], row.get("description") or ""):
            tokens.extend(
                w.lower() for w in field.replace("_", " ").split()
                if len(w) > 2 and w.lower() not in stopwords
            )
    return [t for t, _ in Counter(tokens).most_common(6)]


def _build_cluster(cid: int, members: list, vectors, centroids) -> dict:
    centroid = centroids[cid]
    centroid_norm = centroid / (np.linalg.norm(centroid) + 1e-9)
    txn_list = []
    sims = []
    monthly: dict = defaultdict(float)

    for vec_idx, row in members:
        v = vectors[vec_idx]
        v_norm = v / (np.linalg.norm(v) + 1e-9)
        sim = float(np.dot(v_norm, centroid_norm))
        sims.append(sim)
        month_key = row["timestamp"].strftime("%Y-%m")
        monthly[month_key] += abs(float(row["amount"]))
        d = _serialize_transaction(row)
        d["intra_cluster_similarity"] = round(sim, 4)
        txn_list.append(d)

    txn_list.sort(key=lambda x: x["intra_cluster_similarity"], reverse=True)
    monthly_spend = [{"month": k, "spend": round(v, 2)} for k, v in sorted(monthly.items())]

    fallback_label, majority_category, top_merchants = _derive_cluster_label(members)
    top_terms = _cluster_top_terms(members)
    return {
        "cluster_id": cid,
        "label": fallback_label,          # overwritten by Gemini after all clusters built
        "majority_category": majority_category,
        "top_merchants": top_merchants,   # used by Gemini; not sent to frontend
        "top_terms": top_terms,
        "representative_transactions": txn_list[:3],
        "transaction_count": len(members),
        "total_spend": round(sum(abs(float(r["amount"])) for _, r in members), 2),
        "avg_similarity": round(float(np.mean(sims)), 4),
        "monthly_spend": monthly_spend,
        "transactions": txn_list,
    }


async def _fetch_transactions_in_window(conn, user_id: int, start_dt, end_dt):
    if start_dt and end_dt:
        return await conn.fetch(
            """SELECT transaction_id, timestamp, amount, merchant_name, spending_category,
                      quantity, country, item_description AS description, embedding
               FROM transactions_2
               WHERE user_id = $1 AND embedding IS NOT NULL
                 AND amount < 10000
                 AND timestamp >= $2 AND timestamp <= $3
               ORDER BY timestamp DESC""",
            user_id, start_dt, end_dt,
        )
    elif start_dt:
        return await conn.fetch(
            """SELECT transaction_id, timestamp, amount, merchant_name, spending_category,
                      quantity, country, item_description AS description, embedding
               FROM transactions_2
               WHERE user_id = $1 AND embedding IS NOT NULL
                 AND amount < 10000
                 AND timestamp >= $2
               ORDER BY timestamp DESC""",
            user_id, start_dt,
        )
    else:
        return await conn.fetch(
            """SELECT transaction_id, timestamp, amount, merchant_name, spending_category,
                      quantity, country, item_description AS description, embedding
               FROM transactions_2
               WHERE user_id = $1 AND embedding IS NOT NULL
                 AND amount < 10000
               ORDER BY timestamp DESC""",
            user_id,
        )


async def _run_kmeans(vectors, k: int):
    loop = asyncio.get_event_loop()
    km = await loop.run_in_executor(
        None,
        partial(KMeans(n_clusters=k, random_state=42, n_init=10, max_iter=300).fit, vectors),
    )
    return km.labels_, km.cluster_centers_


async def _count_user_transactions(user_id: int) -> int:
    async with pool.acquire() as conn:
        return await conn.fetchval(
            "SELECT COUNT(*) FROM transactions_2 WHERE user_id = $1",
            user_id,
        )


# --- General Routes ---
# Lightweight endpoints for health checks and example responses.

@app.get("/health")
def health_check() -> dict:
    return {"status": "ok"}


@app.get("/api/hello")
def hello(name: str = "world") -> dict:
    return {"message": f"Hello, {name}!"}


# --- Item Routes ---
# Simple CRUD endpoints for the generic items table used by the demo frontend.

@app.get("/items")
async def get_items():
    async with pool.acquire() as conn:
        rows = await conn.fetch("SELECT * FROM items ORDER BY created_at DESC")
        return [dict(r) for r in rows]

@app.get("/items/{item_id}")
async def get_item(item_id: int):
    async with pool.acquire() as conn:
        row = await conn.fetchrow("SELECT * FROM items WHERE id = $1", item_id)
        if not row:
            raise HTTPException(status_code=404, detail="Item not found")
        return dict(row)

@app.post("/items", status_code=201)
async def create_item(item: ItemCreate):
    async with pool.acquire() as conn:
        row = await conn.fetchrow(
            "INSERT INTO items (name, value) VALUES ($1, $2) RETURNING *",
            item.name, item.value
        )
        return dict(row)

@app.put("/items/{item_id}")
async def update_item(item_id: int, item: ItemUpdate):
    async with pool.acquire() as conn:
        row = await conn.fetchrow(
            """UPDATE items SET
               name = COALESCE($1, name),
               value = COALESCE($2, value)
               WHERE id = $3 RETURNING *""",
            item.name, item.value, item_id
        )
        if not row:
            raise HTTPException(status_code=404, detail="Item not found")
        return dict(row)

@app.delete("/items/{item_id}", status_code=204)
async def delete_item(item_id: int):
    async with pool.acquire() as conn:
        result = await conn.execute("DELETE FROM items WHERE id = $1", item_id)
        if result == "DELETE 0":
            raise HTTPException(status_code=404, detail="Item not found")


# --- Transaction Routes ---
# These endpoints are scoped to the authenticated user and operate only on
# that user's transaction rows.

@app.post("/api/transactions", status_code=201)
async def create_transaction(txn: TransactionCreate, user: dict = Depends(get_current_user)):
    txn_id = uuid.uuid4()
    ts = datetime.now(timezone.utc)
    item_description = txn.item_description or txn.merchant_name
    embed_text = " ".join(filter(None, [txn.merchant_name, txn.spending_category, item_description]))
    async with pool.acquire() as conn:
        row = await conn.fetchrow(
            """INSERT INTO transactions_2 (
                transaction_id, user_id, timestamp, amount, merchant_name,
                spending_category, item_description, quantity, country
            ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
            RETURNING transaction_id, user_id, timestamp, amount, merchant_name,
                      spending_category, quantity, country, item_description AS description""",
            txn_id, int(user["user_id"]), ts, txn.amount, txn.merchant_name,
            txn.spending_category, item_description, txn.quantity, txn.country,
        )
        try:
            vec = _embed_text(embed_text)
            vec_str = f"[{','.join(str(x) for x in vec)}]"
            await conn.execute(
                "UPDATE transactions_2 SET embedding = $1::vector WHERE transaction_id = $2",
                vec_str, txn_id,
            )
        except Exception:
            logger.exception("Failed to generate embedding for transaction %s", txn_id)
        return _serialize_transaction(row)


@app.get("/api/transactions/categories")
async def fetch_categories(user: dict = Depends(get_current_user)):
    async with pool.acquire() as conn:
        rows = await conn.fetch(
            """SELECT DISTINCT spending_category
               FROM transactions_2
               WHERE user_id = $1
               ORDER BY spending_category""",
            int(user["user_id"]),
        )
    return [r["spending_category"] for r in rows]


@app.get("/api/transactions/category/{spending_category}")
async def fetch_transactions_by_category(spending_category: str, user: dict = Depends(get_current_user)):
    async with pool.acquire() as conn:
        rows = await conn.fetch(
            """SELECT transaction_id, user_id, timestamp, amount, merchant_name, spending_category,
                      quantity, country, item_description AS description
               FROM transactions_2
               WHERE user_id = $1 AND LOWER(spending_category) = LOWER($2)
                 AND amount < 10000
               ORDER BY timestamp DESC""",
            int(user["user_id"]), spending_category,
        )
        return [_serialize_transaction(r) for r in rows]


@app.get("/api/transactions/current-month")
async def fetch_current_month(user: dict = Depends(get_current_user)):
    now = datetime.now(timezone.utc)
    month_start = now.replace(day=1, hour=0, minute=0, second=0, microsecond=0)
    async with pool.acquire() as conn:
        rows = await conn.fetch(
            """SELECT transaction_id, user_id, timestamp, amount, merchant_name, spending_category,
                      quantity, country, item_description AS description
               FROM transactions_2
               WHERE user_id = $1 AND timestamp >= $2
                 AND amount < 10000
               ORDER BY timestamp DESC""",
            int(user["user_id"]), month_start,
        )
        return [_serialize_transaction(r) for r in rows]


@app.get("/api/transactions/previous-month")
async def fetch_previous_month(user: dict = Depends(get_current_user)):
    now = datetime.now(timezone.utc)
    current_month_start = now.replace(day=1, hour=0, minute=0, second=0, microsecond=0)
    if current_month_start.month == 1:
        prev_month_start = current_month_start.replace(year=current_month_start.year - 1, month=12)
    else:
        prev_month_start = current_month_start.replace(month=current_month_start.month - 1)
    async with pool.acquire() as conn:
        rows = await conn.fetch(
            """SELECT transaction_id, user_id, timestamp, amount, merchant_name, spending_category,
                      quantity, country, item_description AS description
               FROM transactions_2
               WHERE user_id = $1 AND timestamp >= $2 AND timestamp < $3
                 AND amount < 10000
               ORDER BY timestamp DESC""",
            int(user["user_id"]), prev_month_start, current_month_start,
        )
        return [_serialize_transaction(r) for r in rows]


@app.get("/api/transactions/recent")
async def fetch_n_transactions(limit: int = Query(default=10, ge=1, le=500), user: dict = Depends(get_current_user)):
    async with pool.acquire() as conn:
        rows = await conn.fetch(
            """SELECT transaction_id, user_id, timestamp, amount, merchant_name, spending_category,
                      quantity, country, item_description AS description
               FROM transactions_2
               WHERE user_id = $1
                 AND amount < 10000
               ORDER BY timestamp DESC
               LIMIT $2""",
            int(user["user_id"]), limit,
        )
        return [_serialize_transaction(r) for r in rows]


@app.get("/api/transactions/search")
async def search_transactions(
    q: str = Query(..., min_length=1),
    limit: int = Query(default=50, ge=1, le=200),
    min_similarity: float = Query(default=0.55, ge=0.0, le=1.0),
    user: dict = Depends(get_current_user),
):
    """Semantic similarity search over the authenticated user's transactions."""
    try:
        vec = _embed_text(q.strip())
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"Embedding query failed: {e}")
    vec_str = f"[{','.join(str(x) for x in vec)}]"
    async with pool.acquire() as conn:
        try:
            rows = await conn.fetch(
                """SELECT transaction_id, timestamp, amount, merchant_name,
                          spending_category, quantity, country, item_description AS description,
                          1 - (embedding <=> $1::vector) AS similarity
                   FROM transactions_2
                   WHERE user_id = $2 AND embedding IS NOT NULL
                     AND amount < 10000
                   ORDER BY similarity DESC
                   LIMIT $3""",
                vec_str, int(user["user_id"]), limit,
            )
        except Exception as e:
            raise HTTPException(status_code=500, detail=f"Semantic search failed: {e}") from e
    results = []
    for r in rows:
        d = dict(r)
        d["transaction_id"] = str(d["transaction_id"])
        d["timestamp"] = d["timestamp"].isoformat()
        d["amount"] = float(d["amount"])
        d["similarity"] = float(d["similarity"])
        results.append(d)
    # Filter by similarity threshold, but always return at least 5 results
    filtered = [r for r in results if r["similarity"] >= min_similarity]
    if len(filtered) < 5:
        filtered = results[:max(5, len(filtered))]
    return filtered
@app.get("/api/transactions/clusters")
async def cluster_transactions(
    k: int = Query(default=5, ge=3, le=10),
    time_range: str = Query(default=None),
    start_date: str = Query(default=None),
    end_date: str = Query(default=None),
    compare: bool = Query(default=False),
    user: dict = Depends(get_current_user),
):
    user_id = int(user["user_id"])
    start_dt, end_dt, tr_label = _resolve_time_range(time_range, start_date, end_date)

    async with pool.acquire() as conn:
        rows = await _fetch_transactions_in_window(conn, user_id, start_dt, end_dt)

    total_all = await _count_user_transactions(user_id)

    if not rows:
        raise HTTPException(400, "No transactions with embeddings found in the selected time range.")
    n = len(rows)
    if k > n:
        raise HTTPException(400, f"k ({k}) exceeds transactions with embeddings ({n}). Use a smaller k.")

    vectors = np.array(
        [[float(x) for x in row["embedding"].strip("[]").split(",")] for row in rows],
        dtype=np.float32,
    )
    labels, centroids = await _run_kmeans(vectors, k)

    clusters_raw = [[] for _ in range(k)]
    for i, row in enumerate(rows):
        clusters_raw[labels[i]].append((i, row))

    current_clusters = []
    for cid in range(k):
        if not clusters_raw[cid]:
            continue
        current_clusters.append(_build_cluster(cid, clusters_raw[cid], vectors, centroids))
    current_clusters.sort(key=lambda c: c["total_spend"], reverse=True)

    previous_clusters = None
    if compare and start_dt:
        prior_start, prior_end = _prior_period(start_dt, end_dt)
        async with pool.acquire() as conn:
            prior_rows = await _fetch_transactions_in_window(conn, user_id, prior_start, prior_end)
        if len(prior_rows) >= k:
            prior_vectors = np.array(
                [[float(x) for x in r["embedding"].strip("[]").split(",")] for r in prior_rows],
                dtype=np.float32,
            )
            prior_labels, prior_centroids = await _run_kmeans(prior_vectors, k)
            prior_raw = [[] for _ in range(k)]
            for i, row in enumerate(prior_rows):
                prior_raw[prior_labels[i]].append((i, row))
            previous_clusters = []
            for cid in range(k):
                if not prior_raw[cid]:
                    continue
                previous_clusters.append(_build_cluster(cid, prior_raw[cid], prior_vectors, prior_centroids))
            previous_clusters.sort(key=lambda c: c["total_spend"], reverse=True)

            prev_by_label = {c["majority_category"]: c for c in previous_clusters}
            for c in current_clusters:
                prev = prev_by_label.get(c["majority_category"])
                if prev:
                    delta = c["total_spend"] - prev["total_spend"]
                    pct = (delta / prev["total_spend"] * 100) if prev["total_spend"] else 0.0
                    c["trend"] = {
                        "direction": "up" if pct > 2 else ("down" if pct < -2 else "flat"),
                        "percent_change": round(pct, 1),
                        "prev_spend": prev["total_spend"],
                    }
                else:
                    c["trend"] = {"direction": "new", "percent_change": None, "prev_spend": None}

    # Generate semantic labels via Gemini for all current clusters in one call.
    # Run in executor so the blocking requests.post doesn't stall the event loop.
    all_clusters_for_labelling = current_clusters + (previous_clusters or [])
    clusters_meta = [
        {"merchants": c["top_merchants"], "terms": c["top_terms"], "majority_category": c["majority_category"]}
        for c in all_clusters_for_labelling
    ]
    loop = asyncio.get_event_loop()
    gemini_labels = await loop.run_in_executor(None, _gemini_label_clusters, clusters_meta)
    for i, c in enumerate(all_clusters_for_labelling):
        c["label"] = gemini_labels[i]
        c.pop("top_merchants", None)  # internal field, not needed by frontend

    summary_bullets = await loop.run_in_executor(None, _gemini_summarize_clusters, current_clusters)

    return {
        "k": k,
        "time_range": tr_label,
        "start_date": start_dt.isoformat() if start_dt else None,
        "end_date": end_dt.isoformat() if end_dt else None,
        "total_transactions": total_all,
        "transactions_with_embeddings": n,
        "summary": summary_bullets,
        "clusters": current_clusters,
        "previous_clusters": previous_clusters,
    }


@app.get("/api/transactions/semantic-filter")
async def semantic_filter_transactions(
    category: str = Query(..., min_length=1),
    threshold: float = Query(default=0.75, ge=0.5, le=0.95),
    limit: int = Query(default=50, ge=1, le=200),
    time_range: str = Query(default=None),
    start_date: str = Query(default=None),
    end_date: str = Query(default=None),
    user: dict = Depends(get_current_user),
):
    user_id = int(user["user_id"])
    concept = category.strip()
    start_dt, end_dt, tr_label = _resolve_time_range(time_range, start_date, end_date)

    try:
        vec = _embed_text(concept)
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"Embedding query failed: {e}")
    vec_str = f"[{','.join(str(x) for x in vec)}]"

    time_clause = ""
    time_params: list = []
    if start_dt and end_dt:
        time_clause = "AND t.timestamp >= $4 AND t.timestamp <= $5"
        time_params = [start_dt, end_dt]
    elif start_dt:
        time_clause = "AND t.timestamp >= $4"
        time_params = [start_dt]

    base_params = [user_id, vec_str, threshold] + time_params + [limit]
    limit_pos = f"${len(base_params)}"

    async with pool.acquire() as conn:
        try:
            rows = await conn.fetch(
                f"""SELECT transaction_id, timestamp, amount, merchant_name, spending_category,
                           quantity, country, item_description AS description,
                           1 - (embedding <=> $2::vector) AS similarity,
                           COUNT(*) OVER () AS total_matched
                    FROM transactions_2 t
                    WHERE t.user_id = $1
                      AND t.embedding IS NOT NULL
                      AND t.amount < 10000
                      AND 1 - (t.embedding <=> $2::vector) >= $3
                      {time_clause}
                    ORDER BY similarity DESC
                    LIMIT {limit_pos}""",
                *base_params,
            )
        except Exception as e:
            raise HTTPException(status_code=500, detail=f"Semantic filter failed: {e}")

    total_matched = int(rows[0]["total_matched"]) if rows else 0
    results = []
    for r in rows:
        d = _serialize_transaction(r)
        d["similarity"] = round(float(r["similarity"]), 4)
        results.append(d)

    concept_words = set(concept.lower().split())
    cat_counts: Counter = Counter()
    for r in rows:
        val = r["spending_category"].replace("_", " ").lower()
        if not any(w in val for w in concept_words):
            cat_counts[val] += 1
    suggested_concepts = [k for k, _ in cat_counts.most_common(5) if k]

    return {
        "concept": concept,
        "threshold": threshold,
        "time_range": tr_label,
        "start_date": start_dt.isoformat() if start_dt else None,
        "end_date": end_dt.isoformat() if end_dt else None,
        "total_matched": total_matched,
        "transactions": results,
        "suggested_concepts": suggested_concepts,
    }


@app.get("/api/transactions/all")
async def fetch_all_transactions(user: dict = Depends(get_current_user)):
    async with pool.acquire() as conn:
        rows = await conn.fetch(
            """SELECT transaction_id, user_id, timestamp, amount, merchant_name, spending_category,
                      quantity, country, item_description AS description
               FROM transactions_2
               WHERE user_id = $1
                 AND amount < 10000
               ORDER BY timestamp DESC""",
            int(user["user_id"]),
        )
        return [_serialize_transaction(r) for r in rows]


# --- Budget Routes ---
# Budget operations let the user create or update spending limits by category.

@app.post("/api/budgets", status_code=200)
async def create_budget(budget: BudgetCreate):
    """
    Set (or update) a monthly spending limit for a category.

    - category: one of the SpendingCategory enum values
    - amount: spending limit in USD; must be a positive number

    Returns {"status": 0} on success.
    If the same category already has a budget, the amount is overwritten (upsert).
    """
    # Reject non-positive amounts — a budget of $0 or negative makes no sense
    if budget.amount <= 0:
        raise HTTPException(status_code=422, detail="Amount must be greater than 0")

    try:
        async with pool.acquire() as conn:
            # INSERT or UPDATE: category is the primary key, so duplicate calls
            # for the same category simply update the stored amount instead of erroring
            await conn.execute(
                """INSERT INTO "BudgetPrefs" (category, amount)
                   VALUES ($1, $2)
                   ON CONFLICT (category) DO UPDATE SET amount = EXCLUDED.amount""",
                budget.category.value,
                budget.amount,
            )
        return {"status": 0}
    except Exception as e:
        # Log the full error server-side; return a generic failure code to the caller
        print(f"[ERROR] create_budget: {e}")
        return JSONResponse(status_code=500, content={"status": 1, "error": str(e)})


# --- NL2SQL Models ---

class NL2SQLGenerateRequest(BaseModel):
    question: str

class NL2SQLExecuteRequest(BaseModel):
    sql: str

class AIAnalysisNL2SQLRequest(BaseModel):
    query: str


# --- NL2SQL Routes ---

async def _generate_sql_from_question(question: str) -> str:
    stripped_question = question.strip()
    if not stripped_question:
        raise HTTPException(status_code=422, detail="question must not be empty")
    async with pool.acquire() as conn:
        try:
            row = await conn.fetchrow(
                "SELECT alloydb_ai_nl.get_sql($1::text, $2::text) ->> 'sql' AS sql",
                "transactions_2_config",
                stripped_question,
            )
        except Exception as e:
            raise HTTPException(status_code=500, detail=f"NL2SQL generation failed: {e}")
    sql = row["sql"] if row else None
    if not sql:
        raise HTTPException(status_code=422, detail="NL2SQL could not generate a query for this question")
    return sql


def _add_finance_domain_context(question: str) -> str:
    return (
        f"{question}. "
        "Use only these spending_category values when filtering: shopping, gifts, household, office, clothing, "
        "hobbies, food, beauty, toys, stationery. "
        "If the user asks about groceries or dining, treat it as food."
    )


def _rewrite_common_category_aliases(sql: str) -> str:
    return sql


SPENDING_CONTEXT_CATEGORIES = [
    "shopping",
    "gifts",
    "household",
    "office",
    "clothing",
    "hobbies",
    "food",
    "beauty",
    "toys",
    "stationery",
]


def _extract_context_categories(question: str, sql: str) -> list[str]:
    haystack = f"{question} {sql}".lower()
    return [category for category in SPENDING_CONTEXT_CATEGORIES if category in haystack]



async def _fetch_supporting_transaction_context(user_id: str, question: str, sql: str, limit: int = 12) -> list[dict]:
    categories = _extract_context_categories(question, sql)
    category_clause = ""
    params: list = [int(user_id), limit]

    if categories:
        category_clause = "AND spending_category = ANY($3::text[])"
        params.append(categories)

    async with pool.acquire() as conn:
        rows = await conn.fetch(
            f"""
            SELECT
                timestamp,
                amount,
                merchant_name,
                spending_category,
                quantity,
                country,
                item_description
            FROM transactions_2
            WHERE user_id = $1
              AND amount < 10000
              {category_clause}
            ORDER BY ABS(amount) DESC, timestamp DESC
            LIMIT $2
            """,
            *params,
        )

    return [
        {
            "timestamp": row["timestamp"].isoformat(),
            "amount": float(row["amount"]),
            "merchant_name": row["merchant_name"],
            "spending_category": row["spending_category"],
            "quantity": row["quantity"],
            "country": row["country"],
            "description": row["item_description"],
        }
        for row in rows
    ]


@app.post("/api/nl2sql/generate")
async def nl2sql_generate(req: NL2SQLGenerateRequest, user: dict = Depends(get_current_user)):
    """Translate a natural language question to SQL using AlloyDB AI NL2SQL."""
    t0 = time.perf_counter()
    question = req.question.strip()
    scoped_question = _add_finance_domain_context(f"{question} for user: {user['user_id']}")
    sql = await _generate_sql_from_question(scoped_question)
    sql = _rewrite_common_category_aliases(sql)
    trans_ms = monotonic_ms(t0)
    e2e = monotonic_ms(t0)
    await metrics_store.append(
        {
            "tool": "nl2sql",
            "nl2sql_translation_ms": trans_ms,
            "nl2sql_total_ms": trans_ms,
            "e2e_total_ms": e2e,
        }
    )
    return {"question": req.question, "sql": sql}


ROW_LIMIT = 200

def _scope_transactions_sql(sql: str, user_id: str) -> str:
    safe_user_id = int(user_id)
    normalized_sql = re.sub(r"\bpublic\.transactions_2\b", "transactions_2", sql, flags=re.IGNORECASE)
    normalized_sql = re.sub(r"\bpublic\.transactions\b", "transactions_2", normalized_sql, flags=re.IGNORECASE)
    if re.search(r"^\s*WITH\s+transactions_2\s+AS\s*\(", normalized_sql, flags=re.IGNORECASE):
        raise HTTPException(status_code=422, detail="Generated query could not be safely scoped")
    scoped_transactions_cte = (
        "WITH transactions_2 AS ("
        f"SELECT * FROM public.transactions_2 WHERE user_id = {safe_user_id} AND amount < 10000"
        ")"
    )
    normalized_sql = re.sub(r"\btransactions\b", "transactions_2", normalized_sql, flags=re.IGNORECASE)
    if normalized_sql.lstrip().upper().startswith("WITH "):
        return re.sub(r"^\s*WITH\s+", f"{scoped_transactions_cte}, ", normalized_sql, count=1, flags=re.IGNORECASE)
    return f"{scoped_transactions_cte} {normalized_sql}"


async def _execute_select_sql(sql_query: str, user_id: str | None = None) -> dict:
    sql = _rewrite_common_category_aliases(sql_query.strip().rstrip(";"))
    if not sql.upper().startswith("SELECT"):
        raise HTTPException(status_code=422, detail="Only SELECT statements are allowed")
    executable_sql = _scope_transactions_sql(sql, user_id) if user_id is not None else sql
    async with pool.acquire() as conn:
        try:
            rows = await conn.fetch(f"SELECT * FROM ({executable_sql}) _q LIMIT {ROW_LIMIT + 1}")
        except Exception as e:
            logger.exception("Scoped SQL query failed")
            raise HTTPException(status_code=400, detail="Query failed")
    if not rows:
        return {"columns": [], "rows": [], "truncated": False}
    truncated = len(rows) > ROW_LIMIT
    rows = rows[:ROW_LIMIT]
    columns = list(rows[0].keys())
    result_rows = [
        [str(v) if not isinstance(v, (int, float, bool, type(None))) else v for v in row.values()]
        for row in rows
    ]
    return {"columns": columns, "rows": result_rows, "truncated": truncated}


def _pgvector_literal_from_embedding_text(emb: str) -> str:
    """AlloyDB/google_ml.embedding cast to text often returns Postgres array syntax '{a,b,...}'.
    pgvector's vector input expects bracket form '[a,b,...]' (see InvalidTextRepresentation).
    """
    s = emb.strip()
    if len(s) >= 2 and s[0] == "{" and s[-1] == "}":
        return "[" + s[1:-1] + "]"
    return s


async def _semantic_search_core(
    conn: asyncpg.Connection,
    query_text: str,
    user_id: int,
    limit: int,
) -> tuple[list[asyncpg.Record], float, float]:
    """Two-step embedding + vector search (text round-trip for asyncpg binding)."""
    t_emb = time.perf_counter()
    emb_row = await conn.fetchrow(
        "SELECT (google_ml.embedding('text-embedding-005', $1::text))::text AS emb",
        query_text,
    )
    emb_ms = monotonic_ms(t_emb)
    if emb_row is None or emb_row["emb"] is None:
        raise ValueError("embedding returned no vector text")
    emb_text = _pgvector_literal_from_embedding_text(emb_row["emb"])
    t_vec = time.perf_counter()
    rows = await conn.fetch(
        """SELECT *, item_description AS description,
               1 - (embedding <=> $1::text::vector) AS similarity
           FROM transactions_2
           WHERE user_id = $2
             AND embedding IS NOT NULL
           ORDER BY embedding <=> $1::text::vector
           LIMIT $3""",
        emb_text,
        user_id,
        limit,
    )
    vec_ms = monotonic_ms(t_vec)
    return rows, emb_ms, vec_ms


# --- Customer tool router (Gemini classifies which app tab / tool to use) ---


class CustomerRouteRequest(BaseModel):
    message: str = Field(..., min_length=1, max_length=4000)


class CustomerRouteResponse(BaseModel):
    tool: str
    reasoning: Optional[str] = None


class CustomerAskResponse(BaseModel):
    tool: str
    reasoning: Optional[str] = None
    result: dict[str, Any] = Field(default_factory=dict)


@app.post("/api/customer/route-tool", response_model=CustomerRouteResponse)
async def customer_route_tool(
    req: CustomerRouteRequest,
    user: dict = Depends(get_current_user),
):
    """
    v1: Given a natural-language message, return which product tool best fits.
    Does not execute NL2SQL, search, etc. — only classification.
    """
    logger.debug("customer route-tool for user_id=%s", user.get("user_id"))
    if not GOOGLE_CLOUD_PROJECT:
        raise HTTPException(
            status_code=503,
            detail="GOOGLE_CLOUD_PROJECT is not configured",
        )
    t_e2e = time.perf_counter()
    try:
        t_route = time.perf_counter()
        out = route_user_message(
            req.message.strip(),
            project_id=GOOGLE_CLOUD_PROJECT,
            location=GOOGLE_CLOUD_LOCATION,
            model=GEMINI_MODEL,
        )
        mcp_ms = monotonic_ms(t_route)
    except CustomerToolRouterError as e:
        raise HTTPException(status_code=502, detail=str(e)) from e
    tool = out["tool"]
    if tool not in ROUTER_TOOLS:
        tool = GENERAL
    e2e = monotonic_ms(t_e2e)
    await metrics_store.append(
        {
            "tool": "routing",
            "mcp_routing_ms": mcp_ms,
            "e2e_total_ms": e2e,
        }
    )
    return CustomerRouteResponse(tool=tool, reasoning=out.get("reasoning"))


@app.post("/api/customer/ask", response_model=CustomerAskResponse)
async def customer_ask(
    req: CustomerRouteRequest,
    user: dict = Depends(get_current_user),
):
    """Route the message and execute the corresponding tool for a single-turn response."""
    logger.debug("customer ask for user_id=%s", user.get("user_id"))
    if not GOOGLE_CLOUD_PROJECT:
        raise HTTPException(
            status_code=503,
            detail="GOOGLE_CLOUD_PROJECT is not configured",
        )
    t_e2e = time.perf_counter()
    try:
        t_route = time.perf_counter()
        out = route_user_message(
            req.message.strip(),
            project_id=GOOGLE_CLOUD_PROJECT,
            location=GOOGLE_CLOUD_LOCATION,
            model=GEMINI_MODEL,
        )
        mcp_ms = monotonic_ms(t_route)
    except CustomerToolRouterError as e:
        raise HTTPException(status_code=502, detail=str(e)) from e

    tool = out["tool"]
    if tool not in ROUTER_TOOLS:
        tool = GENERAL

    user_id = int(user["user_id"])
    message = req.message.strip()

    if tool == NL2SQL:
        scoped_question = _add_finance_domain_context(f"{message} for user: {user_id}")
        try:
            t_tr = time.perf_counter()
            sql = await _generate_sql_from_question(scoped_question)
            sql = _rewrite_common_category_aliases(sql)
            trans_ms = monotonic_ms(t_tr)
            t_ex = time.perf_counter()
            executed = await _execute_select_sql(sql, user_id=user["user_id"])
            ex_ms = monotonic_ms(t_ex)
        except HTTPException:
            raise
        except Exception as e:
            raise HTTPException(status_code=500, detail=str(e)) from e
        row_count = len(executed.get("rows") or [])
        e2e = monotonic_ms(t_e2e)
        await metrics_store.append(
            {
                "tool": "e2e",
                "mcp_routing_ms": mcp_ms,
                "nl2sql_translation_ms": trans_ms,
                "nl2sql_execution_ms": ex_ms,
                "nl2sql_total_ms": trans_ms + ex_ms,
                "nl2sql_row_count": row_count,
                "nl2sql_truncated": executed["truncated"],
                "e2e_total_ms": e2e,
            }
        )
        return CustomerAskResponse(
            tool=tool,
            reasoning=out.get("reasoning"),
            result={
                "question": message,
                "sql": sql,
                "columns": executed["columns"],
                "rows": executed["rows"],
                "truncated": executed["truncated"],
            },
        )

    if tool == SEMANTIC_SEARCH:
        async with pool.acquire() as conn:
            try:
                rows, emb_ms, vec_ms = await _semantic_search_core(conn, message, user_id, 10)
            except Exception as e:
                raise HTTPException(status_code=500, detail=f"Semantic search failed: {e}") from e
        results = []
        for r in rows:
            d = _serialize_transaction(r)
            d["similarity"] = float(r["similarity"])
            results.append(d)
        e2e = monotonic_ms(t_e2e)
        await metrics_store.append(
            {
                "tool": "e2e",
                "mcp_routing_ms": mcp_ms,
                "semantic_embedding_ms": emb_ms,
                "semantic_vector_search_ms": vec_ms,
                "semantic_total_ms": emb_ms + vec_ms,
                "semantic_match_count": len(results),
                "e2e_total_ms": e2e,
            }
        )
        return CustomerAskResponse(
            tool=tool,
            reasoning=out.get("reasoning"),
            result={"query": message, "matches": results},
        )

    if tool == MONTHLY_REPORTS:
        report, gem_ms, gem_bytes = await _generate_monthly_report_payload(
            MonthlyReportGenerateRequest(year_month=None), user
        )
        e2e = monotonic_ms(t_e2e)
        rec: dict[str, Any] = {
            "tool": "e2e",
            "mcp_routing_ms": mcp_ms,
            "e2e_total_ms": e2e,
        }
        if gem_ms is not None:
            rec["gemini_call_ms"] = gem_ms
        if gem_bytes is not None:
            rec["gemini_response_bytes"] = gem_bytes
        await metrics_store.append(rec)
        return CustomerAskResponse(
            tool=tool,
            reasoning=out.get("reasoning"),
            result=report,
        )

    if tool == CLUSTERING:
        e2e = monotonic_ms(t_e2e)
        await metrics_store.append(
            {
                "tool": "e2e",
                "mcp_routing_ms": mcp_ms,
                "e2e_total_ms": e2e,
            }
        )
        return CustomerAskResponse(
            tool=tool,
            reasoning=out.get("reasoning"),
            result={
                "status": "not_implemented",
                "message": "Clustering is not wired yet. Please use the dedicated tab when available.",
            },
        )

    e2e = monotonic_ms(t_e2e)
    await metrics_store.append(
        {
            "tool": "e2e",
            "mcp_routing_ms": mcp_ms,
            "e2e_total_ms": e2e,
        }
    )
    return CustomerAskResponse(
        tool=GENERAL,
        reasoning=out.get("reasoning"),
        result={
            "message": "I can route requests to NL2SQL, semantic search, or monthly reports. Try asking about spending insights."
        },
    )


@app.post("/api/nl2sql/execute")
async def nl2sql_execute(req: NL2SQLExecuteRequest, user: dict = Depends(get_current_user)):
    """Execute a SQL query (SELECT only) and return columns + rows (capped at ROW_LIMIT)."""
    t0 = time.perf_counter()
    out = await _execute_select_sql(req.sql, user_id=user["user_id"])
    exec_ms = monotonic_ms(t0)
    e2e = monotonic_ms(t0)
    row_count = len(out.get("rows") or [])
    await metrics_store.append(
        {
            "tool": "nl2sql",
            "nl2sql_execution_ms": exec_ms,
            "nl2sql_total_ms": exec_ms,
            "nl2sql_row_count": row_count,
            "nl2sql_truncated": out["truncated"],
            "e2e_total_ms": e2e,
        }
    )
    return out


@app.post("/api/ai-analysis/nl2sql")
async def ai_analysis_nl2sql(req: AIAnalysisNL2SQLRequest, user: dict = Depends(get_current_user)):
    """Run NL2SQL, execute the query, then ask Gemini to explain the returned data."""
    user_question = req.query.strip()
    if not user_question:
        raise HTTPException(status_code=422, detail="query must not be empty")

    scoped_question = _add_finance_domain_context(f"{user_question} for user: {user['user_id']}")
    sql = await _generate_sql_from_question(scoped_question)
    sql = _rewrite_common_category_aliases(sql)
    data = await _execute_select_sql(sql, user_id=user["user_id"])
    supporting_context = await _fetch_supporting_transaction_context(
        user_id=user["user_id"],
        question=user_question,
        sql=sql,
    )

    try:
        insight = await run_in_threadpool(
            generate_finance_insight_with_gemini,
            user_question=user_question,
            sql_query=sql,
            columns=data["columns"],
            rows=data["rows"],
            supporting_context=supporting_context,
        )
    except Exception:
        logger.exception("Gemini insight generation failed")
        insight = generate_fallback_finance_insight(
            user_question=user_question,
            columns=data["columns"],
            rows=data["rows"],
            supporting_context=supporting_context,
        )

    return {
        "success": True,
        "method": "nl2sql",
        "question": user_question,
        "insight": insight,
        "answer": insight,
        "sql": sql,
        "columns": data["columns"],
        "rows": data["rows"],
        "data": data["rows"],
        "supporting_context": supporting_context,
        "truncated": data["truncated"],
    }

@app.get("/api/budgets/usage")
async def fetch_budget_usage():
    """
    Return spending vs. budget limit for every category the user has configured.

    Spending is summed from the transactions table for the current calendar month.
    Only categories present in BudgetPrefs are included in the response.

    Response shape: [{"category": str, "total_spent": float, "budget_limit": float}, ...]
    Example:        [{"category": "dining", "total_spent": 120.0, "budget_limit": 100.0}]
    """
    # Compute the start of the current calendar month (UTC), same as current-month endpoint
    now = datetime.now(timezone.utc)
    month_start = now.replace(day=1, hour=0, minute=0, second=0, microsecond=0)

    async with pool.acquire() as conn:
        rows = await conn.fetch(
            """
            SELECT
                bp.category,
                -- Sum all transaction amounts for this category this month.
                -- ABS() because debit amounts are stored as negatives; we want a positive spend figure.
                -- COALESCE returns 0 when there are no matching transactions yet.
                COALESCE(SUM(ABS(t.amount)), 0) AS total_spent,
                bp.amount                        AS budget_limit
            FROM "BudgetPrefs" bp
            LEFT JOIN transactions_2 t
                ON LOWER(t.spending_category) = LOWER(bp.category)
               AND t.timestamp >= $1
            GROUP BY bp.category, bp.amount
            ORDER BY bp.category
            """,
            month_start,
        )

    # Convert NUMERIC fields to float for JSON serialisation
    return [
        {
            "category":     row["category"],
            "total_spent":  float(row["total_spent"]),
            "budget_limit": float(row["budget_limit"]),
        }
        for row in rows
    ]


# --- Monthly Report Models ---

class MonthlyReportGenerateRequest(BaseModel):
    year_month: Optional[str] = None


def _parse_year_month(value: Optional[str]) -> str:
    if not value:
        now = datetime.now(timezone.utc)
        return f"{now.year:04d}-{now.month:02d}"
    try:
        parsed = datetime.strptime(value, "%Y-%m")
    except ValueError as exc:
        raise HTTPException(status_code=422, detail="year_month must be in YYYY-MM format") from exc
    return f"{parsed.year:04d}-{parsed.month:02d}"


def _month_window(year_month: str) -> tuple[datetime, datetime]:
    start = datetime.strptime(year_month, "%Y-%m").replace(tzinfo=timezone.utc)
    if start.month == 12:
        end = start.replace(year=start.year + 1, month=1)
    else:
        end = start.replace(month=start.month + 1)
    return start, end


def _build_monthly_fallback(total_spent: float, category_rows: list[dict], merchant_rows: list[dict]) -> tuple[str, str, list[dict], list[str]]:
    if total_spent == 0:
        return (
            "No spending transactions were found for this month yet.",
            "No spending data available for this month.",
            [
                {"title": "Add transactions", "rationale": "Add your monthly bills and recurring expenses to get a richer report.", "estimated_savings_usd": 0, "difficulty": "Easy", "impact": "High"},
                {"title": "Set budgets", "rationale": "Set category budgets so next month's report can include over/under analysis.", "estimated_savings_usd": 0, "difficulty": "Easy", "impact": "Medium"},
            ],
            [],
        )
    top_category = category_rows[0]["category"] if category_rows else "miscellaneous"
    top_cat_spend = category_rows[0]["total_spent"] if category_rows else 0
    top_merchant = merchant_rows[0]["merchant_name"] if merchant_rows else "your top merchant"
    comments = (
        f"You spent ${total_spent:,.2f} this month. "
        f"Your highest category was {top_category} at ${top_cat_spend:,.2f}, and your largest merchant was {top_merchant}. "
        "Keep tracking category-level trends to spot changes earlier."
    )
    headline = f"{top_category.title()} was your biggest spend at ${top_cat_spend:,.2f} this month."
    suggestions = [
        {"title": f"Cap {top_category} spending", "rationale": f"Set a tighter limit for {top_category} and check progress mid-month to avoid overspending.", "estimated_savings_usd": round(top_cat_spend * 0.15, 2), "difficulty": "Medium", "impact": "High"},
        {"title": "Audit recurring charges", "rationale": f"Review your top merchants including {top_merchant} and identify any subscriptions you no longer use.", "estimated_savings_usd": 20.0, "difficulty": "Easy", "impact": "Medium"},
        {"title": "Weekly savings transfer", "rationale": "Move one discretionary purchase equivalent per week into savings to build a monthly cushion.", "estimated_savings_usd": 50.0, "difficulty": "Easy", "impact": "Medium"},
    ]
    watch_items = [
        f"{top_category.title()} accounted for ${top_cat_spend:,.2f} — your largest category this month.",
    ]
    if merchant_rows:
        watch_items.append(f"Top merchant '{top_merchant}' saw ${merchant_rows[0]['total_spent']:,.2f} in charges.")
    return comments, headline, suggestions, watch_items


def _generate_monthly_llm_comments(
    year_month: str,
    total_spent: float,
    category_rows: list[dict],
    merchant_rows: list[dict],
) -> tuple[str, list[str], Optional[float], Optional[int]]:
    fallback_comments, fallback_headline, fallback_suggestions, fallback_watch = _build_monthly_fallback(total_spent, category_rows, merchant_rows)
    if not GOOGLE_CLOUD_PROJECT:
        return fallback_comments, fallback_headline, fallback_suggestions, fallback_watch, None, None

    try:
        t_llm = time.perf_counter()
        comments, suggestions, gem_bytes = generate_monthly_report_comments_with_gemini(
            year_month=year_month,
            total_spent=total_spent,
            category_rows=category_rows,
            merchant_rows=merchant_rows,
        )
        gem_ms = monotonic_ms(t_llm)
        return comments, fallback_headline, suggestions, fallback_watch, gem_ms, gem_bytes
    except Exception:
        logger.exception("Monthly report LLM generation failed")
        return fallback_comments, fallback_headline, fallback_suggestions, fallback_watch, None, None


async def _generate_monthly_report_payload(
    body: MonthlyReportGenerateRequest,
    user: dict,
) -> tuple[dict[str, Any], Optional[float], Optional[int]]:
    year_month = _parse_year_month(body.year_month)
    month_start, month_end = _month_window(year_month)
    user_id = int(user["user_id"])

    async with pool.acquire() as conn:
        spend_row = await conn.fetchrow(
            """SELECT COALESCE(SUM(amount), 0) AS total_spent
               FROM transactions_2
               WHERE user_id = $1
                 AND amount > 0
                 AND amount < 10000
                 AND timestamp >= $2
                 AND timestamp < $3""",
            user_id, month_start, month_end,
        )
        category_rows_raw = await conn.fetch(
            """SELECT spending_category AS category, COALESCE(SUM(amount), 0) AS total_spent
               FROM transactions_2
               WHERE user_id = $1
                 AND amount > 0
                 AND amount < 10000
                 AND timestamp >= $2
                 AND timestamp < $3
               GROUP BY spending_category
               ORDER BY total_spent DESC
               LIMIT 10""",
            user_id, month_start, month_end,
        )
        merchant_rows_raw = await conn.fetch(
            """SELECT merchant_name, COALESCE(SUM(amount), 0) AS total_spent, COUNT(*) AS transaction_count
               FROM transactions_2
               WHERE user_id = $1
                 AND amount > 0
                 AND amount < 10000
                 AND timestamp >= $2
                 AND timestamp < $3
               GROUP BY merchant_name
               ORDER BY total_spent DESC
               LIMIT 10""",
            user_id, month_start, month_end,
        )

        total_spent = float(spend_row["total_spent"] or 0)
        category_rows = [
            {"category": r["category"], "total_spent": float(r["total_spent"])}
            for r in category_rows_raw
        ]
        merchant_rows = [
            {
                "merchant_name": r["merchant_name"],
                "total_spent": float(r["total_spent"]),
                "transaction_count": int(r["transaction_count"]),
            }
            for r in merchant_rows_raw
        ]
        comments, headline, suggestions, watch_items, gem_ms, gem_bytes = _generate_monthly_llm_comments(
            year_month, total_spent, category_rows, merchant_rows
        )

        totals_payload = {
            "total_spent": total_spent,
            "total_income": 0.0,
            "net_cashflow": -total_spent,
            "transaction_count": sum(r.get("transaction_count", 0) for r in merchant_rows),
        }

        await conn.execute(
            """INSERT INTO monthly_reports (
                   user_id, year_month, summary, highlights, suggestions, totals,
                   total_spent, comments, suggestions_json,
                   category_breakdown_json, merchant_breakdown_json, generated_at
               ) VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, NOW())
               ON CONFLICT (user_id, year_month) DO UPDATE SET
                   summary = EXCLUDED.summary,
                   highlights = EXCLUDED.highlights,
                   suggestions = EXCLUDED.suggestions,
                   totals = EXCLUDED.totals,
                   total_spent = EXCLUDED.total_spent,
                   comments = EXCLUDED.comments,
                   suggestions_json = EXCLUDED.suggestions_json,
                   category_breakdown_json = EXCLUDED.category_breakdown_json,
                   merchant_breakdown_json = EXCLUDED.merchant_breakdown_json,
                   generated_at = NOW()""",
            user_id,
            year_month,
            comments,
            json.dumps(watch_items),
            json.dumps(suggestions),
            json.dumps(totals_payload),
            total_spent,
            comments,
            json.dumps(suggestions),
            json.dumps(category_rows),
            json.dumps(merchant_rows),
        )

        saved = await conn.fetchrow(
            """SELECT user_id, year_month, total_spent, comments, suggestions_json,
                      category_breakdown_json, merchant_breakdown_json, generated_at
               FROM monthly_reports
               WHERE user_id = $1 AND year_month = $2""",
            user_id, year_month,
        )

    report = {
        "year_month": saved["year_month"],
        "total_spent": float(saved["total_spent"]),
        "comments": saved["comments"],
        "headline": headline,
        "suggestions": saved["suggestions_json"],
        "watch_items": watch_items,
        "category_breakdown": saved["category_breakdown_json"],
        "merchant_breakdown": saved["merchant_breakdown_json"],
        "generated_at": saved["generated_at"].isoformat(),
    }
    return report, gem_ms, gem_bytes


@app.post("/api/reports/monthly/generate")
async def generate_monthly_report(
    body: MonthlyReportGenerateRequest,
    user: dict = Depends(get_current_user),
):
    t0 = time.perf_counter()
    report, gem_ms, gem_bytes = await _generate_monthly_report_payload(body, user)
    e2e = monotonic_ms(t0)
    rec: dict[str, Any] = {
        "tool": "e2e",
        "e2e_total_ms": e2e,
    }
    if gem_ms is not None:
        rec["gemini_call_ms"] = gem_ms
    if gem_bytes is not None:
        rec["gemini_response_bytes"] = gem_bytes
    await metrics_store.append(rec)
    return report


@app.get("/metrics")
async def get_metrics(user: dict = Depends(get_current_user)):
    return await metrics_store.get_records()


@app.get("/metrics/summary")
async def get_metrics_summary(user: dict = Depends(get_current_user)):
    return await metrics_store.summary()


@app.delete("/metrics")
async def clear_metrics(user: dict = Depends(get_current_user)):
    await metrics_store.clear()
    return {"status": "ok"}


@app.get("/api/reports/monthly")
async def list_monthly_reports(user: dict = Depends(get_current_user)):
    user_id = int(user["user_id"])
    async with pool.acquire() as conn:
        rows = await conn.fetch(
            """SELECT year_month, total_spent, comments, generated_at
               FROM monthly_reports
               WHERE user_id = $1
               ORDER BY year_month DESC""",
            user_id,
        )
    return [
        {
            "year_month": row["year_month"],
            "total_spent": float(row["total_spent"]),
            "comments": row["comments"],
            "generated_at": row["generated_at"].isoformat(),
        }
        for row in rows
    ]


@app.delete("/api/reports/monthly/{year_month}", status_code=204)
async def delete_monthly_report(year_month: str, user: dict = Depends(get_current_user)):
    normalized = _parse_year_month(year_month)
    user_id = int(user["user_id"])
    async with pool.acquire() as conn:
        result = await conn.execute(
            "DELETE FROM monthly_reports WHERE user_id = $1 AND year_month = $2",
            user_id, normalized,
        )
    if result == "DELETE 0":
        raise HTTPException(status_code=404, detail="Monthly report not found")


@app.get("/api/reports/monthly/{year_month}")
async def get_monthly_report(year_month: str, user: dict = Depends(get_current_user)):
    normalized = _parse_year_month(year_month)
    user_id = int(user["user_id"])
    async with pool.acquire() as conn:
        row = await conn.fetchrow(
            """SELECT year_month, total_spent, comments, suggestions_json,
                      category_breakdown_json, merchant_breakdown_json, highlights, generated_at
               FROM monthly_reports
               WHERE user_id = $1 AND year_month = $2""",
            user_id, normalized,
        )
    if not row:
        raise HTTPException(status_code=404, detail="Monthly report not found")
    return {
        "year_month": row["year_month"],
        "total_spent": float(row["total_spent"]),
        "comments": row["comments"],
        "suggestions": row["suggestions_json"],
        "watch_items": row["highlights"] or [],
        "category_breakdown": row["category_breakdown_json"],
        "merchant_breakdown": row["merchant_breakdown_json"],
        "generated_at": row["generated_at"].isoformat(),
    }
