import os
import csv
from pathlib import Path
from collections import Counter

import psycopg2
from psycopg2.extras import execute_values
from dotenv import load_dotenv


def main() -> None:
    dev_user_email = "foo@bar.com"

    # Load environment variables from .env at repo root
    script_dir = Path(__file__).resolve().parent
    env_path = script_dir.parent / ".env"
    if env_path.exists():
        load_dotenv(env_path)

    database_url = os.getenv("DATABASE_URL")
    if not database_url:
        raise ValueError("DATABASE_URL is not set; ensure it is in .env or the environment")

    csv_path = script_dir / "synthetic-data" / "output_data" / "transactions.csv"
    if not csv_path.exists():
        raise FileNotFoundError(f"CSV file not found at {csv_path}")

    conn = psycopg2.connect(database_url)
    try:
        with conn:
            with conn.cursor() as cur:
                cur.execute(
                    """
                    CREATE TABLE IF NOT EXISTS users (
                        id SERIAL PRIMARY KEY,
                        google_id TEXT UNIQUE,
                        email TEXT UNIQUE NOT NULL,
                        password_hash TEXT,
                        name TEXT,
                        picture TEXT,
                        created_at TIMESTAMPTZ DEFAULT NOW()
                    )
                    """
                )

                cur.execute(
                    """
                    INSERT INTO users (email, password_hash, name, picture)
                    VALUES (%s, %s, %s, %s)
                    ON CONFLICT (email) DO UPDATE SET
                        password_hash = EXCLUDED.password_hash,
                        name = EXCLUDED.name
                    RETURNING id
                    """,
                    (dev_user_email, None, "Dev User", None),
                )
                dev_user_id = cur.fetchone()[0]

                cur.execute(
                    """
                    CREATE TABLE IF NOT EXISTS transactions (
                        transaction_id UUID PRIMARY KEY,
                        user_id INTEGER NOT NULL REFERENCES users(id),
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
                    """
                )

                # Read CSV rows
                with csv_path.open(newline="", encoding="utf-8") as f:
                    reader = csv.DictReader(f)
                    rows = list(reader)

                if not rows:
                    print("No rows found in CSV; nothing to insert.")
                    return

                # Fetch existing transaction_ids so we can detect conflicts explicitly
                cur.execute("SELECT transaction_id FROM transactions;")
                existing_ids = {row[0] for row in cur.fetchall()}

                # Prepare data for bulk insert, logging any conflicts we detect client-side
                records = []
                for r in rows:
                    tx_id = r["transaction_id"]
                    if tx_id in existing_ids:
                        # Print the CSV row that would conflict
                        print(f"Conflict for transaction_id={tx_id}: {r}")
                        continue

                    records.append(
                        (
                            tx_id,
                            dev_user_id,
                            r["timestamp"],
                            float(r["amount"]),
                            r["merchant_name"],
                            r["merchant_category"],
                            r["spending_category"],
                            r["transaction_type"],
                            r["payment_method"],
                            r["city"],
                            r["country"],
                            r["currency"],
                            r.get("description"),
                        )
                    )

                # Count existing rows before insert (after filtering duplicates)
                cur.execute("SELECT COUNT(*) FROM transactions;")
                before = cur.fetchone()[0]

                # Insert data, ignoring duplicates on primary key
                insert_sql = """
                    INSERT INTO transactions (
                        transaction_id,
                        user_id,
                        timestamp,
                        amount,
                        merchant_name,
                        merchant_category,
                        spending_category,
                        transaction_type,
                        payment_method,
                        city,
                        country,
                        currency,
                        description
                    )
                    VALUES %s
                    ON CONFLICT (transaction_id) DO NOTHING
                """
                execute_values(cur, insert_sql, records, page_size=1000)

                # Report actual number of new rows
                cur.execute("SELECT COUNT(*) FROM transactions;")
                after = cur.fetchone()[0]
                print(f"Inserted {after - before} new transactions into the 'transactions' table.")
                print(f"Assigned imported rows to user_id={dev_user_id} ({dev_user_email}).")

                category_counts = Counter(r["spending_category"] for r in rows)
                print("CSV category counts:")
                for category, count in sorted(category_counts.items()):
                    print(f"  - {category}: {count}")
    finally:
        conn.close()


if __name__ == "__main__":
    main()

