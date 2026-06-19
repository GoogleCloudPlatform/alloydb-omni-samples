import os
import sys
import types
import unittest
from unittest.mock import patch

os.environ.setdefault("DATABASE_URL", "postgresql://example")
os.environ.setdefault("JWT_SECRET", "test-secret")

passlib_module = types.ModuleType("passlib")
passlib_context_module = types.ModuleType("passlib.context")


class _FakeCryptContext:
    def __init__(self, *args, **kwargs):
        pass

    def hash(self, password):
        return password

    def verify(self, plain_password, hashed_password):
        return plain_password == hashed_password


passlib_context_module.CryptContext = _FakeCryptContext
sys.modules.setdefault("passlib", passlib_module)
sys.modules.setdefault("passlib.context", passlib_context_module)

from app.main import (
    _add_finance_domain_context,
    _extract_context_categories,
    _gemini_label_cache,
    _gemini_label_clusters,
    _rewrite_common_category_aliases,
    _scope_transactions_sql,
)


class MainHelperTests(unittest.TestCase):
    def setUp(self):
        _gemini_label_cache.clear()

    def test_scope_transactions_sql_adds_user_scoped_cte(self):
        scoped = _scope_transactions_sql("SELECT * FROM transactions_2", "42")

        self.assertIn("WITH transactions_2 AS (SELECT * FROM public.transactions_2 WHERE user_id = 42)", scoped)
        self.assertTrue(scoped.endswith("SELECT * FROM transactions_2"))

    def test_scope_transactions_sql_rewrites_old_table_name(self):
        scoped = _scope_transactions_sql("SELECT * FROM transactions", "42")

        self.assertIn("public.transactions_2 WHERE user_id = 42", scoped)

    def test_scope_transactions_sql_merges_existing_with_clause(self):
        scoped = _scope_transactions_sql("WITH totals AS (SELECT 1) SELECT * FROM totals", "42")

        self.assertTrue(scoped.startswith("WITH transactions_2 AS"))
        self.assertIn(", totals AS (SELECT 1)", scoped)

    def test_rewrite_common_category_aliases_is_passthrough(self):
        sql = "SELECT SUM(amount) FROM public.transactions_2 WHERE spending_category = 'food'"
        self.assertEqual(_rewrite_common_category_aliases(sql), sql)

    def test_finance_domain_context_mentions_valid_categories(self):
        context = _add_finance_domain_context("How much did I spend on food?")

        self.assertIn("shopping", context)
        self.assertIn("food", context)
        self.assertIn("hobbies", context)

    def test_extract_context_categories_uses_question_and_sql(self):
        categories = _extract_context_categories(
            "How much did I spend on shopping?",
            "SELECT SUM(amount) FROM transactions_2",
        )

        self.assertEqual(categories, ["shopping"])

    def test_extract_context_categories_returns_empty_for_unknown(self):
        categories = _extract_context_categories(
            "How much did I spend on dining?",
            "SELECT SUM(amount) FROM transactions_2",
        )

        self.assertEqual(categories, [])

    def test_gemini_label_clusters_uses_vertex_helper(self):
        clusters_meta = [
            {"merchants": ["Target"], "terms": ["cleaning", "detergent"], "majority_category": "household"},
            {"merchants": ["Home Depot"], "terms": ["hardware", "repair"], "majority_category": "household"},
        ]
        vertex_body = {
            "candidates": [
                {
                    "content": {
                        "parts": [
                            {"text": '["Cleaning Supplies", "Home Repairs"]'},
                        ],
                    },
                },
            ],
        }

        with (
            patch("app.main.GOOGLE_CLOUD_PROJECT", "test-project"),
            patch("app.main.GEMINI_MODEL", "gemini-2.5-flash"),
            patch("app.main.post_vertex_generate_content", return_value=(vertex_body, 123)) as post_vertex,
        ):
            labels = _gemini_label_clusters(clusters_meta)

        self.assertEqual(labels, ["Cleaning Supplies", "Home Repairs"])
        self.assertEqual(post_vertex.call_count, 1)
        self.assertEqual(post_vertex.call_args.kwargs["model"], "gemini-2.5-flash")
        self.assertEqual(post_vertex.call_args.kwargs["generation_config"]["responseMimeType"], "application/json")

    def test_gemini_label_clusters_falls_back_without_vertex_config(self):
        clusters_meta = [
            {"merchants": ["Target"], "terms": ["cleaning"], "majority_category": "household"},
            {"merchants": ["Home Depot"], "terms": ["repair"], "majority_category": "household"},
        ]

        with patch("app.main.GOOGLE_CLOUD_PROJECT", ""):
            labels = _gemini_label_clusters(clusters_meta)

        self.assertEqual(labels, ["Household", "Household"])


if __name__ == "__main__":
    unittest.main()
