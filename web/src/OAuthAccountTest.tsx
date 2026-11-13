import { useEffect, useId, useRef, useState } from "react";
import { Play, X } from "lucide-react";

type Props = {
  account: { id: string; name: string };
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
  onUnauthorized: () => void;
  onClose: () => void;
};
type Model = { slug: string; display_name: string };
type Result = {
  model: string;
  prompt: string;
  output: string;
  duration_ms: number;
  http_status: number;
  request_id: string;
  error: string;
  error_code: string;
  error_param: string;
  detail: string;
};

export function OAuthAccountTest({
  account,
  api,
  t,
  onUnauthorized,
  onClose,
}: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const controller = useRef<AbortController | null>(null);
  const active = useRef(true);
  const [models, setModels] = useState<Model[]>([]);
  const [model, setModel] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Result | null>(null);
  const titleId = useId();

  useEffect(() => {
    active.current = true;
    const abort = new AbortController();
    dialog.current?.showModal();
    void api(`oauth_accounts/${encodeURIComponent(account.id)}/models`, {
      signal: abort.signal,
    })
      .then((catalog) => {
        if (!active.current) return;
        setModels(catalog.models);
        setModel(catalog.models[0]?.slug || "");
        setError(catalog.error || "");
      })
      .catch((e: Error) => {
        if (!active.current) return;
        setError(e.message);
        if (e.message === "unauthorized") onUnauthorized();
      })
      .finally(() => {
        if (active.current) setLoading(false);
      });
    return () => {
      active.current = false;
      abort.abort();
      controller.current?.abort();
    };
  }, [account.id]);

  async function run() {
    if (busy || !models.some((entry) => entry.slug === model)) return;
    setBusy(true);
    setError("");
    setResult(null);
    const abort = new AbortController();
    controller.current = abort;
    try {
      const response = await api(
        `oauth_accounts/${encodeURIComponent(account.id)}/test`,
        {
          method: "POST",
          body: JSON.stringify({ model }),
          signal: abort.signal,
        },
      );
      if (active.current) setResult(response);
    } catch (e) {
      if (!active.current) return;
      const code = e instanceof Error ? e.message : "unexpected_error";
      setError(code);
      if (code === "unauthorized") onUnauthorized();
    } finally {
      if (active.current) setBusy(false);
    }
  }

  return (
    <dialog
      ref={dialog}
      className="modal oauth-test-modal"
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
    >
      <div className="modal-heading">
        <h2 id={titleId}>
          {account.name} · {t("oauthTest")}
        </h2>
        <button type="button" onClick={onClose} aria-label={t("close")}>
          <X size={18} />
        </button>
      </div>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void run();
        }}
      >
        <p className="muted">{t("oauthTestHint")}</p>
        <label>
          {t("model")}
          <select
            value={model}
            onChange={(event) => {
              setModel(event.target.value);
              setResult(null);
            }}
            disabled={loading || busy || !models.length}
            required
            autoFocus
          >
            {!models.length && (
              <option value="">
                {t(loading ? "loading" : "oauthNoModels")}
              </option>
            )}
            {models.map((entry) => (
              <option key={entry.slug} value={entry.slug}>
                {entry.display_name} ({entry.slug})
              </option>
            ))}
          </select>
        </label>
        <div className="oauth-test-prompt">
          <span className="muted">{t("oauthTestPrompt")}</span>
          <code>Hello word!</code>
        </div>
        {error && (
          <p className="alert" role="alert">
            {t(error)}
          </p>
        )}
        <div aria-live="polite" aria-busy={busy}>
          {busy && <p className="muted">{t("oauthTesting")}</p>}
          {result && (
            <div className="oauth-test-result">
              <strong
                className={result.error ? "oauth-error" : "oauth-test-success"}
              >
                {t(result.error || "oauthTestSuccess")}
              </strong>
              <small className="muted">
                {result.model} · {result.duration_ms} ms
                {result.http_status ? ` · HTTP ${result.http_status}` : ""}
              </small>
              {result.error_code && (
                <code>
                  {result.error_code}
                  {result.error_param ? ` · ${result.error_param}` : ""}
                </code>
              )}
              {result.detail && <p>{result.detail}</p>}
              {result.request_id && (
                <small className="muted">Request ID: {result.request_id}</small>
              )}
              {result.output && (
                <>
                  <span className="muted">
                    {t(
                      result.error
                        ? "oauthTestPartialOutput"
                        : "oauthTestOutput",
                    )}
                  </span>
                  <pre>{result.output}</pre>
                </>
              )}
              {!result.error && !result.output && (
                <p className="muted">{t("oauthTestNoText")}</p>
              )}
            </div>
          )}
        </div>
        <div className="modal-footer">
          <button type="button" className="secondary" onClick={onClose}>
            {t("close")}
          </button>
          <button
            type="submit"
            className="primary"
            disabled={loading || busy || !model}
          >
            <Play size={14} />
            {t(busy ? "oauthTesting" : "oauthTestSend")}
          </button>
        </div>
      </form>
    </dialog>
  );
}
