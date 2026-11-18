import { formatTotals, money } from "./SpendingDetails";

type Translate = (key: string) => string;

export function BillingCycleField({
  defaultValue = 1,
  t,
}: {
  defaultValue?: number;
  t: Translate;
}) {
  return (
    <label>
      {t("billingCycleDay")}
      <select name="billing_cycle_day" defaultValue={defaultValue} required>
        {Array.from({ length: 31 }, (_, i) => i + 1).map((day) => (
          <option key={day} value={day}>
            {t("billingCycleDayValue").replace("{day}", String(day))}
          </option>
        ))}
      </select>
      <small>{t("billingCycleHint")}</small>
    </label>
  );
}

export function KeySpendButton({
  row,
  locale,
  t,
  expanded,
  onClick,
  billingCycle = false,
}: {
  row: any;
  locale: string;
  t: Translate;
  expanded: boolean;
  onClick: () => void;
  billingCycle?: boolean;
}) {
  return (
    <button
      className="key-spend-button"
      type="button"
      aria-expanded={expanded}
      onClick={onClick}
    >
      <strong>
        {Number(row.month_requests) === 0
          ? money(0, locale, row.month_currency || "USD")
          : formatTotals(row.month_totals_by_currency, locale) ||
            (row.month_cost == null
              ? t("unknown")
              : money(row.month_cost, locale, row.month_currency || "USD"))}
      </strong>
      <small>
        {t(billingCycle ? "currentBillingCycle" : "currentCalendarMonth")}
        {billingCycle &&
          row.billing_cycle_start_date &&
          ` · ${t("spendSince").replace("{date}", row.billing_cycle_start_date)}`}
        {row.month_unknown > 0 &&
          " · " +
            t("partialEstimate").replace(
              "{currency}",
              row.month_currency ||
                Object.keys(row.month_totals_by_currency || {}).join(" / ") ||
                "USD",
            )}
      </small>
      <span>{t(expanded ? "lessDetails" : "moreDetails")}</span>
    </button>
  );
}
