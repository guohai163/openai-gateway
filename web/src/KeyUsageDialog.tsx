import { useEffect, useId, useState } from "react";
import { Check, Copy, Eye, EyeOff, X } from "lucide-react";
import { buildCurlExample, exampleModel } from "./key-usage.mjs";

type Props = {
  name: string;
  allowedModels: string[];
  availableModels: string[];
  initialKey?: string;
  showKey?: boolean;
  loadKey: (original?: string) => Promise<{ key: string }>;
  onClose: () => void;
  t: (key: string) => string;
};

export function KeyUsageDialog({
  name,
  allowedModels,
  availableModels,
  initialKey = "",
  showKey = false,
  loadKey,
  onClose,
  t,
}: Props) {
  const [key, setKey] = useState(initialKey);
  const [model, setModel] = useState(() => {
    const permitted = (id: string) =>
      allowedModels.includes("*") || allowedModels.includes(id);
    return (
      (permitted(exampleModel) && availableModels.includes(exampleModel)
        ? exampleModel
        : availableModels.find(permitted)) ||
      allowedModels.find((id) => id !== "*") ||
      exampleModel
    );
  });
  const [copyStatus, setCopyStatus] = useState("");
  const [visible, setVisible] = useState(showKey);
  const [keyLoading, setKeyLoading] = useState(!initialKey);
  const [keyError, setKeyError] = useState("");
  const [legacy, setLegacy] = useState(false);
  const id = useId();
  const permitted =
    allowedModels.includes("*") || allowedModels.includes(model.trim());
  const supported = availableModels.includes(model.trim());
  const options = allowedModels.includes("*") ? availableModels : allowedModels;
  const curl = buildCurlExample(location.origin, model, key);

  useEffect(() => {
    if (initialKey) return;
    let active = true;
    loadKey()
      .then((result) => {
        if (active) {
          setKey(result.key);
          setLegacy(false);
        }
      })
      .catch((error: Error) => {
        if (active) {
          setLegacy(error.message === "key_not_recoverable");
          setKeyError(error.message);
        }
      })
      .finally(() => {
        if (active) setKeyLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", escape);
    return () => document.removeEventListener("keydown", escape);
  }, [onClose]);

  return (
    <div className="modal-overlay">
      <section
        className="modal usage-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${id}-title`}
      >
        <div className="modal-heading">
          <div>
            <span className="eyebrow">{name}</span>
            <h2 id={`${id}-title`}>{t("howToUse")}</h2>
          </div>
          <button type="button" aria-label={t("close")} onClick={onClose}>
            <X size={20} />
          </button>
        </div>
        <div className="usage-body">
          <p>{t("usageIntro")}</p>
          <div className="usage-fields">
            <label>
              {t("usagePrivateKey")}
              <div className="key-reveal-field">
                <input
                  type={visible ? "text" : "password"}
                  value={key}
                  placeholder={keyLoading ? t("loading") : "gk-ai-..."}
                  disabled={keyLoading}
                  autoFocus={!initialKey}
                  autoComplete="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  onChange={(event) => {
                    setKey(event.target.value);
                    setCopyStatus("");
                  }}
                />
                <button
                  type="button"
                  title={t(visible ? "hideKey" : "revealKey")}
                  aria-label={t(visible ? "hideKey" : "revealKey")}
                  onClick={() => setVisible(!visible)}
                >
                  {visible ? <EyeOff size={17} /> : <Eye size={17} />}
                </button>
                <button
                  type="button"
                  disabled={!key.trim()}
                  title={t("copy")}
                  aria-label={t("copy")}
                  onClick={async () => {
                    try {
                      await navigator.clipboard.writeText(key);
                      setCopyStatus("key");
                    } catch {
                      setCopyStatus("error");
                    }
                  }}
                >
                  <Copy size={17} />
                </button>
              </div>
              <small>{t("usageKeyHint")}</small>
              {keyError && (
                <small className={legacy ? "" : "error-hint"} role="status">
                  {t(keyError)}
                </small>
              )}
              {legacy && (
                <button
                  type="button"
                  className="secondary"
                  disabled={!key.trim() || keyLoading}
                  onClick={async () => {
                    setKeyLoading(true);
                    setKeyError("");
                    try {
                      const result = await loadKey(key.trim());
                      setKey(result.key);
                      setLegacy(false);
                    } catch (error) {
                      setKeyError(
                        error instanceof Error
                          ? error.message
                          : "unexpected_error",
                      );
                    } finally {
                      setKeyLoading(false);
                    }
                  }}
                >
                  {t("rememberOriginalKey")}
                </button>
              )}
            </label>
            <label>
              {t("model")}
              <input
                value={model}
                list={`${id}-models`}
                autoFocus={!!initialKey}
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => {
                  setModel(event.target.value);
                  setCopyStatus("");
                }}
              />
              <datalist id={`${id}-models`}>
                {[...new Set(options)]
                  .filter((value) => value !== "*")
                  .map((value) => (
                    <option key={value} value={value} />
                  ))}
              </datalist>
            </label>
          </div>
          {model.trim() && (!permitted || !supported) && (
            <p className="usage-model-hint" role="status">
              {t(!permitted ? "usageModelNotAllowed" : "usageModelNotSynced")}
            </p>
          )}
          {!key.trim() && (
            <p className="usage-env-hint">{t("usageEnvironmentHint")}</p>
          )}
          <pre className="usage-code">
            <code>{curl}</code>
          </pre>
          {copyStatus === "error" && (
            <p className="error-hint" role="alert">
              {t("copyCommandFailed")}
            </p>
          )}
          {["success", "key"].includes(copyStatus) && (
            <p className="usage-copy-status" role="status">
              {t("copied")}
            </p>
          )}
        </div>
        <div className="modal-footer">
          <button type="button" className="secondary" onClick={onClose}>
            {t("close")}
          </button>
          <button
            type="button"
            className="primary"
            disabled={!model.trim()}
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(curl);
                setCopyStatus("success");
              } catch {
                setCopyStatus("error");
              }
            }}
          >
            {copyStatus === "success" ? (
              <Check size={16} />
            ) : (
              <Copy size={16} />
            )}
            {t(copyStatus === "success" ? "copied" : "copyCommand")}
          </button>
        </div>
      </section>
    </div>
  );
}
