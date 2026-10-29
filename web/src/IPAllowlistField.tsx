import { useEffect, useRef, useState } from "react";

type Props = {
  editing: boolean;
  savedCIDRs?: string[];
  request: (
    path: string,
  ) => Promise<{ client_ip: string; default_cidr: string }>;
  t: (key: string) => string;
};

export function IPAllowlistField({ editing, savedCIDRs, request, t }: Props) {
  const [value, setValue] = useState(() => savedCIDRs?.join("\n") || "");
  const [loading, setLoading] = useState(!editing);
  const [sourceIP, setSourceIP] = useState("");
  const [failed, setFailed] = useState(false);
  const touched = useRef(false);
  const zeroHostOnly = value
    .split(/[\n,]/)
    .some((cidr) =>
      ["0.0.0.0", "0.0.0.0/32", "::", "::/128"].includes(cidr.trim()),
    );

  useEffect(() => {
    if (editing) return;
    let cancelled = false;
    request("client_ip")
      .then((result) => {
        if (!cancelled && !touched.current) {
          setValue(result.default_cidr);
          setSourceIP(result.client_ip);
        }
      })
      .catch(() => {
        if (!cancelled && !touched.current) setFailed(true);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [editing, request]);

  return (
    <label>
      {t("cidrs")}
      <textarea
        name="cidrs"
        rows={4}
        required
        value={value}
        placeholder={loading ? t("detectingClientIP") : ""}
        onChange={(event) => {
          touched.current = true;
          setValue(event.target.value);
          setSourceIP("");
          setFailed(false);
        }}
      />
      <small>{t("cidrHint")}</small>
      {zeroHostOnly && (
        <small className="cidr-scope-hint" role="status">
          {t("zeroHostCIDRHint")}
        </small>
      )}
      {!editing && (loading || failed || sourceIP) && (
        <small role="status">
          {loading
            ? t("detectingClientIP")
            : failed
              ? t("client_ip_unavailable")
              : t("defaultCIDRHint").replace("{ip}", sourceIP)}
        </small>
      )}
    </label>
  );
}
