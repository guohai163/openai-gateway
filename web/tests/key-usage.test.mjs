import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { buildCurlExample, exampleModel } from "../src/key-usage.mjs";

function curlArguments(command) {
  // Capture curl's arguments locally; never make a network request.
  const shim = 'curl() { printf "%s\\0" "$@"; }\n';
  return execFileSync("sh", ["-c", shim + command], {
    encoding: "utf8",
    env: { ...process.env, GATEWAY_API_KEY: "gk-ai-test-from-environment" },
  })
    .split("\0")
    .slice(0, -1);
}

test("copyable example uses the current gateway, requested model and hi input", () => {
  const args = curlArguments(
    buildCurlExample("https://gyai.dev", undefined, "gk-ai-example-key"),
  );
  assert.equal(exampleModel, "gpt-6-luna");
  assert.ok(args.includes("https://gyai.dev/v1/responses"));
  assert.ok(args.includes("Authorization: Bearer gk-ai-example-key"));
  assert.ok(args.includes("Content-Type: application/json"));
  assert.deepEqual(JSON.parse(args[args.indexOf("--data-raw") + 1]), {
    model: "gpt-6-luna",
    input: "hi",
  });
});

test("saved-key help supports an environment variable without inventing a secret", () => {
  const command = buildCurlExample("https://gateway.example/", "gpt-4.1");
  assert.ok(command.includes("$GATEWAY_API_KEY"));
  const args = curlArguments(command);
  assert.ok(args.includes("Authorization: Bearer gk-ai-test-from-environment"));
  assert.ok(args.includes("https://gateway.example/v1/responses"));
});

test("quotes and shell expressions in fields remain literal curl arguments", () => {
  const model = "model'$(printf unexpected)";
  const key = "gk-ai-test'$(printf unexpected)";
  const args = curlArguments(
    buildCurlExample("https://gateway.example", model, key),
  );
  assert.ok(args.includes(`Authorization: Bearer ${key}`));
  assert.deepEqual(JSON.parse(args[args.indexOf("--data-raw") + 1]), {
    model,
    input: "hi",
  });
});
