import { useEffect, useId, useRef, useState } from "react";
import {
  availableLogColumns,
  defaultLogColumns,
  normalizeLogColumnPreference,
} from "./log-columns.mjs";

function readPreference(key: string, admin: boolean) {
  try {
    return normalizeLogColumnPreference(
      JSON.parse(localStorage.getItem(key) || "null"),
      admin
    );
  } catch {
    return null;
  }
}

export function useLogColumns(
  admin: boolean,
  userID: string,
  enabled: boolean
) {
  const storageKey = `openai-log-columns:v1:${userID}:${
    admin ? "admin" : "user"
  }`;
  const [saved, setSaved] = useState<{ key: string; columns: string[] | null }>(
    () => ({ key: storageKey, columns: readPreference(storageKey, admin) })
  );
  const [width, setWidth] = useState(320);
  const containerRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    setSaved({ key: storageKey, columns: readPreference(storageKey, admin) });
  }, [storageKey, admin]);
  useEffect(() => {
    if (!enabled || !containerRef.current) return;
    const element = containerRef.current;
    const measure = () =>
      setWidth(Math.floor(element.getBoundingClientRect().width));
    measure();
    if (typeof ResizeObserver !== "undefined") {
      const observer = new ResizeObserver(measure);
      observer.observe(element);
      return () => observer.disconnect();
    }
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, [enabled]);
  const preference =
    saved.key === storageKey
      ? normalizeLogColumnPreference(saved.columns, admin)
      : null;
  const columns = preference || defaultLogColumns(width, admin);
  const onChange = (next: string[] | null) => {
    const normalized = normalizeLogColumnPreference(next, admin);
    setSaved({ key: storageKey, columns: normalized });
    try {
      localStorage.setItem(storageKey, JSON.stringify(normalized));
    } catch {}
  };
  return { columns, automatic: preference === null, onChange, containerRef };
}

export function LogColumnPicker({
  columns,
  automatic,
  admin,
  onChange,
  t,
}: {
  columns: string[];
  automatic: boolean;
  admin: boolean;
  onChange: (columns: string[] | null) => void;
  t: (key: string) => string;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const title = useId();
  return (
    <>
      <button
        type="button"
        className="secondary log-columns-button"
        onClick={() => dialog.current?.showModal()}
      >
        {t("logDisplayColumns")} ({columns.length})
      </button>
      <dialog
        className="log-columns-dialog"
        ref={dialog}
        aria-labelledby={title}
      >
        <div className="modal-heading">
          <h2 id={title}>{t("logDisplayColumns")}</h2>
          <button
            type="button"
            aria-label={t("close")}
            onClick={() => dialog.current?.close()}
          >
            ×
          </button>
        </div>
        <div className="log-columns-body">
          <p>{t(automatic ? "logColumnsAutomatic" : "logColumnsManual")}</p>
          <div className="log-columns-grid">
            {availableLogColumns(admin).map((column) => (
              <label key={column}>
                <input
                  type="checkbox"
                  checked={columns.includes(column)}
                  disabled={columns.length === 1 && columns.includes(column)}
                  onChange={(event) =>
                    onChange(
                      event.target.checked
                        ? [...columns, column]
                        : columns.filter((item) => item !== column)
                    )
                  }
                />
                {t(column)}
              </label>
            ))}
          </div>
          <small>{t("logColumnsHint")}</small>
        </div>
        <div className="modal-footer">
          <button
            type="button"
            className="secondary"
            onClick={() => onChange(null)}
          >
            {t("logColumnsReset")}
          </button>
          <button
            type="button"
            className="primary"
            onClick={() => dialog.current?.close()}
          >
            {t("close")}
          </button>
        </div>
      </dialog>
    </>
  );
}
