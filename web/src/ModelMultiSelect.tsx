import { useEffect, useId, useRef, useState } from "react";
import { ChevronDown, Search } from "lucide-react";
import {
  normalizeModelSelection,
  toggleModelSelection,
} from "./gateway-key-form.mjs";

type Props = {
  models: string[];
  defaultValue: string[];
  t: (key: string) => string;
};

export function ModelMultiSelect({ models, defaultValue, t }: Props) {
  const [selected, setSelected] = useState<string[]>(() =>
    normalizeModelSelection(defaultValue),
  );
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const id = useId();
  const available = new Set(models);
  const options = [
    ...new Set([...models, ...defaultValue.filter((model) => model !== "*")]),
  ]
    .sort((a, b) => a.localeCompare(b))
    .filter((model) =>
      model.toLowerCase().includes(query.trim().toLowerCase()),
    );

  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setOpen(false);
        trigger.current?.focus();
      }
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", escape);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("keydown", escape);
    };
  }, [open]);

  const summary = selected.includes("*")
    ? t("allModels")
    : selected.length
      ? t("selectedModels").replace("{count}", String(selected.length))
      : t("selectModels");

  return (
    <div className="model-multiselect" ref={root}>
      <span className="field-label" id={`${id}-label`}>
        {t("models")}
      </span>
      {selected.map((model) => (
        <input key={model} type="hidden" name="models" value={model} />
      ))}
      <div className="model-select-control">
        <button
          ref={trigger}
          type="button"
          className="model-select-trigger"
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-controls={open ? `${id}-options` : undefined}
          aria-labelledby={`${id}-label ${id}-summary`}
          onClick={() => setOpen(!open)}
        >
          <span id={`${id}-summary`}>{summary}</span>
          <ChevronDown size={16} aria-hidden="true" />
        </button>
        {open && (
          <div
            className="model-select-popover"
            id={`${id}-options`}
            role="dialog"
            aria-labelledby={`${id}-label`}
          >
            <label className="model-search">
              <Search size={15} aria-hidden="true" />
              <input
                type="search"
                autoFocus
                aria-label={t("searchModels")}
                placeholder={t("searchModels")}
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") event.preventDefault();
                }}
              />
            </label>
            <label className="model-option all-models-option">
              <input
                type="checkbox"
                checked={selected.includes("*")}
                onChange={() =>
                  setSelected((current) => toggleModelSelection(current, "*"))
                }
              />
              <span>
                {t("allModels")}
                <small>{t("allModelsHint")}</small>
              </span>
            </label>
            <div className="model-options-list">
              {options.map((model) => (
                <label className="model-option" key={model}>
                  <input
                    type="checkbox"
                    checked={selected.includes(model)}
                    onChange={() =>
                      setSelected((current) =>
                        toggleModelSelection(current, model),
                      )
                    }
                  />
                  <span>
                    <code>{model}</code>
                    {!available.has(model) && (
                      <small>{t("modelUnavailable")}</small>
                    )}
                  </span>
                </label>
              ))}
              {!options.length && (
                <p className="model-options-empty">
                  {t(query ? "noMatchingModels" : "noModels")}
                </p>
              )}
            </div>
            <div className="model-select-footer">
              <span>{summary}</span>
              <button type="button" onClick={() => setSelected([])}>
                {t("clearSelection")}
              </button>
            </div>
          </div>
        )}
      </div>
      {!!selected.length && !selected.includes("*") && (
        <div className="model-tags selected-model-tags">
          {selected.slice(0, 4).map((model) => (
            <code key={model}>{model}</code>
          ))}
          {selected.length > 4 && <small>+{selected.length - 4}</small>}
        </div>
      )}
      <small className="field-hint">{t("modelHint")}</small>
    </div>
  );
}
