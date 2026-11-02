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

/**
 * @typedef {{id:number, text:string, depth:number, children:ArchiveJSONLine[], close:string|null, foldable:boolean, long:boolean}} ArchiveJSONLine
 */

/** Build a foldable view of formatted JSON without parsing number values.
 * Works with a truncated preview too: unfinished branches retain their text.
 * @param {string} content
 * @returns {ArchiveJSONLine[]}
 */
export function buildArchiveJSONTree(content) {
  /** @type {ArchiveJSONLine[]} */
  const roots = [];
  /** @type {ArchiveJSONLine[]} */
  const stack = [];
  content.split("\n").forEach((text, id) => {
    const trimmed = text.trim();
    if (/^[}\]],?$/.test(trimmed) && stack.length) {
      stack.pop().close = text;
      return;
    }
    const branch = /^(?:"(?:\\.|[^"\\])*"\s*:\s*)?[{\[]$/.test(trimmed);
    const long = !branch && text.length > 300;
    /** @type {ArchiveJSONLine} */
    const node = {
      id,
      text,
      depth: stack.length,
      children: [],
      close: null,
      foldable: branch || long,
      long,
    };
    (stack.length ? stack[stack.length - 1].children : roots).push(node);
    if (branch) stack.push(node);
  });
  return roots;
}

/** @param {ArchiveJSONLine[]} nodes @param {boolean} all */
export function collapsedArchiveLines(nodes, all = false) {
  /** @type {number[]} */
  const ids = [];
  const pending = [...nodes];
  while (pending.length) {
    const node = pending.pop();
    if (node.foldable && (all || node.long || node.depth >= 2))
      ids.push(node.id);
    pending.push(...node.children);
  }
  return ids;
}

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
