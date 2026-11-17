type Props = {
  row: Record<string, any>;
  t: (key: string) => string;
};

export function KeyConcurrencyFields({ row, t }: Props) {
  return (
    <fieldset className="key-routing-fields">
      <legend>{t("keyConcurrency")}</legend>
      <label>
        {t("max_concurrent_requests")}
        <input
          name="max_concurrent_requests"
          type="number"
          min="0"
          max="1000"
          step="1"
          placeholder={t("concurrencyAutomatic")}
          defaultValue={row.max_concurrent_requests ?? ""}
        />
      </label>
      <small>{t("keyConcurrencyHint")}</small>
      <small>{t("keyConcurrencyBudgetHint")}</small>
    </fieldset>
  );
}

export function KeyConcurrencySummary({ row, t }: Props) {
  const limit = Number(row.effective_max_concurrent_requests || 0);
  return (
    <div className="key-budget-summary">
      <span>
        {limit
          ? t("concurrencyCount").replace("{count}", String(limit))
          : t("unlimitedConcurrency")}
      </span>
      {row.max_concurrent_requests == null && (
        <small>{t("concurrencyAutomatic")}</small>
      )}
      {limit > 0 && (
        <small>
          {t("concurrencyActive").replace(
            "{count}",
            String(row.active_requests || 0),
          )}
        </small>
      )}
    </div>
  );
}
