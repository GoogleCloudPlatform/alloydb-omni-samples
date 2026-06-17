import json
import os
import re
from typing import Any

import google.auth
from google.auth.transport.requests import Request as GoogleAuthRequest
import requests


DEFAULT_GEMINI_MODEL = "gemini-2.5-flash"
FALLBACK_INSIGHT = "I found the data, but couldn’t generate an AI explanation for it."
GEMINI_MODEL_PATTERN = re.compile(r"^[A-Za-z0-9._-]+$")
GOOGLE_CLOUD_CONFIG_PATTERN = re.compile(r"^[A-Za-z0-9._:-]+$")
GOOGLE_CLOUD_LOCATION_PATTERN = re.compile(r"^[A-Za-z0-9-]+$")
VERTEX_AI_SCOPE = "https://www.googleapis.com/auth/cloud-platform"


def get_google_cloud_project() -> str:
    project = os.getenv("GOOGLE_CLOUD_PROJECT", "").strip()
    if not project:
        raise ValueError("GOOGLE_CLOUD_PROJECT must be set")
    if not GOOGLE_CLOUD_CONFIG_PATTERN.fullmatch(project):
        raise ValueError("GOOGLE_CLOUD_PROJECT contains invalid characters")
    return project


def get_google_cloud_location() -> str:
    location = os.getenv("GOOGLE_CLOUD_LOCATION", "").strip()
    if not location:
        raise ValueError("GOOGLE_CLOUD_LOCATION must be set")
    if not GOOGLE_CLOUD_LOCATION_PATTERN.fullmatch(location):
        raise ValueError("GOOGLE_CLOUD_LOCATION contains invalid characters")
    return location


def get_gemini_model() -> str:
    gemini_model = os.getenv("GEMINI_MODEL", DEFAULT_GEMINI_MODEL).strip() or DEFAULT_GEMINI_MODEL
    return validate_gemini_model(gemini_model)


def validate_gemini_model(gemini_model: str) -> str:
    if not GEMINI_MODEL_PATTERN.fullmatch(gemini_model):
        raise ValueError("GEMINI_MODEL contains invalid characters")
    return gemini_model


def build_vertex_generate_content_url(*, project: str, location: str, model: str) -> str:
    return (
        f"https://{location}-aiplatform.googleapis.com/v1/projects/{project}/locations/{location}"
        f"/publishers/google/models/{model}:generateContent"
    )


def get_vertex_access_token() -> str:
    try:
        credentials, _ = google.auth.default(scopes=[VERTEX_AI_SCOPE])
        credentials.refresh(GoogleAuthRequest())
    except Exception as exc:
        raise RuntimeError("Google ADC credentials not found") from exc

    token = getattr(credentials, "token", None)
    if not token:
        raise RuntimeError("Google ADC credentials did not return an access token")
    return token


def post_vertex_generate_content(
    *,
    prompt: str,
    generation_config: dict[str, Any],
    model: str | None = None,
    timeout_s: int = 30,
) -> tuple[dict[str, Any], int]:
    selected_model = validate_gemini_model(model) if model else get_gemini_model()
    project = get_google_cloud_project()
    location = get_google_cloud_location()
    url = build_vertex_generate_content_url(project=project, location=location, model=selected_model)
    access_token = get_vertex_access_token()
    payload: dict[str, Any] = {
        "contents": [{"role": "user", "parts": [{"text": prompt}]}],
        "generationConfig": generation_config,
    }

    response = requests.post(
        url,
        headers={"Authorization": f"Bearer {access_token}", "Content-Type": "application/json"},
        json=payload,
        timeout=timeout_s,
    )
    response.raise_for_status()
    raw = response.content or b""
    return response.json(), len(raw)


def format_query_results_for_prompt(columns: list[str], rows: list[list[Any]]) -> str:
    """Convert SQL result columns/rows into compact JSON for Gemini."""
    records = [
        {column: row[index] if index < len(row) else None for index, column in enumerate(columns)}
        for row in rows
    ]
    return json.dumps(records, ensure_ascii=False, default=str)


def format_supporting_context_for_prompt(supporting_context: list[dict[str, Any]] | None) -> str:
    """Convert representative transaction rows into compact JSON for Gemini."""
    return json.dumps(supporting_context or [], ensure_ascii=False, default=str)


def build_finance_insight_prompt(
    user_question: str,
    sql_query: str,
    query_results: str,
    supporting_context: str = "[]",
) -> str:
    return f"""
You are a helpful personal finance analysis assistant.

The user asked:
<user_question>
{user_question}
</user_question>

The generated SQL query was:
<sql_query>
{sql_query}
</sql_query>

The database returned these rows:
<query_results>
{query_results}
</query_results>

Supporting transaction context:
<supporting_transactions>
{supporting_context}
</supporting_transactions>

Your task:
Explain what this result means in clear, concise natural language before the raw data is shown to the user. Use the supporting transactions to explain likely drivers behind totals when they are relevant.

Rules:
- Only use the data provided above.
- Treat the supporting transaction context as part of the provided data.
- Do not invent numbers, categories, merchants, time periods, or trends.
- If the result is empty, say that no matching data was found and suggest a more specific query.
- Do not mention backend implementation details unless useful.
- Do not say "based on the SQL query" unless necessary.
- Keep the response to 2-5 sentences.
- If there is an obvious insight, highlight it.
- If the result contains totals, rankings, categories, or budget comparisons, explain the key takeaway.
- If supporting transactions include descriptions, merchants, or unusually large purchases, use them to explain why the total may be high or low.
""".strip()


