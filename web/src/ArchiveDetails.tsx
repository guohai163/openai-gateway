import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Copy, FileJson, X } from "lucide-react";
import { archivePreviewLimit, formatArchiveJSON } from "./archive-json.mjs";
import { ArchiveJSONTree } from "./ArchiveJSONTree";

type Props = {
  row: Record<string, any>;
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
};

export function ArchiveDetails({ row, api, t }: Props) {
  const [direction, setDirection] = useState<"input" | "output" | null>(null);
  return (
    <div className="archive-status">
      {(["input", "output"] as const).map((side) => {
        const label = `${t(
          side === "input" ? "archiveInput" : "archiveOutput"
        )}: ${t("archive_" + (row[`${side}_status`] || "disabled"))}`;
        const path = row[`${side}_path`];
        return (
          <div key={side}>
            {row[`${side}_status`] === "stored" && path ? (
              <button
                type="button"
                className="text-button archive-open"
                onClick={() => setDirection(side)}
                aria-label={`${t("viewArchiveJSON")} · ${label}`}
              >
                <FileJson size={13} /> {label}
              </button>
            ) : (
              <small>{label}</small>
            )}
            {path && <code title={path}>{path}</code>}
          </div>
        );
      })}
      {direction &&
        createPortal(
          <ArchiveJSONDialog
            key={`${row.id}-${direction}`}
            row={row}
            direction={direction}
            api={api}
            t={t}
            onClose={() => setDirection(null)}
          />,
          document.body
        )}
    </div>
  );
}

function ArchiveJSONDialog({
  row,
  direction,
  api,
  t,
  onClose,
}: Props & { direction: "input" | "output"; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const [content, setContent] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [copyStatus, setCopyStatus] = useState("");

  useEffect(() => {
    const el = dialog.current!;
    el.showModal();
    return () => el.close();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setContent(null);
    setError("");
    setCopyStatus("");
    api(`logs/${encodeURIComponent(row.id)}/archive/${direction}`, {
      signal: controller.signal,
    })
      .then((result) => {
        if (!controller.signal.aborted)
          setContent(formatArchiveJSON(result.content));
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted)
          setError(
            e instanceof SyntaxError
              ? "archive_invalid_json"
              : e instanceof Error
              ? e.message
              : "archive_read_failed"
          );
      });
    return () => controller.abort();
  }, [row.id, direction, api, attempt]);

  return (
    <dialog
      ref={dialog}
      className="archive-dialog"
      aria-labelledby={titleId}
      onCancel={onClose}
    >
      <div className="modal-heading">
        <div>
          <span className="eyebrow">
            {t(direction === "input" ? "archiveInput" : "archiveOutput")}
          </span>
          <h2 id={titleId}>{t("viewArchiveJSON")}</h2>
        </div>
        <button
          type="button"
          aria-label={t("close")}
          onClick={onClose}
          autoFocus
        >
          <X size={20} />
        </button>
      </div>
      <div className="archive-view-meta">
        <code>{row[`${direction}_path`]}</code>
        <small>
          {t("request_uuid")}: {row.request_uuid}
        </small>
      </div>
      <div className="archive-view-toolbar">
        <span className="muted">JSON</span>
        <div className="archive-copy-controls">
          <span role="status">{copyStatus && t(copyStatus)}</span>
          <button
            type="button"
            className="secondary"
            disabled={content === null}
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(content!);
                setCopyStatus("copied");
              } catch {
                setCopyStatus("archiveCopyFailed");
              }
            }}
          >
            <Copy size={14} />
            {t("copyJSON")}
          </button>
        </div>
      </div>
      {error ? (
        <div className="archive-view-state">
          <p className="error-hint" role="alert">
            {t(error)}
          </p>
          <button
            type="button"
            className="secondary"
            onClick={() => setAttempt(attempt + 1)}
          >
            {t("archiveRetry")}
          </button>
        </div>
      ) : content === null ? (
        <p className="archive-view-state" role="status">
          {t("loading")}
        </p>
      ) : (
        <>
          {content.length > archivePreviewLimit && (
            <p className="archive-preview-hint">
              {t("archivePreviewTruncated")}
            </p>
          )}
          <ArchiveJSONTree
            content={content.slice(0, archivePreviewLimit)}
            t={t}
          />
        </>
      )}
    </dialog>
  );
}
