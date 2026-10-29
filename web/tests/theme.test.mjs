import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { resolveTheme } from "../src/theme.mjs";

const bootstrap = readFileSync(
  new URL("../public/theme-init.js", import.meta.url),
  "utf8",
);

function initialTheme(preference, systemDark, storageBlocked = false) {
  const document = { documentElement: { dataset: {} } };
  runInNewContext(bootstrap, {
    document,
    localStorage: {
      getItem(key) {
        assert.equal(key, "gateway-theme");
        if (storageBlocked) throw new Error("Storage unavailable");
        return preference;
      },
    },
    matchMedia(query) {
      assert.equal(query, "(prefers-color-scheme: dark)");
      return { matches: systemDark };
    },
  });
  return document.documentElement.dataset.theme;
}

test("initial paint and runtime agree on system defaults and saved overrides", () => {
  for (const [preference, systemDark, expected] of [
    [null, false, "light"],
    [null, true, "dark"],
    ["system", false, "light"],
    ["system", true, "dark"],
    ["light", true, "light"],
    ["dark", false, "dark"],
    ["invalid-setting", true, "dark"],
  ]) {
    assert.equal(initialTheme(preference, systemDark), expected);
    assert.equal(resolveTheme(preference, systemDark), expected);
  }
});

test("first paint follows the system when browser storage is unavailable", () => {
  assert.equal(initialTheme(null, true, true), "dark");
  assert.equal(initialTheme(null, false, true), "light");
});
