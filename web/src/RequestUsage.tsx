import { ArrowDown, ArrowUp, Database, Info } from "lucide-react";

type Props = {
  row: Record<string, any>;
  locale: string;
  t: (key: string) => string;
};

function money(value: unknown, locale: string) {
  if (value == null) return "—";
  return new Intl.NumberFormat(locale, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: 16,
  }).format(Number(value));
}

export function RequestTokens({ row, locale, t }: Props) {
  const n = (value: number) => new Intl.NumberFormat(locale).format(value);
  const duplicate = row.usage_accounting === "duplicate";
  const known = row.usage_known;
  const cacheApplicable =
    row.endpoint !== "/v1/embeddings" &&
    row.pricing_snapshot?.cache_applicable !== false;
  const cache = !cacheApplicable
    ? "cacheNotApplicable"
    : row.cached_tokens_known
      ? row.cached_tokens > 0
        ? "cacheHit"
        : "cacheMiss"
      : "cacheUnknown";
  return (
    <div className="request-token-cell">
      <div className="request-token-counts">
        <span title={t("input_tokens")}>
          <ArrowDown size={19} className="token-input" />
          {known || row.input_tokens > 0 ? n(row.input_tokens) : "—"}
        </span>
        <span title={t("output_tokens")}>
          <ArrowUp size={19} className="token-output" />
          {known || row.output_tokens > 0 ? n(row.output_tokens) : "—"}
        </span>
      </div>
      {duplicate ? (
        <small>{t("usage_duplicate")}</small>
      ) : (
        <span
          className={`cache-indicator ${row.cached_tokens_known && row.cached_tokens > 0 ? "hit" : ""}`}
        >
          <Database size={12} />
          {t(cache)}
          {cacheApplicable && row.cached_tokens_known && row.cached_tokens > 0
            ? ` · ${n(row.cached_tokens)}`
            : ""}
        </span>
      )}
      {row.cache_write_tokens > 0 && (
        <small>
          {t("cacheWrites")} · {n(row.cache_write_tokens)}
        </small>
      )}
    </div>
  );
}

export function RequestCost({ row, locale, t }: Props) {
  const p = row.pricing_snapshot || {};
  const rate = (value: unknown) =>
    value == null ? t("unknown") : `${money(value, locale)} / 1M`;
  const fmt = (value: unknown) =>
    value == null ? t("unknown") : money(value, locale);
  return (
    <div className="request-cost-cell">
      <details>
        <summary>
          <span
            className={row.total_cost != null ? "cost-total" : "cost-unknown"}
          >
            {fmt(row.total_cost)}
          </span>
          <Info size={14} />
        </summary>
        <div className="cost-breakdown">
          <strong>{t("tokenCostEstimate")}</strong>
          <span>
            {t("input_tokens")}
            <b>{fmt(row.input_cost)}</b>
          </span>
          <span>
            {t("output_tokens")}
            <b>{fmt(row.output_cost)}</b>
          </span>
          <span>
            {t("cachedInputIncluded")}
            <b>{fmt(row.cached_input_cost)}</b>
          </span>
          {row.cache_write_tokens > 0 && (
            <span>
              {t("cacheWriteIncluded")}
              <b>{fmt(row.cache_write_cost)}</b>
            </span>
          )}
          <hr />
          {p.image_api ? (
            <>
              {Object.entries(p.modality_prices || {}).map(([name, value]) => {
                const price = value as Record<string, unknown>;
                return (
                  <div className="image-cost-rates" key={name}>
                    <strong>
                      {t(name === "image" ? "imageTokens" : "textTokens")}
                    </strong>
                    <span>
                      {t("inputPrice")}
                      <b>{rate(price.input_price)}</b>
                    </span>
                    {price.output_applicable === true && (
                      <span>
                        {t("outputPrice")}
                        <b>{rate(price.output_price)}</b>
                      </span>
                    )}
                  </div>
                );
              })}
              {p.modality_usage?.input_known && (
                <small>
                  {t("textTokens")}:{" "}
                  {new Intl.NumberFormat(locale).format(
                    p.modality_usage.text_input_tokens,
                  )}{" "}
                  · {t("imageTokens")}:{" "}
                  {new Intl.NumberFormat(locale).format(
                    p.modality_usage.image_input_tokens,
                  )}
                </small>
              )}
            </>
          ) : (
            <>
              <span>
                {t("inputPrice")}
                <b>{rate(p.input_price)}</b>
              </span>
              <span>
                {t("outputPrice")}
                <b>{rate(p.output_price)}</b>
              </span>
              <span>
                {t("cachedInputPrice")}
                <b>{rate(p.cached_input_price)}</b>
              </span>
            </>
          )}
          {row.cache_write_tokens > 0 && (
            <>
              <span>
                {t("cacheWrite30m")}
                <b>{rate(p.cache_write_30m_price)}</b>
              </span>
              <span>
                {t("cacheWrite12h")}
                <b>{rate(p.cache_write_12h_price)}</b>
              </span>
            </>
          )}
          {p.long_context && <small>{t("longContextApplied")}</small>}
          {p.response_model && p.response_model !== row.model && (
            <small>
              {t("responseModel")}: {p.response_model}
            </small>
          )}
          {(p.reasons || []).map((reason: string) => (
            <small className="cost-note" key={reason}>
              {t(reason)}
            </small>
          ))}
          <small>{t("costEstimateHint")}</small>
        </div>
      </details>
      <span className="cost-line">
        <span className="token-input">↓</span> {fmt(row.input_cost)}{" "}
        <span className="token-output">↑</span> {fmt(row.output_cost)}
      </span>
      <small className="cost-label">
        {t(
          row.pricing_status === "duplicate"
            ? "usage_duplicate"
            : row.pricing_status === "partial"
              ? "partialEstimate"
              : row.pricing_status === "estimated"
                ? "estimatedCost"
                : "costUnavailable",
        )}
      </small>
    </div>
  );
}
