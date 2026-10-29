import { useEffect, useId, useState } from "react";
import { Check, Copy, X } from "lucide-react";
import { buildCurlExample, exampleModel } from "./key-usage.mjs";

type Props = {
  name: string;
  allowedModels: string[];
  availableModels: string[];
  initialKey?: string;
  onClose: () => void;
  t: (key: string) => string;
};

export function KeyUsageDialog({
  name,
  allowedModels,
  availableModels,
  initialKey = "",
  onClose,
  t,
}: Props) {
  const [key, setKey] = useState(initialKey);
  const [model, setModel] = useState(exampleModel);
  const [copyStatus, setCopyStatus] = useState("");
  const id = useId();
  const permitted =
    allowedModels.includes("*") || allowedModels.includes(model.trim());
  const supported = availableModels.includes(model.trim());
  const options = allowedModels.includes("*") ? availableModels : allowedModels;
  const curl = buildCurlExample(location.origin, model, key);

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
              <input
                type="password"
                value={key}
                placeholder="gk-ai-..."
                autoFocus={!initialKey}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                onChange={(event) => {
                  setKey(event.target.value);
                  setCopyStatus("");
                }}
              />
              <small>{t("usageKeyHint")}</small>
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
          {copyStatus === "success" && (
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
