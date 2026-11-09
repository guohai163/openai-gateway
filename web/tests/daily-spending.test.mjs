import test from "node:test";
import assert from "node:assert/strict";
import { dailySpendingSeries, dailyAxisMax } from "../src/daily-spending.mjs";

test("daily lines separate tokens and currencies, preserving unknown costs and empty days", () => {
  const days = [
    {
      date: "2026-11-01",
      input_tokens: 100,
      output_tokens: 20,
      totals_by_currency: { USD: "0.003", CNY: "0.02" },
    },
    {
      date: "2026-11-02",
      input_tokens: 30,
      output_tokens: 5,
      totals_by_currency: { USD: null },
    },
    {
      date: "2026-11-03",
      input_tokens: 0,
      output_tokens: 0,
      totals_by_currency: {},
    },
  ];
  const lines = dailySpendingSeries(days);
  const values = Object.fromEntries(
    lines.map((line) => [line.id, line.values]),
  );
  assert.deepEqual(values.tokens, [120, 35, 0]);
  assert.deepEqual(values.input_tokens, [100, 30, 0]);
  assert.deepEqual(values.output_tokens, [20, 5, 0]);
  assert.deepEqual(values["cost:USD"], [0.003, null, 0]);
  assert.deepEqual(values["cost:CNY"], [0.02, 0, 0]);
  assert.equal(lines.find((line) => line.id === "cost:USD").axis, "USD");
  assert.equal(lines.find((line) => line.id === "cost:CNY").axis, "CNY");
});

test("unknown-only currencies remain selectable, with no invented default currency", () => {
  const lines = dailySpendingSeries([
    { input_tokens: 0, output_tokens: 0, totals_by_currency: { CNY: null } },
  ]);
  assert.deepEqual(lines.find((line) => line.id === "cost:CNY").values, [null]);
  assert.ok(!lines.some((line) => line.id === "cost:USD"));
  assert.equal(dailySpendingSeries([]).length, 3);
});

test("axis scales handle tiny costs, large token counts and hidden or unknown values", () => {
  for (const values of [
    [],
    [0, 0],
    [null],
    [0.00000001, 0.00000002],
    [3, 9],
    [42_590_592, 3_211_964],
  ]) {
    const max = dailyAxisMax(values);
    assert.ok(Number.isFinite(max) && max > 0);
    assert.ok(max >= Math.max(0, ...values.filter((value) => value != null)));
  }
  assert.equal(dailyAxisMax([3, 9]), 10);
});