def parse_gemini_text_response(body: dict[str, Any]) -> str:
    try:
        text = body["candidates"][0]["content"]["parts"][0]["text"]
    except (KeyError, IndexError, TypeError):
        return FALLBACK_INSIGHT

    normalized = " ".join(str(text).strip().split())
    return normalized or FALLBACK_INSIGHT


def generate_fallback_finance_insight(
    *,
    user_question: str,
    columns: list[str],
    rows: list[list[Any]],
    supporting_context: list[dict[str, Any]] | None = None,
) -> str:
    """Return a deterministic explanation when Gemini is unavailable."""
    if not rows:
        return "No matching data was found. Try asking about a specific category, merchant, budget, or time period."

    non_null_values = [
        value
        for row in rows
        for value in row
        if value is not None
    ]
    if not non_null_values:
        return "No matching data was found. Try asking about a specific category, merchant, budget, or time period."

    if supporting_context:
        merchants = []
        descriptions = []
        for item in supporting_context[:5]:
            merchant = item.get("merchant_name")
            if merchant and merchant not in merchants:
                merchants.append(str(merchant))
            description = item.get("description")
            if description:
                descriptions.append(str(description))

        merchant_text = ", ".join(merchants[:3])
        detail_text = f" The largest matching transactions include {merchant_text}." if merchant_text else ""
        if descriptions:
            detail_text += f" Context from the transaction descriptions points to: {descriptions[0]}"

        if len(rows) == 1 and len(columns) == 1:
            return f"The query returned {columns[0]}: {rows[0][0]}.{detail_text}"
        return f"I found matching transaction context for this question.{detail_text}"

    if len(rows) == 1 and len(columns) == 1:
        return f"The query returned {columns[0]}: {rows[0][0]}."

    return FALLBACK_INSIGHT


def generate_monthly_report_comments_with_gemini(
    *,
    year_month: str,
    total_spent: float,
    category_rows: list[dict[str, Any]],
    merchant_rows: list[dict[str, Any]],
    model: str | None = None,
    timeout_s: int = 40,
) -> tuple[str, list[str], int]:
    compact_payload = {
        "month": year_month,
        "total_spent_usd": round(total_spent, 2),
        "top_categories": category_rows[:8],
        "top_merchants": merchant_rows[:8],
    }
    prompt = (
        "You are a personal finance coach helping someone understand and improve their spending habits.\n"
        "Given monthly spending data, produce a detailed, actionable financial report.\n"
        "Return ONLY valid JSON with exactly these keys:\n"
        "- comments: string (3-4 sentences summarizing the month: total context, biggest categories, notable patterns, overall financial health signal)\n"
        "- headline: string (one punchy sentence — a single key takeaway for the month, e.g. 'Dining out drove 40% of your spending this month')\n"
        "- suggestions: array of exactly 3 objects, each with:\n"
        "    - title: string (short action label, e.g. 'Cut subscription overlap')\n"
        "    - rationale: string (1-2 sentences explaining why this matters and what to do specifically, with dollar figures)\n"
        "    - estimated_savings_usd: number (realistic monthly savings estimate in USD)\n"
        "    - difficulty: string — one of 'Easy', 'Medium', 'Hard'\n"
        "    - impact: string — one of 'Low', 'Medium', 'High'\n"
        "- watch_items: array of 2-3 strings (spending behaviors worth monitoring, e.g. 'Frequent small purchases at coffee shops added up to $X')\n"
        "Rules:\n"
        "- Anchor every claim to exact dollar figures from the data.\n"
        "- Prioritize suggestions by highest realistic impact.\n"
        "- Be specific: name the category or merchant, not generic advice.\n"
        "- Keep tone direct, friendly, and non-judgmental.\n"
        f"Input JSON:\n{compact_payload}"
    )
    body, response_bytes = post_vertex_generate_content(
        prompt=prompt,
        generation_config={
            "temperature": 0.2,
            "responseMimeType": "application/json",
        },
        model=model,
        timeout_s=timeout_s,
    )
    text = body["candidates"][0]["content"]["parts"][0]["text"]
    parsed = json.loads(text)
    comments = str(parsed.get("comments", "")).strip()
    suggestions = parsed.get("suggestions", [])
    if not comments or not isinstance(suggestions, list):
        raise ValueError("Monthly report response did not include comments and suggestions")
    return comments, suggestions[:3], response_bytes


def generate_finance_insight_with_gemini(
    *,
    user_question: str,
    sql_query: str,
    columns: list[str],
    rows: list[list[Any]],
    supporting_context: list[dict[str, Any]] | None = None,
    model: str | None = None,
    timeout_s: int = 30,
) -> str:
    """Generate a concise natural-language explanation for already-returned SQL results."""
    if not rows or not any(value is not None for row in rows for value in row):
        return generate_fallback_finance_insight(
            user_question=user_question,
            columns=columns,
            rows=rows,
            supporting_context=supporting_context,
        )

    query_results = format_query_results_for_prompt(columns=columns, rows=rows)
    supporting_transactions = format_supporting_context_for_prompt(supporting_context)
    prompt = build_finance_insight_prompt(
        user_question=user_question,
        sql_query=sql_query,
        query_results=query_results,
        supporting_context=supporting_transactions,
    )
    body, _response_bytes = post_vertex_generate_content(
        prompt=prompt,
        generation_config={
            "temperature": 0.2,
        },
        model=model,
        timeout_s=timeout_s,
    )
    return parse_gemini_text_response(body)
