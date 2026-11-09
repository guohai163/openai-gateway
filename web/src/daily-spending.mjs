/**
 * @typedef {{ date: string, requests: number, input_tokens: number, output_tokens: number,
 * unknown_cost_requests: number, totals_by_currency: Record<string, string | null> }} SpendingDay
 */

/** @param {SpendingDay[]} days */
export function dailySpendingSeries(days) {
  const currencies = [
    ...new Set(days.flatMap((day) => Object.keys(day.totals_by_currency))),
  ].sort();
  return [
    {
      id: "tokens",
      label: "dailyTotalTokens",
      axis: "tokens",
      values: days.map((day) => day.input_tokens + day.output_tokens),
    },
    {
      id: "input_tokens",
      label: "input_tokens",
      axis: "tokens",
      values: days.map((day) => day.input_tokens),
    },
    {
      id: "output_tokens",
      label: "output_tokens",
      axis: "tokens",
      values: days.map((day) => day.output_tokens),
    },
    ...currencies.map((currency) => ({
      id: `cost:${currency}`,
      label: "cost",
      axis: currency,
      values: days.map((day) => {
        // No requests in this currency means zero; unpriced requests stay unknown.
        if (!(currency in day.totals_by_currency)) return 0;
        const value = day.totals_by_currency[currency];
        return value == null ? null : Number(value);
      }),
    })),
  ];
}

/** @param {(number | null)[]} values */
export function dailyAxisMax(values) {
  const max = Math.max(
    0,
    ...values.filter((value) => value != null && Number.isFinite(value)),
  );
  if (!max) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(max / 4));
  const step =
    [1, 2, 2.5, 5, 10].find((value) => value * magnitude >= max / 4) ?? 10;
  return step * magnitude * 4;
}
