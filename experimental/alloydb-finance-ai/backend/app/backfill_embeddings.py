"""
Run once to generate embeddings for all transactions that don't have one yet.

Usage:
    python backfill_embeddings.py
"""
import asyncio
import os
import asyncpg

DATABASE_URL = os.environ["DATABASE_URL"]

async def main():
    pool = await asyncpg.create_pool(DATABASE_URL, ssl=False, min_size=1, max_size=1)
    try:
        total_updated = 0
        chunk_size = 100
        async with pool.acquire() as conn:
            while True:
                res = await conn.execute(
                    f"""UPDATE transactions
                       SET embedding = google_ml.embedding(
                           'text-embedding-005',
                           COALESCE(merchant_name, '') || ' ' ||
                           COALESCE(merchant_category, '') || ' ' ||
                           COALESCE(spending_category, '') || ' ' ||
                           COALESCE(description, '')
                       )::vector
                       WHERE transaction_id IN (
                           SELECT transaction_id FROM transactions
                           WHERE embedding IS NULL
                           LIMIT {chunk_size}
                       )"""
                )
                words = res.split()
                count = int(words[-1]) if words else 0
                if count == 0:
                    break
                total_updated += count
                print(f"Updated {count} transactions (Total updated: {total_updated})...")
    finally:
        await pool.close()

    print(f"\nDone. {total_updated} updated successfully.")


asyncio.run(main())
