export function quotaWindowState(window, now = Date.now()) {
  if (
    !window ||
    typeof window.remaining_percent !== "number" ||
    !Number.isFinite(window.remaining_percent) ||
    window.remaining_percent < 0 ||
    window.remaining_percent > 100
  )
    return null;
  const reset = window.resets_at ? Date.parse(window.resets_at) : NaN;
  return {
    remaining: window.remaining_percent,
    expired: Number.isFinite(reset) && reset <= now,
    reset: Number.isFinite(reset) ? reset : null,
  };
}

export function quotaCountdown(reset, now = Date.now()) {
  if (reset == null || !Number.isFinite(reset)) return "—";
  if (reset <= now) return "";
  const minutes = Math.ceil((reset - now) / 60000);
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  if (days) return `${days}d ${hours}h`;
  if (hours) return `${hours}h ${minutes % 60}m`;
  return `${minutes}m`;
}

export function quotaPercent(value, locale) {
  return (
    new Intl.NumberFormat(locale, {
      maximumFractionDigits: 1,
    }).format(value) + "%"
  );
}
