import test from "node:test";
import assert from "node:assert/strict";
import {
  quotaCountdown,
  quotaPercent,
  quotaWindowState,
} from "../src/oauth-usage.mjs";

test("quota windows preserve remaining 0%, missing data and expired snapshots", () => {
  const now = Date.parse("2026-11-12T10:00:00Z");
  assert.equal(quotaWindowState(null, now), null);
  assert.equal(quotaWindowState({}, now), null);
  assert.equal(quotaWindowState({ remaining_percent: -1 }, now), null);
  assert.equal(quotaWindowState({ remaining_percent: NaN }, now), null);
  assert.equal(quotaWindowState({ remaining_percent: 101 }, now), null);
  assert.deepEqual(
    quotaWindowState({ remaining_percent: 0, resets_at: null }, now),
    { remaining: 0, expired: false, reset: null },
  );
  assert.equal(
    quotaWindowState(
      { remaining_percent: 94, resets_at: "2026-11-12T09:59:00Z" },
      now,
    ).expired,
    true,
  );
  assert.equal(
    quotaWindowState({ remaining_percent: 94, resets_at: "not a date" }, now)
      .reset,
    null,
  );
});

test("countdowns show the upstream reset interval without restarting expired windows", () => {
  const now = 1800000000000;
  assert.equal(quotaCountdown(now + (6 * 24 + 15) * 3600000, now), "6d 15h");
  assert.equal(quotaCountdown(now + (4 * 60 + 21) * 60000, now), "4h 21m");
  assert.equal(quotaCountdown(now + 1000, now), "1m");
  assert.equal(quotaCountdown(now, now), "");
  assert.equal(quotaCountdown(now - 1, now), "");
  assert.equal(quotaCountdown(null, now), "—");
});

test("remaining percentages retain exhausted and nearly exhausted values", () => {
  assert.equal(quotaPercent(94, "en-US"), "94%");
  assert.equal(quotaPercent(0, "zh-CN"), "0%");
  assert.equal(quotaPercent(0.4, "en-US"), "0.4%");
  assert.equal(quotaPercent(100, "en-US"), "100%");
  assert.equal(quotaPercent(99.6, "en-US"), "99.6%");
});
