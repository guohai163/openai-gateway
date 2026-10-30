import { Activity, ArrowDown, ArrowUp, Coins, Info } from "lucide-react";

type Hour = {
  hour: string;
  requests: number;
  input_tokens: number;
  output_tokens: number;
  total_cost: string | null;
};
type ModelRow = {
  model: string;
  requests: number;
  input_tokens: number;
  output_tokens: number;
  total_cost: string | null;
};
type Props = {
  hourly: Hour[];
  models: ModelRow[];
  locale: string;
  t: (key: string) => string;
};

function number(value: number, locale: string) {
  return new Intl.NumberFormat(locale).format(value || 0);
}
function money(value: string | number | null | undefined, locale: string) {
  if (value === null || value === undefined) return "—";
  return new Intl.NumberFormat(locale, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: 8,
  }).format(Number(value));
}
function hourLabel(value: string, locale: string) {
  return new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}
function points(values: number[], width: number, height: number) {
  const padX = 18;
  const padY = 18;
  const max = Math.max(...values, 1);
  return values.map((value, index) => {
    const x =
      padX + (index * (width - padX * 2)) / Math.max(values.length - 1, 1);
    const y = height - padY - (value / max) * (height - padY * 2);
    return { x, y, value, max };
  });
}

function TrendCard({
  chartId,
  title,
  summary,
  values,
  labels,
  color,
  formatter,
  icon,
  t,
}: {
  chartId: string;
  title: string;
  summary: string;
  values: number[];
  labels: string[];
  color: string;
  formatter: (value: number) => string;
  icon: React.ReactNode;
  t: (key: string) => string;
}) {
  const width = 760;
  const height = 178;
  const plotted = points(values, width, height);
  const line = plotted.map((point) => `${point.x},${point.y}`).join(" ");
  const area = `${plotted[0]?.x || 18},${height - 18} ${line} ${plotted.at(-1)?.x || width - 18},${height - 18}`;
  return (
    <section className="panel trend-card">
      <div className="trend-heading">
        <div className="trend-title">
          <span className="trend-icon" style={{ color }}>
            {icon}
          </span>
          <div>
            <h2>{title}</h2>
            <small>{t("last24Hours")}</small>
          </div>
        </div>
        <strong>{summary}</strong>
      </div>
      <div className="trend-chart-wrap">
        <svg
          className="trend-chart"
          viewBox={`0 0 ${width} ${height}`}
          role="img"
          aria-label={title}
          preserveAspectRatio="none"
        >
          <defs>
            <linearGradient
              id={`trend-fill-${chartId}`}
              x1="0"
              x2="0"
              y1="0"
              y2="1"
            >
              <stop offset="0" stopColor={color} stopOpacity=".22" />
              <stop offset="1" stopColor={color} stopOpacity="0" />
            </linearGradient>
          </defs>
          <line
            x1="18"
            x2={width - 18}
            y1={height - 18}
            y2={height - 18}
            className="trend-axis"
          />
          <polygon points={area} fill={`url(#trend-fill-${chartId})`} />
          <polyline
            points={line}
            fill="none"
            stroke={color}
            strokeWidth="3"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
          {plotted.map((point, index) => (
            <circle
              key={`${labels[index]}-${index}`}
              cx={point.x}
              cy={point.y}
              r={index === plotted.length - 1 ? 5 : 2.5}
              fill="var(--surface)"
              stroke={color}
              strokeWidth="2"
            >
              <title>
                {labels[index]} · {formatter(point.value)}
              </title>
            </circle>
          ))}
        </svg>
        <div className="trend-labels">
          {labels
            .filter(
              (_, index) =>
                index === 0 || index === labels.length - 1 || index % 6 === 0,
            )
            .map((label) => (
              <span key={label}>{label}</span>
            ))}
        </div>
      </div>
    </section>
  );
}

export function OverviewAnalytics({ hourly, models, locale, t }: Props) {
  const hours = hourly.length
    ? hourly
    : Array.from({ length: 24 }, (_, index) => ({
        hour: new Date(Date.now() - (23 - index) * 3600000).toISOString(),
        requests: 0,
        input_tokens: 0,
        output_tokens: 0,
        total_cost: null,
      }));
  const labels = hours.map((row) => hourLabel(row.hour, locale));
  const requestValues = hours.map((row) => Number(row.requests || 0));
  const costValues = hours.map((row) => Number(row.total_cost || 0));
  const requestTotal = requestValues.reduce((sum, value) => sum + value, 0);
  const knownCosts = hours
    .filter((row) => row.total_cost !== null)
    .reduce((sum, row) => sum + Number(row.total_cost), 0);
  const costKnown = hours.some((row) => row.total_cost !== null);
  return (
    <div className="overview-analytics">
      <div className="trend-grid">
        <TrendCard
          chartId="requests"
          title={t("hourlyRequests")}
          summary={number(requestTotal, locale)}
          values={requestValues}
          labels={labels}
          color="var(--positive)"
          formatter={(value) => number(value, locale)}
          icon={<Activity size={18} />}
          t={t}
        />
        <TrendCard
          chartId="cost"
          title={t("hourlyCost")}
          summary={costKnown ? money(knownCosts, locale) : t("unknown")}
          values={costValues}
          labels={labels}
          color="#9971f8"
          formatter={(value) => money(value, locale)}
          icon={<Coins size={18} />}
          t={t}
        />
      </div>
      <section className="panel model-activity-panel">
        <div className="panel-heading">
          <div>
            <h2>{t("modelActivity")}</h2>
            <small>{t("last24Hours")}</small>
          </div>
          <Info size={16} className="muted" />
        </div>
        {models.length ? (
          <div className="model-activity-table-wrap">
            <table className="model-activity-table">
              <thead>
                <tr>
                  <th>{t("model")}</th>
                  <th>{t("requests")}</th>
                  <th>{t("input_tokens")}</th>
                  <th>{t("output_tokens")}</th>
                  <th>{t("cost")}</th>
                </tr>
              </thead>
              <tbody>
                {models.map((row) => (
                  <tr key={row.model}>
                    <td>
                      <code>{row.model || t("unknown")}</code>
                    </td>
                    <td>{number(row.requests, locale)}</td>
                    <td>
                      <span className="token-cell">
                        <ArrowDown size={14} className="token-input" />
                        {number(row.input_tokens, locale)}
                      </span>
                    </td>
                    <td>
                      <span className="token-cell">
                        <ArrowUp size={14} className="token-output" />
                        {number(row.output_tokens, locale)}
                      </span>
                    </td>
                    <td
                      className={
                        row.total_cost === null ? "muted" : "cost-total"
                      }
                    >
                      {money(row.total_cost, locale)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="empty-state compact">
            <h3>{t("noRecentRequests")}</h3>
            <p>{t("noRecentRequestsHint")}</p>
          </div>
        )}
      </section>
    </div>
  );
}
