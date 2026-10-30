import { useState } from "react";
import { LoaderCircle, Search } from "lucide-react";

type Props = {
  logId: string;
  ip: string;
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
};
export function ClientIPDetails({ logId, ip, api, t }: Props) {
  const [details, setDetails] = useState<Record<string, any> | null>(null),
    [loading, setLoading] = useState(false),
    [error, setError] = useState("");
  return (
    <div className="client-ip-details">
      <div className="client-ip-query">
        <span>{ip || "—"}</span>
        {ip && (
          <button
            type="button"
            className="text-button"
            disabled={loading}
            aria-label={`${t("lookupIP")} ${ip}`}
            onClick={async () => {
              setLoading(true);
              setError("");
              try {
                setDetails(
                  await api(`logs/${encodeURIComponent(logId)}/ip_lookup`, {
                    method: "POST",
                    body: "{}",
                  }),
                );
              } catch (e) {
                setError(e instanceof Error ? e.message : "ip_lookup_failed");
              } finally {
                setLoading(false);
              }
            }}
          >
            {loading ? <LoaderCircle size={12} /> : <Search size={12} />}{" "}
            {t(loading ? "loading" : "lookupIP")}
          </button>
        )}
      </div>
      {details &&
        (details.local ? (
          <small className="muted">{t("localIP")}</small>
        ) : (
          <dl className="client-ip-result">
            {["country", "city", "asn", "asn_org"].map((field) => (
              <div key={field}>
                <dt>{t("ip_" + field)}</dt>
                <dd>{details[field] || "—"}</dd>
              </div>
            ))}
          </dl>
        ))}
      {error && (
        <small className="error-hint" role="alert">
          {t(error)}
        </small>
      )}
    </div>
  );
}
