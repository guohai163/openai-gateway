import { useState } from "react";

export function KeyCacheFields({
  row,
  t,
}: {
  row: Record<string, any>;
  t: (key: string) => string;
}) {
  const [mode, setMode] = useState<string>(row.prompt_cache_mode || "inherit");
  const [ttl, setTTL] = useState<string>(row.prompt_cache_ttl || "30m");
  return (
    <fieldset className="key-routing-fields">
      <legend>{t("keyPromptCache")}</legend>
      <label>
        {t("promptCachePolicy")}
        <select
          name="prompt_cache_mode"
          value={mode}
          onChange={(event) => setMode(event.target.value)}
        >
          <option value="inherit">{t("promptCacheInherit")}</option>
          <option value="first_system">{t("promptCacheFirstSystem")}</option>
        </select>
      </label>
      {mode === "first_system" && (
        <>
          <label>
            {t("promptCacheLifetime")}
            <select
              name="prompt_cache_ttl"
              value={ttl}
              onChange={(event) => setTTL(event.target.value)}
            >
              <option value="30m">{t("promptCache30m")}</option>
              <option value="12h">{t("promptCache12h")}</option>
            </select>
          </label>
          <p>{t("promptCacheCompatibility")}</p>
          <small>{t("promptCacheFirstSystemHint")}</small>
        </>
      )}
    </fieldset>
  );
}
