import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { formatDateTime } from "../src/date-time.mjs";

test("timestamps include padded date, 24-hour time and seconds", () => {
  assert.equal(
    formatDateTime(new Date(2026, 0, 2, 15, 4, 5)),
    "2026-01-02 15:04:05",
  );
  assert.equal(
    formatDateTime(new Date(2026, 10, 5, 0, 0, 0)),
    "2026-11-05 00:00:00",
  );
});

test("timestamps preserve the browser's local time zone across date boundaries", () => {
  const moduleURL = new URL("../src/date-time.mjs", import.meta.url).href;
  const script = `import { formatDateTime } from ${JSON.stringify(moduleURL)};
    process.stdout.write(formatDateTime("2026-11-04T16:04:05Z"));`;
  const output = execFileSync(process.execPath, ["--input-type=module", "-e", script], {
    env: { ...process.env, TZ: "Asia/Shanghai", LANG: "en_US.UTF-8" },
    encoding: "utf8",
  });
  assert.equal(output, "2026-11-05 00:04:05");
});

test("missing and invalid timestamps use a placeholder", () => {
  for (const value of [null, undefined, "", "invalid"]) {
    assert.equal(formatDateTime(value), "—");
  }
  assert.notEqual(formatDateTime(0), "—");
});
