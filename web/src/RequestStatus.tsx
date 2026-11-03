export function RequestStatus({
  row,
  t,
}: {
  row: Record<string, any>;
  t: (key: string) => string;
}) {
  const status = (
    <span className={`http-status ${row.status < 400 ? "success" : "failed"}`}>
      {row.status}
    </span>
  );
  const error = row.pricing_snapshot?.upstream_error;
  if (row.status < 400 || (!error?.message && !error?.param && !row.error_code))
    return status;
  return (
    <details className="request-error-details">
      <summary aria-label={`${t("requestErrorDetails")} ${row.status}`}>
        {status}
        <span className="request-error-hint">{t("requestErrorDetails")}</span>
      </summary>
      <div className="request-error-body">
        {row.error_code && (
          <p>
            <strong>{t("error_code")}: </strong>
            <code>{row.error_code}</code>
          </p>
        )}
        {error?.param && (
          <p>
            <strong>{t("requestErrorParam")}: </strong>
            <code>{error.param}</code>
          </p>
        )}
        <p>{error?.message || t("requestErrorMessageUnavailable")}</p>
        {row.pricing_snapshot?.prompt_cache_policy && (
          <p>
            {t("keyPromptCache")}: {row.pricing_snapshot.prompt_cache_policy}
            {row.pricing_snapshot.prompt_cache_ttl
              ? ` · ${row.pricing_snapshot.prompt_cache_ttl}`
              : ""}
          </p>
        )}
      </div>
    </details>
  );
}
