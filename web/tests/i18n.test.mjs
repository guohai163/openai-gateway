import test from "node:test";
import assert from "node:assert/strict";
import { resolveLanguage, messages } from "../src/i18n.mjs";
test("browser detection and explicit preference", () => {
  assert.equal(resolveLanguage("auto", ["zh-TW", "en-US"]), "zh");
  assert.equal(resolveLanguage("auto", ["en-US", "zh-CN"]), "en");
  assert.equal(resolveLanguage("en", ["zh-CN"]), "en");
  assert.equal(resolveLanguage("zh", ["en-US"]), "zh");
  assert.equal(resolveLanguage("auto", ["fr-FR"]), "en");
});
test("both languages cover the same UI and validation messages", () => {
  assert.deepEqual(
    Object.keys(messages.zh).sort(),
    Object.keys(messages.en).sort(),
  );
  for (const dict of Object.values(messages))
    for (const value of Object.values(dict)) assert.ok(value.length > 0);
});
