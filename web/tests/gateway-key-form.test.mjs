import test from "node:test";
import assert from "node:assert/strict";
import {
  fetchAllModels,
  normalizeModelSelection,
  toggleModelSelection,
} from "../src/gateway-key-form.mjs";

test("model options include every page and deduplicate overlapping upstream catalogs", async () => {
  const rows = Array.from({ length: 735 }, (_, i) => ({ model: `model-${i}` }));
  const pages = [rows.slice(0, 500), [...rows.slice(450, 735)]];
  const requests = [];
  const result = await fetchAllModels(async (path) => {
    requests.push(path);
    return { data: pages.shift() };
  });
  assert.deepEqual(requests, [
    "models?limit=500&offset=0",
    "models?limit=500&offset=500",
  ]);
  assert.equal(result.length, 735);
  assert.equal(result.at(-1).model, "model-734");
});

test("choosing specific models removes the all-model permission and keeps multiple choices", () => {
  let selected = toggleModelSelection(["*"], "gpt-4.1");
  selected = toggleModelSelection(selected, "gpt-4.1-mini");
  assert.deepEqual(selected, ["gpt-4.1", "gpt-4.1-mini"]);
  selected = toggleModelSelection(selected, "gpt-4.1");
  assert.deepEqual(selected, ["gpt-4.1-mini"]);
  assert.deepEqual(toggleModelSelection(selected, "*"), ["*"]);
  assert.deepEqual(toggleModelSelection(["*"], "*"), []);
});

test("saved unavailable model permissions survive reopening the editor", () => {
  assert.deepEqual(
    normalizeModelSelection(["legacy-model", "legacy-model", "gpt-4.1"]),
    ["legacy-model", "gpt-4.1"],
  );
  assert.deepEqual(normalizeModelSelection(["*", "legacy-model"]), ["*"]);
});
