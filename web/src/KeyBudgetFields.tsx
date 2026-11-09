import { formatDateTime } from "./date-time.mjs";
import { money } from "./SpendingDetails";

type Props = {
  row: Record<string, any>;
  t: (key: string) => string;
  locale: string;
};

export function KeyBudgetFields({ row, t, locale }: Props) {
  return (
    <fieldset className="key-routing-fields">
      <legend>{t("keyBudget")}</legend>
      <p>{t("keyBudgetHint")}</p>
      {["USD", "CNY"].map((currency) => (
        <div className="key-budget-currency" key={currency}>
          <strong>{currency}</strong>
          {["daily", "weekly"].map((period) => (
            <label key={period}>
              {t(period === "daily" ? "dailyBudget" : "weeklyBudget")}
              <input
                name={`budget_${currency}_${period}`}
                type="number"
                min="0"
                max="999999999999.999999"
                step="0.000001"
                placeholder={t("unlimitedBudget")}
                defaultValue={row.spend_limits?.[currency]?.[period] ?? ""}
              />
              {row.id && (
                <small>
                  {t("budgetSpent")}:{" "}
                  {money(
                    row.budget_usage?.[currency]?.[period] || "0",
                    locale,
                    currency,
                  )}
                </small>
              )}
            </label>
          ))}
        </div>
      ))}
      <small>{t("keyBudgetSafetyHint")}</small>
      {row.budget_timezone && (
        <small>
          {t("report_timezone")}: {row.budget_timezone}
        </small>
      )}
    </fieldset>
  );
}

export function KeyBudgetSummary({ row, t, locale }: Props) {
  const limits = row.spend_limits || {};
  if (!Object.keys(limits).length)
    return <span className="muted">{t("unlimitedBudget")}</span>;
  return (
    <div className="key-budget-summary">
      {row.budget_hold_until && (
        <small role="status">
          {t(
            new Date(row.budget_active_until).getTime() > Date.now()
              ? "budgetSettling"
              : "budgetUnknown",
          )}
        </small>
      )}
      {Object.entries(limits).map(([currency, raw]) => {
        const limit = raw as Record<string, string>;
        return ["daily", "weekly"].map((period) => {
          if (limit[period] == null || limit[period] === "") return null;
          const used = row.budget_usage?.[currency]?.[period] || "0";
          const unknown =
            row.budget_usage?.[currency]?.[`${period}_unknown`] || 0;
          return (
            <div
              key={`${currency}-${period}`}
              title={formatDateTime(
                row[`budget_${period === "daily" ? "day" : "week"}_reset_at`],
              )}
            >
              <small>
                {t(period === "daily" ? "dailyBudget" : "weeklyBudget")} ·{" "}
                {currency}
              </small>
              <span
                className={
                  Number(used) >= Number(limit[period]) ? "error-hint" : ""
                }
              >
                {money(used, locale, currency)} /{" "}
                {money(limit[period], locale, currency)}
              </span>
              {unknown > 0 && (
                <small className="error-hint">{t("budgetUnknown")}</small>
              )}
            </div>
          );
        });
      })}
    </div>
  );
}
