// Format token text without converting JSON numbers into JavaScript numbers.
/** @param {string} content */
export function formatArchiveJSON(content) {
  JSON.parse(content);
  const tokens =
    content.match(
      /"(?:\\[\s\S]|[^"\\])*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null|[{}\[\],:]/g
    ) || [];
  let depth = 0;
  const output = [];
  const newline = () => output.push("\n", "  ".repeat(depth));
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i];
    if (token === "{" || token === "[") {
      output.push(token);
      depth++;
      if (tokens[i + 1] !== "}" && tokens[i + 1] !== "]") newline();
    } else if (token === "}" || token === "]") {
      depth--;
      if (tokens[i - 1] !== "{" && tokens[i - 1] !== "[") newline();
      output.push(token);
    } else if (token === ",") {
      output.push(token);
      newline();
    } else if (token === ":") {
      output.push(": ");
    } else {
      output.push(token);
    }
  }
  return output.join("");
}

export const archivePreviewLimit = 256 * 1024;

/** @param {string} content */
export function highlightArchiveJSON(content) {
  return content
    .split(
      /("(?:\\[\s\S]|[^"\\])*"\s*:|"(?:\\[\s\S]|[^"\\])*"|\btrue\b|\bfalse\b|\bnull\b|-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g
    )
    .filter(Boolean)
    .map((text) => ({
      text,
      kind: /^"/.test(text)
        ? /:$/.test(text)
          ? "key"
          : "string"
        : /^(true|false)$/.test(text)
        ? "boolean"
        : text === "null"
        ? "null"
        : /^-?\d/.test(text)
        ? "number"
        : "plain",
    }));
}
