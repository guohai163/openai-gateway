import { useEffect, useId, useState, type ReactNode } from "react";
import { Search } from "lucide-react";

export type LogFilterValues = {
  model: string;
  owner_id: string;
  upstream_key_id: string;
  gateway_key_id: string;
  kind: string;
  status: string;
  start: string;
  end: string;
  q: string;
};
export const emptyLogFilters: LogFilterValues = {
  model: "",
  owner_id: "",
  upstream_key_id: "",
  gateway_key_id: "",
  kind: "",
  status: "",
  start: "",
  end: "",
  q: "",
};
export type LogFilterOptions = {
  models?: string[];
  owners?: { id: string; name: string | null }[];
  upstreams?: { id: string; name: string | null }[];
  keys?: { id: string; name: string | null }[];
};

export function LogFilters({
  value,
  options,
  isAdmin,
  loading,
  totalText,
  columnControl,
  onChange,
  t,
}: {
  value: LogFilterValues;
  options: LogFilterOptions;
  isAdmin: boolean;
  loading: boolean;
  totalText: string;
  columnControl: ReactNode;
  onChange: (filters: LogFilterValues) => void;
  t: (key: string) => string;
}) {
  const [draft, setDraft] = useState(value);
  const [expanded, setExpanded] = useState(false);
  const id = useId();
  useEffect(() => setDraft(value), [value]);
  const set = (key: keyof LogFilterValues, v: string) =>
    setDraft({ ...draft, [key]: v });
  const localDate = (date: string) => {
    if (!date) return "";
    const d = new Date(date);
    return new Date(d.getTime() - d.getTimezoneOffset() * 60000)
      .toISOString()
      .slice(0, 16);
  };
  const select = (
    field: "owner_id" | "upstream_key_id" | "gateway_key_id",
    label: string,
    items: { id: string; name: string | null }[] = []
  ) => (
    <label>
      {t(label)}
      <select value={draft[field]} onChange={(e) => set(field, e.target.value)}>
        <option value="">{t("logAll")}</option>
        {items.map((item) => (
          <option key={item.id} value={item.id}>
            {item.name || item.id}
          </option>
        ))}
      </select>
    </label>
  );
  const active = (Object.keys(value) as (keyof LogFilterValues)[]).filter(
    (key) => value[key]
  );
  return (
    <div className="log-filter-panel">
      <div className="log-filter-heading">
        <button
          type="button"
          className="log-filter-toggle"
          aria-expanded={expanded}
          aria-controls={`${id}-panel`}
          onClick={() => setExpanded(!expanded)}
        >
          <Search size={16} aria-hidden="true" />
          {t(expanded ? "logHideFilters" : "logShowFilters")}
          {active.length > 0 && (
            <span className="log-filter-count">
              {t("logFilterCount").replace("{count}", String(active.length))}
            </span>
          )}
          <span aria-hidden="true">{expanded ? "▴" : "▾"}</span>
        </button>
        <div className="log-filter-summary">
          {columnControl}
          {!expanded && active.length > 0 && (
            <button
              type="button"
              className="text-button"
              onClick={() => {
                setDraft({ ...emptyLogFilters });
                onChange({ ...emptyLogFilters });
              }}
            >
              {t("logClear")}
            </button>
          )}
          <span className="record-count">{totalText}</span>
        </div>
      </div>
      <div id={`${id}-panel`} hidden={!expanded}>
        <div className="log-status-tabs" role="group" aria-label={t("status")}>
          {[
            ["", "logAllRequests"],
            ["success", "logSuccess"],
            ["failed", "logFailed"],
          ].map(([status, label]) => (
            <button
              type="button"
              key={status}
              aria-pressed={value.status === status}
              onClick={() => onChange({ ...value, status })}
            >
              {t(label)}
            </button>
          ))}
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            onChange({ ...draft });
            setExpanded(false);
          }}
        >
          <div className="log-filter-grid">
            <label>
              {t("model")}
              <input
                list={`${id}-models`}
                value={draft.model}
                onChange={(e) => set("model", e.target.value)}
                placeholder={t("logAllModels")}
              />
              <datalist id={`${id}-models`}>
                {(options.models || []).map((model) => (
                  <option key={model} value={model} />
                ))}
              </datalist>
            </label>
            {isAdmin && select("owner_id", "owner_email", options.owners)}
            {isAdmin &&
              select("upstream_key_id", "upstream_name", options.upstreams)}
            {select("gateway_key_id", "gateway_name", options.keys)}
            <label>
              {t("status")}
              <select
                value={draft.status}
                onChange={(e) => set("status", e.target.value)}
              >
                <option value="">{t("logAll")}</option>
                <option value="success">{t("logSuccess")}</option>
                <option value="failed">{t("logFailed")}</option>
                {[200, 400, 401, 403, 404, 429, 500, 502, 503, 504].map(
                  (code) => (
                    <option key={code} value={String(code)}>
                      {code}
                    </option>
                  )
                )}
              </select>
            </label>
            {isAdmin && (
              <label>
                {t("kind")}
                <select
                  value={draft.kind}
                  onChange={(e) => set("kind", e.target.value)}
                >
                  <option value="">{t("logAll")}</option>
                  <option value="inference">{t("inference")}</option>
                  <option value="probe">{t("probeKind")}</option>
                </select>
              </label>
            )}
            <label>
              {t("logStart")}
              <input
                type="datetime-local"
                value={localDate(draft.start)}
                onChange={(e) =>
                  set(
                    "start",
                    e.target.value ? new Date(e.target.value).toISOString() : ""
                  )
                }
              />
            </label>
            <label>
              {t("logEnd")}
              <input
                type="datetime-local"
                min={localDate(draft.start)}
                value={localDate(draft.end)}
                onChange={(e) =>
                  set(
                    "end",
                    e.target.value ? new Date(e.target.value).toISOString() : ""
                  )
                }
              />
            </label>
          </div>
          <div className="log-filter-search">
            <label className="search">
              <Search size={16} />
              <input
                aria-label={t("logSearch")}
                placeholder={t("logSearch")}
                value={draft.q}
                maxLength={512}
                onChange={(e) => set("q", e.target.value)}
              />
            </label>
            <button type="submit" className="primary" disabled={loading}>
              {t("logApply")}
            </button>
            <button
              type="button"
              className="secondary"
              onClick={() => {
                setDraft({ ...emptyLogFilters });
                onChange({ ...emptyLogFilters });
              }}
            >
              {t("logClear")}
            </button>
          </div>
          <small className="log-time-hint">{t("logTimeHint")}</small>
        </form>
        {active.length > 0 && (
          <div className="log-filter-chips" aria-label={t("logActiveFilters")}>
            {active.map((key) => {
              const labels: Record<keyof LogFilterValues, string> = {
                model: "model",
                owner_id: "owner_email",
                upstream_key_id: "upstream_name",
                gateway_key_id: "gateway_name",
                kind: "kind",
                status: "status",
                start: "logStart",
                end: "logEnd",
                q: "logSearch",
              };
              const items =
                key === "owner_id"
                  ? options.owners
                  : key === "upstream_key_id"
                  ? options.upstreams
                  : key === "gateway_key_id"
                  ? options.keys
                  : undefined;
              const raw = value[key];
              const display =
                key === "start" || key === "end"
                  ? new Date(raw).toLocaleString()
                  : items?.find((item) => item.id === raw)?.name ||
                    (
                      {
                        success: t("logSuccess"),
                        failed: t("logFailed"),
                        inference: t("inference"),
                        probe: t("probeKind"),
                      } as Record<string, string>
                    )[raw] ||
                    raw;
              return (
                <button
                  type="button"
                  key={key}
                  className="secondary"
                  onClick={() => onChange({ ...value, [key]: "" })}
                  aria-label={`${t("logRemoveFilter")} ${t(labels[key])}`}
                >
                  {t(labels[key])}: {display} <span aria-hidden="true">×</span>
                </button>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
