import { useId, useState } from "react";
import { dailyAxisMax, dailySpendingSeries } from "./daily-spending.mjs";
import type { SpendingDay } from "./daily-spending.mjs";
import { money } from "./SpendingDetails";

const colors = [
  "#9070df",
  "#3298d5",
  "var(--positive)",
  "#d58a30",
  "#d763a7",
  "#38a4a4",
];

export function DailySpendingChart({
  days,
  locale,
  t,
}: {
  days: SpendingDay[];
  locale: string;
  t: (key: string) => string;
}) {
  const titleId = useId();
  const hintId = useId();
  const [hidden, setHidden] = useState<string[]>([]);
  const [active, setActive] = useState<number | null>(null);
  const series = dailySpendingSeries(days).map((line, index) => ({
    ...line,
    color: colors[index % colors.length],
    name: t(line.label) + (line.axis === "tokens" ? "" : ` (${line.axis})`),
  }));
  const visible = series.filter((line) => !hidden.includes(line.id));
  const axes = [...new Set(visible.map((line) => line.axis))];
  const costAxes = axes.filter((axis) => axis !== "tokens");
  const maxima = Object.fromEntries(
    axes.map((axis) => [
      axis,
      dailyAxisMax(
        visible
          .filter((line) => line.axis === axis)
          .flatMap((line) => line.values),
      ),
    ]),
  );
  const width = 1000,
    height = 326;
  const left = 76,
    right = width - 24 - costAxes.length * 88;
  const top = 34,
    bottom = height - 38;
  const x = (index: number) =>
    days.length === 1
      ? (left + right) / 2
      : left + (index * (right - left)) / Math.max(days.length - 1, 1);
  const y = (value: number, axis: string) =>
    bottom - (value / maxima[axis]) * (bottom - top);
  const fullNumber = (value: number) =>
    new Intl.NumberFormat(locale).format(value);
  const tickNumber = (value: number) =>
    new Intl.NumberFormat(locale, {
      notation: "compact",
      maximumSignificantDigits: 3,
    }).format(value);
  const dateLabel = (date: string, full = false) =>
    new Intl.DateTimeFormat(locale, {
      timeZone: "UTC",
      ...(full ? { year: "numeric" as const } : {}),
      month: "2-digit",
      day: "2-digit",
    }).format(new Date(date + "T00:00:00Z"));
  const formatValue = (value: number | null, axis: string) =>
    value == null
      ? t("unknown")
      : axis === "tokens"
        ? fullNumber(value)
        : money(value, locale, axis);
  const selected = active == null ? null : Math.min(active, days.length - 1);
  const selectedDay = selected == null ? null : days[selected];

  return (
    <section className="daily-spending" aria-labelledby={titleId}>
      <div className="daily-spending-heading">
        <h3 id={titleId}>{t("dailySpendingTrend")}</h3>
        <small id={hintId}>{t("dailySpendingHint")}</small>
      </div>
      <div
        className="daily-spending-legend"
        aria-label={t("dailySpendingLines")}
      >
        {series.map((line) => (
          <button
            key={line.id}
            type="button"
            aria-pressed={!hidden.includes(line.id)}
            onClick={() =>
              setHidden((current) =>
                current.includes(line.id)
                  ? current.filter((id) => id !== line.id)
                  : [...current, line.id],
              )
            }
          >
            <span style={{ background: line.color }} />
            {line.name}
          </button>
        ))}
      </div>
      {visible.length ? (
        <div className="daily-spending-scroll">
          <div className="daily-spending-plot">
            <svg
              viewBox={`0 0 ${width} ${height}`}
              role="group"
              aria-labelledby={titleId}
              aria-describedby={hintId}
            >
              {[0, 1, 2, 3, 4].map((tick) => (
                <line
                  key={tick}
                  x1={left}
                  x2={right}
                  y1={top + (tick * (bottom - top)) / 4}
                  y2={top + (tick * (bottom - top)) / 4}
                  className="daily-spending-grid"
                />
              ))}
              {axes.map((axis) => {
                const isTokens = axis === "tokens";
                const axisX = isTokens
                  ? left - 12
                  : right + 12 + costAxes.indexOf(axis) * 88;
                return (
                  <g
                    key={axis}
                    className="daily-spending-axis"
                    textAnchor={isTokens ? "end" : "start"}
                  >
                    <text x={axisX} y={18}>
                      {isTokens ? "Token" : axis}
                    </text>
                    {[0, 1, 2, 3, 4].map((tick) => (
                      <text
                        key={tick}
                        x={axisX}
                        y={bottom - (tick * (bottom - top)) / 4 + 4}
                      >
                        {tickNumber((maxima[axis] * tick) / 4)}
                      </text>
                    ))}
                  </g>
                );
              })}
              {days.map((day, index) =>
                index === 0 ||
                index === days.length - 1 ||
                index % Math.max(1, Math.ceil((days.length - 1) / 6)) === 0 ? (
                  <text
                    key={day.date}
                    x={x(index)}
                    y={height - 10}
                    textAnchor="middle"
                    className="daily-spending-axis"
                  >
                    {dateLabel(day.date)}
                  </text>
                ) : null,
              )}
              {visible.map((line) => {
                let connected = false;
                const path = line.values
                  .map((value, index) => {
                    if (value == null) {
                      connected = false;
                      return "";
                    }
                    const command = connected ? "L" : "M";
                    connected = true;
                    return `${command}${x(index)},${y(value, line.axis)}`;
                  })
                  .join(" ");
                return (
                  <g key={line.id}>
                    <path
                      d={path}
                      fill="none"
                      stroke={line.color}
                      strokeWidth="2.5"
                      strokeLinejoin="round"
                      strokeLinecap="round"
                    />
                    {line.values.map((value, index) =>
                      value != null &&
                      (days.length < 40 || index === selected) ? (
                        <circle
                          key={days[index].date}
                          cx={x(index)}
                          cy={y(value, line.axis)}
                          r={index === selected ? 4.5 : 2.5}
                          fill="var(--surface)"
                          stroke={line.color}
                          strokeWidth="2"
                        />
                      ) : null,
                    )}
                  </g>
                );
              })}
              {selected != null && (
                <line
                  x1={x(selected)}
                  x2={x(selected)}
                  y1={top}
                  y2={bottom}
                  className="daily-spending-cursor"
                />
              )}
              {!!days.length && (
                <rect
                  x={left}
                  y={top}
                  width={right - left}
                  height={bottom - top}
                  fill="transparent"
                  tabIndex={0}
                  role="slider"
                  aria-label={t("dailySpendingDate")}
                  aria-valuemin={0}
                  aria-valuemax={days.length - 1}
                  aria-valuenow={selected ?? 0}
                  aria-valuetext={dateLabel(days[selected ?? 0].date, true)}
                  onPointerMove={(event) => {
                    const bounds =
                      event.currentTarget.ownerSVGElement!.getBoundingClientRect();
                    const chartX =
                      ((event.clientX - bounds.left) / bounds.width) * width;
                    setActive(
                      Math.max(
                        0,
                        Math.min(
                          days.length - 1,
                          Math.round(
                            ((chartX - left) / (right - left)) *
                              (days.length - 1),
                          ),
                        ),
                      ),
                    );
                  }}
                  onPointerLeave={() => setActive(null)}
                  onFocus={() => setActive(0)}
                  onBlur={() => setActive(null)}
                  onKeyDown={(event) => {
                    if (
                      ![
                        "ArrowLeft",
                        "ArrowRight",
                        "ArrowUp",
                        "ArrowDown",
                        "Home",
                        "End",
                      ].includes(event.key)
                    )
                      return;
                    event.preventDefault();
                    const next =
                      event.key === "Home"
                        ? 0
                        : event.key === "End"
                          ? days.length - 1
                          : (selected ?? 0) +
                            (["ArrowRight", "ArrowUp"].includes(event.key)
                              ? 1
                              : -1);
                    setActive(Math.max(0, Math.min(days.length - 1, next)));
                  }}
                />
              )}
            </svg>
            {selectedDay && selected != null && (
              <div
                className="daily-spending-tooltip"
                role="status"
                style={{
                  left: `${Math.max(18, Math.min(82, (x(selected) / width) * 100))}%`,
                }}
              >
                <strong>{dateLabel(selectedDay.date, true)}</strong>
                {visible.map((line) => (
                  <div key={line.id}>
                    <span>
                      <i style={{ background: line.color }} />
                      {line.name}
                    </span>
                    <b>{formatValue(line.values[selected], line.axis)}</b>
                  </div>
                ))}
                {selectedDay.unknown_cost_requests > 0 && (
                  <small className="spend-partial">
                    {t("unknownCostCount").replace(
                      "{count}",
                      fullNumber(selectedDay.unknown_cost_requests),
                    )}
                  </small>
                )}
              </div>
            )}
          </div>
        </div>
      ) : (
        <p className="spending-empty">{t("dailySpendingHidden")}</p>
      )}
    </section>
  );
}
