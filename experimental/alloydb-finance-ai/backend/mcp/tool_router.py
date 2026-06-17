"""Classify a free-form user message into a single app tool (v1 router)."""

from __future__ import annotations

import logging
from typing import Any, Optional

from mcp.constants import GENERAL, ROUTER_TOOLS, ROUTER_TOOL_SCHEMA_ENUM
from mcp.gemini_json import generate_json

logger = logging.getLogger(__name__)


class CustomerToolRouterError(Exception):
    """Raised when routing cannot be completed (e.g. Gemini failure)."""


def _router_response_schema() -> dict[str, Any]:
    return {
        "type": "object",
        "properties": {
            "tool": {
                "type": "string",
                "enum": ROUTER_TOOL_SCHEMA_ENUM,
                "description": "Which AlloyFinance capability best matches the user message.",
            },
            "reasoning": {
                "type": "string",
                "description": "One short phrase for debugging (not shown to end users unless desired).",
            },
        },
        "required": ["tool"],
    }


def _build_prompt(user_message: str) -> str:
    tools_desc = """
- nl2sql: Questions that need querying transaction data with SQL-style logic — totals, filters,
  comparisons, budgets vs spend, top merchants, "how much did I spend", counts, listings by rules.
- semantic_search: Finding transactions by meaning or similarity — "like my coffee purchases",
  "things similar to gym charges", fuzzy vibe-based search, not a precise numeric report.
- monthly_reports: Month-level narrative summaries, "how did I do in January", spending reports,
  commentary-style monthly review (not ad-hoc SQL tables).
- clustering: User explicitly wants groups, clusters, segments, or patterns bucketing without a
  specific SQL question (feature may be limited; still route here when that is the intent).
- general: Greetings, off-topic chat, unclear input, account/settings questions, or anything that
  does not map clearly to the tools above.
""".strip()
    return (
        "You route user messages to exactly ONE tool id for a personal finance app.\n"
        f"{tools_desc}\n"
        "Return JSON only with keys: tool (required), reasoning (optional, max ~20 words).\n"
        f"User message:\n{user_message.strip()}"
    )


def _normalize_tool(raw: Any) -> str:
    if raw is None:
        return GENERAL
    t = str(raw).strip().lower().replace("-", "_")
    if t in ROUTER_TOOLS:
        return t
    return GENERAL


def route_user_message(
    user_message: str,
    *,
    project_id: str,
    location: str,
    model: str,
) -> dict[str, Optional[str]]:
    """
    Returns {"tool": <id>, "reasoning": <str|None>}.

    On model/JSON failures raises CustomerToolRouterError.
    """
    text = (user_message or "").strip()
    if not text:
        return {"tool": GENERAL, "reasoning": None}

    prompt = _build_prompt(text)
    try:
        parsed = generate_json(
            project_id=project_id,
            location=location,
            model=model,
            prompt=prompt,
            temperature=0.1,
            # Vertex generateContent is stricter than AI Studio about responseSchema
            # shape; for v1 routing we rely on prompt-constrained JSON output.
            response_schema=None,
        )
    except RuntimeError as e:
        raise CustomerToolRouterError(str(e)) from e
    except Exception as e:
        logger.exception("Unexpected error during tool routing")
        raise CustomerToolRouterError("Routing failed") from e

    tool = _normalize_tool(parsed.get("tool"))
    reasoning = parsed.get("reasoning")
    if reasoning is not None:
        reasoning = str(reasoning).strip() or None
    return {"tool": tool, "reasoning": reasoning}
