import test from "node:test";
import assert from "node:assert/strict";
import {
  availableLogColumns,
  defaultLogColumns,
  normalizeLogColumnPreference,
} from "../src/log-columns.mjs";

test("automatic layout progressively adds main columns using the container width", () => {
  assert.deepEqual(defaultLogColumns(320, true), [
    "created_at",
    "model",
    "status",
  ]);
  assert.equal(defaultLogColumns(519, true).length, 3);
  assert.equal(defaultLogColumns(520, true).length, 4);
  assert.equal(defaultLogColumns(760, true).length, 5);
  assert.equal(defaultLogColumns(1000, true).length, 6);
  assert.equal(defaultLogColumns(1200, true).length, 8);
  for (const width of [320, 640, 900, 1200, 2000]) {
    assert.ok(defaultLogColumns(width, true).includes("status"));
    assert.ok(!defaultLogColumns(width, true).includes("request_uuid"));
  }
});

test("ordinary users cannot select admin columns including saved preferences", () => {
  for (const column of ["owner_email", "upstream_name", "archive_status"]) {
    assert.ok(!availableLogColumns(false).includes(column));
    assert.ok(!defaultLogColumns(2000, false).includes(column));
    assert.equal(normalizeLogColumnPreference([column], false), null);
  }
  assert.deepEqual(
    normalizeLogColumnPreference(["model", "archive_status", "status"], false),
    ["model", "status"]
  );
});

test("custom choices stay independent of screen size and tolerate stale preferences", () => {
  const saved = ["error_code", "model", "error_code", "removed-column"];
  assert.deepEqual(normalizeLogColumnPreference(saved, true), [
    "model",
    "error_code",
  ]);
  for (const invalid of [null, [], {}, "model", ["removed-column"]])
    assert.equal(normalizeLogColumnPreference(invalid, true), null);
  assert.deepEqual(normalizeLogColumnPreference(["request_uuid"], true), [
    "request_uuid",
  ]);
});
