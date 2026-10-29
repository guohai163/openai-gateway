export const exampleModel = "gpt-6.1-lune";

function shellQuote(value) {
  return "'" + String(value).replaceAll("'", "'\\''") + "'";
}

export function buildCurlExample(origin, model = exampleModel, key = "") {
  const endpoint = `${origin.replace(/\/$/, "")}/v1/responses`;
  const authorization = key.trim()
    ? shellQuote(`Authorization: Bearer ${key.trim()}`)
    : '"Authorization: Bearer $GATEWAY_API_KEY"';
  const body = JSON.stringify({ model: model.trim(), input: "hi" }, null, 2);
  return [
    `curl --request POST ${shellQuote(endpoint)} \\`,
    `  --header ${authorization} \\`,
    "  --header 'Content-Type: application/json' \\",
    `  --data-raw ${shellQuote(body)}`,
  ].join("\n");
}
