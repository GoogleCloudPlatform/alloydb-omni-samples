"""
Run once to generate embeddings for all transactions that don't have one yet.

Usage:
    python backfill_embeddings.py
"""
import asyncio
import os
import asyncpg

DATABASE_URL = os.environ["DATABASE_URL"]
CONCURRENCY = 1  # number of parallel embedding calls


async def embed_one(pool, txn_id, idx, total, counters):
    try:
        async with pool.acquire() as conn:
            await conn.execute(
                """UPDATE transactions
                   SET embedding = google_ml.embedding(
                       'text-embedding-005',
                       COALESCE(merchant_name, '') || ' ' ||
                       COALESCE(merchant_category, '') || ' ' ||
                       COALESCE(spending_category, '') || ' ' ||
                       COALESCE(description, '')
                   )::vector
                   WHERE transaction_id = $1""",
                txn_id,
            )
        counters["updated"] += 1
        print(f"  [{idx}/{total}] {txn_id} ✓")
    except Exception as e:
        counters["failed"] += 1
        print(f"  [{idx}/{total}] {txn_id} FAILED: {e}")


async def main():
    pool = await asyncpg.create_pool(DATABASE_URL, ssl=False, min_size=1, max_size=CONCURRENCY)
    try:
        async with pool.acquire() as conn:
            ids = await conn.fetch(
                "SELECT transaction_id FROM transactions WHERE embedding IS NULL"
            )
        total = len(ids)
        print(f"Found {total} transactions to embed (concurrency={CONCURRENCY}).")

        counters = {"updated": 0, "failed": 0}
        sem = asyncio.Semaphore(CONCURRENCY)

        async def bounded(txn_id, idx):
            async with sem:
                await embed_one(pool, txn_id, idx, total, counters)

        await asyncio.gather(*[
            bounded(row["transaction_id"], i + 1)
            for i, row in enumerate(ids)
        ])
    finally:
        await pool.close()

    print(f"\nDone. {counters['updated']} updated, {counters['failed']} failed.")


asyncio.run(main())
