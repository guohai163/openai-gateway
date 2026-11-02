import test from "node:test";
import assert from "node:assert/strict";
import {
  formatArchiveJSON,
  highlightArchiveJSON,
  buildArchiveJSONTree,
  collapsedArchiveLines,
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

function restoreTree(nodes) {
  const lines = [];
  const visit = (node) => {
    lines.push(node.text);
    node.children.forEach(visit);
    if (node.close !== null) lines.push(node.close);
  };
  nodes.forEach(visit);
  return lines.join("\n");
}

test("folding preserves nested arrays, duplicate keys, escaped braces and exact numeric text", () => {
  const formatted = formatArchiveJSON(
    '{"messages":[{"content":"not a bracket: } { [ ] \\""}],"id":9007199254740993123,"id":-0,"empty":{}}'
  );
  const nodes = buildArchiveJSONTree(formatted);
  assert.equal(restoreTree(nodes), formatted);
  const root = nodes[0];
  assert.equal(root.children.length, 4);
  const array = root.children[0];
  assert.equal(array.children.length, 1);
  assert.equal(array.children[0].children[0].foldable, false);
  assert.equal(root.children[3].foldable, false);
  const initial = collapsedArchiveLines(nodes);
  assert.ok(!initial.includes(root.id));
  assert.ok(!initial.includes(array.id));
  assert.ok(initial.includes(array.children[0].id));
  assert.ok(collapsedArchiveLines(nodes, true).includes(root.id));
});

test("long content starts folded and retains its complete string for expansion", () => {
  const text = 'line with \\n and <script> and quotes " '.repeat(50);
  const formatted = formatArchiveJSON(JSON.stringify({ content: text }));
  const nodes = buildArchiveJSONTree(formatted);
  const content = nodes[0].children[0];
  assert.equal(content.long, true);
  assert.ok(collapsedArchiveLines(nodes).includes(content.id));
  assert.equal(restoreTree(nodes), formatted);
});

test("truncated previews do not fabricate closing brackets or parse partial strings", () => {
  for (const partial of [
    '{\n  "messages": [\n    {\n      "text": "unfinished {',
    '{\n  "list": [\n    123',
    '"very long string' + "x".repeat(400),
  ]) {
    const nodes = buildArchiveJSONTree(partial);
    assert.equal(restoreTree(nodes), partial);
    assert.equal(nodes[0].close, null);
  }
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
