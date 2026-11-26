import assert from "node:assert/strict";

const actual = "bun" in process.versions ? "bun" : "node";
assert.equal(actual, process.env.SPIKE_CANDIDATE, "Vitest worker must use the candidate runtime");
