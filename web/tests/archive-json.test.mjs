import test from "node:test";
import assert from "node:assert/strict";
import {
  formatArchiveJSON,
  highlightArchiveJSON,
} from "../src/archive-json.mjs";

test("archive formatting preserves exact numbers, string escapes and nested data", () => {
  const source =
    '{"id":9007199254740993123,"negative":-0,"exp":1.234e+90,"text":"escaped \\" quote, { } : \\n","empty":{},"list":[true,false,null,[]]}';
  const formatted = formatArchiveJSON(source);
  assert.ok(formatted.includes('"id": 9007199254740993123'));
  assert.ok(formatted.includes('"negative": -0'));
  assert.ok(formatted.includes('"exp": 1.234e+90'));
  assert.ok(formatted.includes('"empty": {}'));
  assert.deepEqual(JSON.parse(formatted), JSON.parse(source));
  assert.equal(
    highlightArchiveJSON(formatted)
      .map((t) => t.text)
      .join(""),
    formatted
  );
});

test("JSON highlighting treats HTML and script content as string text", () => {
  const source = formatArchiveJSON(
    '{"message":"<script>alert(1)</script>","enabled":true,"value":null,"count":-2.5}'
  );
  const tokens = highlightArchiveJSON(source);
  assert.ok(
    tokens.some((t) => t.kind === "string" && t.text.includes("<script>"))
  );
  assert.ok(tokens.some((t) => t.kind === "key" && t.text === '"message":'));
  assert.ok(tokens.some((t) => t.kind === "boolean" && t.text === "true"));
  assert.ok(tokens.some((t) => t.kind === "null" && t.text === "null"));
  assert.ok(tokens.some((t) => t.kind === "number" && t.text === "-2.5"));
});

test("scalar archives are valid and malformed objects are rejected", () => {
  for (const source of ["null", "[]", "{}", '"hello"', "12", "false"]) {
    assert.equal(formatArchiveJSON(source), source);
  }
  assert.throws(() => formatArchiveJSON('{"incomplete":'), SyntaxError);
});
