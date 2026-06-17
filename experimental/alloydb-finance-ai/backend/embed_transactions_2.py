import asyncio
import asyncpg
import os
import time
import google.auth
import google.auth.transport.requests
import requests
from pathlib import Path
from dotenv import load_dotenv

load_dotenv(Path(__file__).resolve().parent.parent / ".env")

PROJECT = os.environ["GOOGLE_CLOUD_PROJECT"]
LOCATION = os.environ.get("GOOGLE_CLOUD_LOCATION", "us-central1")
BATCH_SIZE = 100

_creds = None
_session = None

def _get_token() -> str:
    global _creds, _session
    if _creds is None:
        _creds, _ = google.auth.default(scopes=["https://www.googleapis.com/auth/cloud-platform"])
        _session = google.auth.transport.requests.Request()
    if not _creds.valid:
        _creds.refresh(_session)
    return _creds.token


def _embed_batch(texts: list[str]) -> list[list[float]]:
    url = (
        f"https://{LOCATION}-aiplatform.googleapis.com/v1/projects/{PROJECT}"
        f"/locations/{LOCATION}/publishers/google/models/text-embedding-005:predict"
    )
    body = {"instances": [{"content": t} for t in texts]}
    for attempt in range(5):
        resp = requests.post(
            url,
            json=body,
            headers={"Authorization": f"Bearer {_get_token()}", "Content-Type": "application/json"},
            timeout=120,
        )
        if resp.status_code == 429:
            wait = 30 * (attempt + 1)
            print(f"  Rate limited, waiting {wait}s (attempt {attempt + 1}/5)...", flush=True)
            time.sleep(wait)
            continue
        resp.raise_for_status()
        return [p["embeddings"]["values"] for p in resp.json()["predictions"]]
    raise RuntimeError("Vertex AI rate limit: all 5 attempts failed")


async def embed():
    db_url = os.environ["DATABASE_URL"].replace("host.docker.internal", "localhost")
    ssl = "require" if "sslmode=require" in db_url else False
    conn = await asyncpg.connect(db_url, ssl=ssl)

    total = 0
    chunk = 1
    while True:
        rows = await conn.fetch(
            """
            SELECT transaction_id, quantity, item_description, spending_category, merchant_name, country
            FROM transactions_2
            WHERE embedding IS NULL
            LIMIT $1
            """,
            BATCH_SIZE,
        )
        if not rows:
            break

        t0 = time.time()
        texts = [
            f"Purchased {r['quantity']} {r['item_description']}. "
            f"Category: {r['spending_category']}. "
            f"Store type: {r['merchant_name']}. "
            f"Country: {r['country']}."
            for r in rows
        ]

        vectors = _embed_batch(texts)

        await conn.executemany(
            "UPDATE transactions_2 SET embedding = $1 WHERE transaction_id = $2",
            [(f"[{','.join(str(x) for x in vec)}]", r["transaction_id"])
             for vec, r in zip(vectors, rows)],
        )

        total += len(rows)
        print(f"Chunk {chunk}: {len(rows)} rows ({total} total) in {time.time()-t0:.1f}s", flush=True)
        chunk += 1
        time.sleep(10)

    await conn.close()
    print(f"Done. {total} rows embedded.")


asyncio.run(embed())
