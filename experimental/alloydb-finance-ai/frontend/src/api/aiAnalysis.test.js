import assert from "node:assert/strict";
import test from "node:test";

import { analyzeWithNl2Sql } from "./aiAnalysis.js";

test("analyzeWithNl2Sql returns SQL results with Gemini insight from the analysis backend", async () => {
  const calls = [];
  const analysisClient = {
    nl2sql: async (query) => {
      calls.push(["nl2sql", query]);
      return {
        method: "nl2sql",
        insight: "Dining accounted for the returned spending.",
        answer: "Dining accounted for the returned spending.",
        sql: "SELECT spending_category, SUM(amount) FROM transactions GROUP BY spending_category",
        columns: ["spending_category", "sum"],
        rows: [["dining", -42]],
        truncated: false,
      };
    },
  };

  const result = await analyzeWithNl2Sql("How much did I spend on dining?", {
    analysisClient,
  });

  assert.deepEqual(calls, [["nl2sql", "How much did I spend on dining?"]]);
  assert.equal(result.insight, "Dining accounted for the returned spending.");
  assert.equal(result.answer, "Dining accounted for the returned spending.");
  assert.equal(result.sql, "SELECT spending_category, SUM(amount) FROM transactions GROUP BY spending_category");
  assert.deepEqual(result.rows, [["dining", -42]]);
});
