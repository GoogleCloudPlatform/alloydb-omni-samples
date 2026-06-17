import unittest
from unittest.mock import Mock, patch

from app.ai_analysis import (
    FALLBACK_INSIGHT,
    build_finance_insight_prompt,
    format_query_results_for_prompt,
    format_supporting_context_for_prompt,
    generate_finance_insight_with_gemini,
    generate_fallback_finance_insight,
    generate_monthly_report_comments_with_gemini,
    parse_gemini_text_response,
)


class FinanceInsightPromptTests(unittest.TestCase):
    def test_formats_columns_and_rows_for_prompt(self):
        formatted = format_query_results_for_prompt(
            columns=["category", "total_spent"],
            rows=[["dining", 42.5], ["groceries", 18.25]],
        )

        self.assertIn('"category": "dining"', formatted)
        self.assertIn('"total_spent": 42.5', formatted)

    def test_build_prompt_includes_finance_rules_and_context(self):
        prompt = build_finance_insight_prompt(
            user_question="How much did I spend on dining?",
            sql_query="SELECT spending_category, SUM(amount) FROM transactions",
            query_results='[{"category": "dining", "total_spent": 42.5}]',
            supporting_context='[{"merchant_name": "Starbucks", "description": "Coffee and breakfast"}]',
        )

        self.assertIn("You are a helpful personal finance analysis assistant.", prompt)
        self.assertIn("How much did I spend on dining?", prompt)
        self.assertIn("Only use the data provided above.", prompt)
        self.assertIn("SELECT spending_category", prompt)
        self.assertIn("Starbucks", prompt)
        self.assertIn("explain likely drivers", prompt)

    def test_formats_supporting_context_for_prompt(self):
        formatted = format_supporting_context_for_prompt([
            {
                "merchant_name": "Trader Joes",
                "amount": -91.75,
                "spending_category": "groceries",
                "description": "Bought weekly groceries.",
            }
        ])

        self.assertIn('"merchant_name": "Trader Joes"', formatted)
        self.assertIn('"description": "Bought weekly groceries."', formatted)

    def test_parse_gemini_text_response_returns_candidate_text(self):
        body = {
            "candidates": [
                {
                    "content": {
                        "parts": [
                            {"text": "Dining was your largest returned category."},
                        ],
                    },
                },
            ],
        }

        self.assertEqual(parse_gemini_text_response(body), "Dining was your largest returned category.")

    def test_parse_gemini_text_response_falls_back_for_missing_text(self):
        self.assertEqual(parse_gemini_text_response({"candidates": []}), FALLBACK_INSIGHT)

    def test_generate_finance_insight_uses_vertex_endpoint_and_adc_token(self):
        response = Mock()
        response.json.return_value = {
            "candidates": [{"content": {"parts": [{"text": "Dining was the only returned category."}]}}],
        }
        response.content = b'{"candidates":[]}'
        response.raise_for_status.return_value = None
        credentials = Mock()
        credentials.token = "vertex-access-token"

        with (
            patch.dict(
                "os.environ",
                {
                    "GOOGLE_CLOUD_PROJECT": "test-project",
                    "GOOGLE_CLOUD_LOCATION": "us-central1",
                    "GEMINI_MODEL": "gemini-2.5-flash",
                },
                clear=False,
            ),
            patch("app.ai_analysis.google.auth.default", return_value=(credentials, "test-project")) as default_auth,
            patch("app.ai_analysis.GoogleAuthRequest") as auth_request,
            patch("app.ai_analysis.requests.post", return_value=response) as post,
        ):
            insight = generate_finance_insight_with_gemini(
                user_question="How much did I spend on dining?",
                sql_query="SELECT * FROM transactions",
                columns=["category"],
                rows=[["dining"]],
            )

        self.assertEqual(insight, "Dining was the only returned category.")
        default_auth.assert_called_once_with(scopes=["https://www.googleapis.com/auth/cloud-platform"])
        credentials.refresh.assert_called_once_with(auth_request.return_value)
        self.assertEqual(
            post.call_args.args[0],
            "https://us-central1-aiplatform.googleapis.com/v1/projects/test-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent",
        )
        self.assertEqual(post.call_args.kwargs["headers"]["Authorization"], "Bearer vertex-access-token")
        self.assertEqual(post.call_args.kwargs["json"]["contents"][0]["role"], "user")

    def test_generate_finance_insight_uses_vertex_config_from_environment(self):
        response = Mock()
        response.json.return_value = {
            "candidates": [{"content": {"parts": [{"text": "Used environment config."}]}}],
        }
        response.content = b"{}"
        response.raise_for_status.return_value = None
        credentials = Mock()
        credentials.token = "vertex-access-token"

        with (
            patch.dict(
                "os.environ",
                {
                    "GOOGLE_CLOUD_PROJECT": "env-project",
                    "GOOGLE_CLOUD_LOCATION": "europe-west4",
                    "GEMINI_MODEL": "gemini-env-model",
                },
                clear=False,
            ),
            patch("app.ai_analysis.google.auth.default", return_value=(credentials, "env-project")),
            patch("app.ai_analysis.GoogleAuthRequest"),
            patch("app.ai_analysis.requests.post", return_value=response) as post,
        ):
            insight = generate_finance_insight_with_gemini(
                user_question="How much did I spend on dining?",
                sql_query="SELECT * FROM transactions",
                columns=["category"],
                rows=[["dining"]],
            )

        self.assertEqual(insight, "Used environment config.")
        self.assertEqual(
            post.call_args.args[0],
            "https://europe-west4-aiplatform.googleapis.com/v1/projects/env-project/locations/europe-west4/publishers/google/models/gemini-env-model:generateContent",
        )

    def test_generate_finance_insight_uses_explicit_model_override(self):
        response = Mock()
        response.json.return_value = {
            "candidates": [{"content": {"parts": [{"text": "Used custom model."}]}}],
        }
        response.content = b"{}"
        response.raise_for_status.return_value = None
        credentials = Mock()
        credentials.token = "vertex-access-token"

        with (
            patch.dict(
                "os.environ",
                {
                    "GOOGLE_CLOUD_PROJECT": "test-project",
                    "GOOGLE_CLOUD_LOCATION": "us-central1",
                },
                clear=False,
            ),
            patch("app.ai_analysis.google.auth.default", return_value=(credentials, "test-project")),
            patch("app.ai_analysis.GoogleAuthRequest"),
            patch("app.ai_analysis.requests.post", return_value=response) as post,
        ):
            insight = generate_finance_insight_with_gemini(
                user_question="How much did I spend on dining?",
                sql_query="SELECT * FROM transactions",
                columns=["category"],
                rows=[["dining"]],
                model="custom-model",
            )

        self.assertEqual(insight, "Used custom model.")
        self.assertEqual(
            post.call_args.args[0],
            "https://us-central1-aiplatform.googleapis.com/v1/projects/test-project/locations/us-central1/publishers/google/models/custom-model:generateContent",
        )

    def test_generate_finance_insight_requires_google_cloud_project(self):
        with patch.dict("os.environ", {"GOOGLE_CLOUD_PROJECT": "", "GOOGLE_CLOUD_LOCATION": "us-central1"}):
            with self.assertRaises(ValueError) as context:
                generate_finance_insight_with_gemini(
                    user_question="How much did I spend on dining?",
                    sql_query="SELECT * FROM transactions",
                    columns=["category"],
                    rows=[["dining"]],
                )

        self.assertEqual(str(context.exception), "GOOGLE_CLOUD_PROJECT must be set")

    def test_generate_finance_insight_requires_google_cloud_location(self):
        with patch.dict("os.environ", {"GOOGLE_CLOUD_PROJECT": "test-project", "GOOGLE_CLOUD_LOCATION": ""}):
            with self.assertRaises(ValueError) as context:
                generate_finance_insight_with_gemini(
                    user_question="How much did I spend on dining?",
                    sql_query="SELECT * FROM transactions",
                    columns=["category"],
                    rows=[["dining"]],
                )

        self.assertEqual(str(context.exception), "GOOGLE_CLOUD_LOCATION must be set")

    def test_generate_finance_insight_rejects_invalid_project(self):
        with patch.dict("os.environ", {"GOOGLE_CLOUD_PROJECT": "../metadata", "GOOGLE_CLOUD_LOCATION": "us-central1"}):
            with self.assertRaises(ValueError) as context:
                generate_finance_insight_with_gemini(
                    user_question="How much did I spend on dining?",
                    sql_query="SELECT * FROM transactions",
                    columns=["category"],
                    rows=[["dining"]],
                )

        self.assertEqual(str(context.exception), "GOOGLE_CLOUD_PROJECT contains invalid characters")

    def test_generate_finance_insight_rejects_invalid_model_name(self):
        with patch.dict("os.environ", {"GOOGLE_CLOUD_PROJECT": "test-project", "GOOGLE_CLOUD_LOCATION": "us-central1"}):
            with self.assertRaises(ValueError) as context:
                generate_finance_insight_with_gemini(
                    user_question="How much did I spend on dining?",
                    sql_query="SELECT * FROM transactions",
                    columns=["category"],
                    rows=[["dining"]],
                    model="../metadata",
                )

        self.assertEqual(str(context.exception), "GEMINI_MODEL contains invalid characters")

    def test_generate_finance_insight_falls_back_when_adc_is_unavailable(self):
        with patch("app.ai_analysis.google.auth.default", side_effect=RuntimeError("missing adc")):
            insight = generate_finance_insight_with_gemini(
                user_question="How much did I spend on dining?",
                sql_query="SELECT * FROM transactions",
                columns=[],
                rows=[],
            )

        self.assertEqual(insight, "No matching data was found. Try asking about a specific category, merchant, budget, or time period.")

    def test_generate_finance_insight_falls_back_without_vertex_config(self):
        insight = generate_finance_insight_with_gemini(
            user_question="How much did I spend on dining?",
            sql_query="SELECT * FROM transactions",
            columns=[],
            rows=[],
        )

        self.assertEqual(insight, "No matching data was found. Try asking about a specific category, merchant, budget, or time period.")

    def test_generate_monthly_report_comments_uses_vertex_json_payload(self):
        response = Mock()
        response.json.return_value = {
            "candidates": [
                {
                    "content": {
                        "parts": [
                            {
                                "text": (
                                    '{"comments":"Dining drove most spending.",'
                                    '"suggestions":["Cook once","Review subscriptions","Set a cap"]}'
                                ),
                            },
                        ],
                    },
                },
            ],
        }
        response.content = b'{"candidates":[]}'
        response.raise_for_status.return_value = None
        credentials = Mock()
        credentials.token = "vertex-access-token"

        with (
            patch.dict(
                "os.environ",
                {
                    "GOOGLE_CLOUD_PROJECT": "test-project",
                    "GOOGLE_CLOUD_LOCATION": "us-central1",
                    "GEMINI_MODEL": "gemini-2.5-flash",
                },
                clear=False,
            ),
            patch("app.ai_analysis.google.auth.default", return_value=(credentials, "test-project")),
            patch("app.ai_analysis.GoogleAuthRequest"),
            patch("app.ai_analysis.requests.post", return_value=response) as post,
        ):
            comments, suggestions, response_bytes = generate_monthly_report_comments_with_gemini(
                year_month="2026-05",
                total_spent=250.0,
                category_rows=[{"category": "dining", "total_spent": 150.0}],
                merchant_rows=[{"merchant_name": "Cafe", "total_spent": 60.0, "transaction_count": 2}],
            )

        self.assertEqual(comments, "Dining drove most spending.")
        self.assertEqual(suggestions, ["Cook once", "Review subscriptions", "Set a cap"])
        self.assertEqual(response_bytes, len(response.content))
        self.assertEqual(post.call_args.kwargs["headers"]["Authorization"], "Bearer vertex-access-token")
        self.assertEqual(post.call_args.kwargs["json"]["contents"][0]["role"], "user")
        self.assertEqual(post.call_args.kwargs["json"]["generationConfig"]["responseMimeType"], "application/json")

    def test_fallback_finance_insight_explains_null_aggregate_as_no_matching_data(self):
        insight = generate_fallback_finance_insight(
            user_question="How much did I spend on food?",
            columns=["total_spending"],
            rows=[[None]],
            supporting_context=[],
        )

        self.assertEqual(insight, "No matching data was found. Try asking about a specific category, merchant, budget, or time period.")

    def test_fallback_finance_insight_uses_supporting_context_for_aggregate(self):
        insight = generate_fallback_finance_insight(
            user_question="How much did I spend on groceries?",
            columns=["total_spending"],
            rows=[[-250.0]],
            supporting_context=[
                {
                    "merchant_name": "Trader Joes",
                    "amount": -100.0,
                    "description": "Weekly groceries and pantry staples.",
                },
                {
                    "merchant_name": "Whole Foods",
                    "amount": -80.0,
                    "description": "Produce and household groceries.",
                },
            ],
        )

        self.assertIn("Trader Joes", insight)
        self.assertIn("Whole Foods", insight)


if __name__ == "__main__":
    unittest.main()
