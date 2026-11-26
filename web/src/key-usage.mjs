export const exampleModel = "gpt-6-luna";

function shellQuote(value) {
  return "'" + String(value).replaceAll("'", "'\\''") + "'";
}

export function buildCurlExample(origin, model = exampleModel, key = "") {
  const id = model.trim();
  const gemini = /^(?:models\/|gemini-)/.test(id);
  const path = gemini
    ? `/v1beta/models/${encodeURIComponent(id.replace(/^models\//, ""))}:generateContent`
    : "/v1/responses";
  const endpoint = origin.replace(/\/$/, "") + path;
  const header = gemini ? "X-Goog-Api-Key: " : "Authorization: Bearer ";
  const authorization = key.trim()
    ? shellQuote(header + key.trim())
    : `"${header}$GATEWAY_API_KEY"`;
  const payload = gemini
    ? { contents: [{ parts: [{ text: "hi" }] }] }
    : { model: id, input: "hi" };
  const body = JSON.stringify(payload, null, 2);
  return [
    `curl --request POST ${shellQuote(endpoint)} \\`,
    `  --header ${authorization} \\`,
    "  --header 'Content-Type: application/json' \\",
    `  --data-raw ${shellQuote(body)}`,
  ].join("\n");
}
