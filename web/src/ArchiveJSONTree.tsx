import { useMemo, useState } from "react";
import {
  buildArchiveJSONTree,
  collapsedArchiveLines,
  highlightArchiveJSON,
} from "./archive-json.mjs";

type JSONLine = ReturnType<typeof buildArchiveJSONTree>[number];

function Highlight({
  text,
  limit = Infinity,
}: {
  text: string;
  limit?: number;
}) {
  let remaining = limit;
  const tokens = highlightArchiveJSON(text).flatMap((token) => {
    if (remaining <= 0) return [];
    const visible = { ...token, text: token.text.slice(0, remaining) };
    remaining -= visible.text.length;
    return [visible];
  });
  return (
    <>
      {tokens.map((token, i) => (
        <span key={i} className={`json-${token.kind}`}>
          {token.text}
        </span>
      ))}
    </>
  );
}

export function ArchiveJSONTree({
  content,
  t,
}: {
  content: string;
  t: (key: string) => string;
}) {
  const nodes = useMemo(() => buildArchiveJSONTree(content), [content]);
  const [collapsed, setCollapsed] = useState(
    () => new Set(collapsedArchiveLines(nodes))
  );
  const toggle = (id: number) =>
    setCollapsed((previous) => {
      const next = new Set(previous);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const rows: { node: JSONLine; closing: boolean }[] = [];
  const pending = [...nodes]
    .reverse()
    .map((node) => ({ node, closing: false }));
  while (pending.length) {
    const row = pending.pop()!;
    rows.push(row);
    if (!row.closing && !collapsed.has(row.node.id)) {
      if (row.node.close !== null)
        pending.push({ node: row.node, closing: true });
      for (let i = row.node.children.length - 1; i >= 0; i--)
        pending.push({ node: row.node.children[i], closing: false });
    }
  }
  function renderLine({ node, closing }: { node: JSONLine; closing: boolean }) {
    if (closing)
      return (
        <div
          key={`close-${node.id}`}
          className="archive-tree-line"
          style={{ paddingInlineStart: `${Math.min(node.depth, 12) * 16}px` }}
        >
          <span className="archive-tree-spacer" />
          <code>{node.close!.trimStart()}</code>
        </div>
      );
    const folded = collapsed.has(node.id);
    // Keep indentation in layout rather than duplicated spaces in wrapped text.
    const text = node.text.trimStart();
    const label =
      text.match(/^("(?:\\.|[^"\\])*")\s*:/)?.[1].slice(0, 120) || "JSON";
    return (
      <div
        key={node.id}
        className="archive-tree-line"
        style={{ paddingInlineStart: `${Math.min(node.depth, 12) * 16}px` }}
      >
        {node.foldable ? (
          <button
            type="button"
            className="archive-tree-toggle"
            aria-expanded={!folded}
            aria-label={`${t(
              folded ? "archiveExpand" : "archiveCollapse"
            )} ${label}`}
            onClick={() => toggle(node.id)}
          >
            <span aria-hidden="true">{folded ? "▸" : "▾"}</span>
          </button>
        ) : (
          <span className="archive-tree-spacer" />
        )}
        <code>
          <Highlight text={text} limit={node.long && folded ? 180 : Infinity} />
          {folded && (
            <button
              type="button"
              className="archive-tree-preview"
              onClick={() => toggle(node.id)}
              aria-label={`${t("archiveExpand")} ${label}`}
            >
              {node.long
                ? `… ${t("archiveLongText")} (${text.length.toLocaleString()})`
                : ` … ${node.children.length} ${t("archiveEntries")} ${
                    node.close?.trim() || ""
                  }`}
            </button>
          )}
          {node.long && !folded && (
            <button
              type="button"
              className="archive-tree-preview"
              onClick={() => toggle(node.id)}
            >
              {t("archiveCollapse")}
            </button>
          )}
        </code>
      </div>
    );
  }
  return (
    <>
      <div className="archive-tree-actions">
        <button
          type="button"
          className="secondary"
          onClick={() => setCollapsed(new Set())}
        >
          {t("archiveExpandAll")}
        </button>
        <button
          type="button"
          className="secondary"
          onClick={() =>
            setCollapsed(new Set(collapsedArchiveLines(nodes, true)))
          }
        >
          {t("archiveCollapseAll")}
        </button>
      </div>
      <div
        className="archive-json archive-json-tree"
        aria-label={t("viewArchiveJSON")}
      >
        {rows.map(renderLine)}
      </div>
    </>
  );
}
