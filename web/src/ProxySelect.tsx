import { useEffect, useState } from "react";

export type ProxyOption = { id: string; name: string; status: string };

export function ProxyChoice({
  options,
  value,
  onChange,
  disabled,
  t,
}: {
  options: ProxyOption[];
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  t: (key: string) => string;
}) {
  return (
    <label>
      {t("outboundProxy")}
      <select
        name="proxy_id"
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value="">{t("proxyDefault")}</option>
        {options.map((proxy) => (
          <option
            key={proxy.id}
            value={proxy.id}
            disabled={proxy.status !== "active" && proxy.id !== value}
          >
            {proxy.name}
            {proxy.status !== "active" ? ` · ${t(proxy.status)}` : ""}
          </option>
        ))}
        {value && !options.some((proxy) => proxy.id === value) && (
          <option value={value}>{t("proxy_unavailable")}</option>
        )}
      </select>
    </label>
  );
}

export function ProxySelect({
  api,
  initialValue = "",
  t,
}: {
  api: (path: string, options?: RequestInit) => Promise<any>;
  initialValue?: string;
  t: (key: string) => string;
}) {
  const [options, setOptions] = useState<ProxyOption[]>([]);
  const [value, setValue] = useState(initialValue || "");
  const [ready, setReady] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    setReady(false);
    setError("");
    api("proxies")
      .then((result) => {
        if (active) {
          setOptions(result);
          setReady(true);
        }
      })
      .catch((e) => {
        if (active)
          setError(e instanceof Error ? e.message : "proxy_load_failed");
      });
    return () => {
      active = false;
    };
  }, [api, attempt]);
  return (
    <fieldset className="key-routing-fields">
      <legend>{t("outboundProxy")}</legend>
      <ProxyChoice
        options={options}
        value={value}
        onChange={setValue}
        disabled={!ready}
        t={t}
      />
      <input
        type="hidden"
        name="proxy_ready"
        value={ready ? "true" : "false"}
      />
      {!ready && !error && <small>{t("loading")}</small>}
      {error && (
        <p className="error-hint" role="alert">
          {t(error)}{" "}
          <button
            type="button"
            className="text-button"
            onClick={() => setAttempt(attempt + 1)}
          >
            {t("refresh")}
          </button>
        </p>
      )}
      <small>{t("proxySelectionHint")}</small>
    </fieldset>
  );
}
