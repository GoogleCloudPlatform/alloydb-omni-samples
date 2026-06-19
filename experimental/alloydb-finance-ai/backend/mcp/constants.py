# Stable tool ids returned by the router and echoed by the API.

NL2SQL = "nl2sql"
SEMANTIC_SEARCH = "semantic_search"
MONTHLY_REPORTS = "monthly_reports"
CLUSTERING = "clustering"
GENERAL = "general"

ROUTER_TOOLS: tuple[str, ...] = (
    NL2SQL,
    SEMANTIC_SEARCH,
    MONTHLY_REPORTS,
    CLUSTERING,
    GENERAL,
)

ROUTER_TOOL_SCHEMA_ENUM = list(ROUTER_TOOLS)
