import { useState } from "react";
import { Check, Database } from "lucide-react";
export function StorageSettings({
  storage,
  api,
  t,
}: {
  storage: Record<string, string>;
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
}) {
  const [busy, setBusy] = useState(false),
    [state, setState] = useState("");
  return (
    <section className="panel storage-settings">
      <div className="panel-heading">
        <h2>{t("s3Storage")}</h2>
        <Database size={18} />
      </div>
      <div className="settings-list">
        {Object.entries(storage).map(([key, value]) => (
          <div key={key}>
            <span>{t("s3_" + key)}</span>
            <code>{value}</code>
          </div>
        ))}
      </div>
      <div className="storage-test">
        <button
          className="secondary"
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            setState("");
            try {
              await api("storage/test", { method: "POST", body: "{}" });
              setState("storageTestPassed");
            } catch (e) {
              setState(e instanceof Error ? e.message : "unexpected_error");
            } finally {
              setBusy(false);
            }
          }}
        >
          {t(busy ? "loading" : "testStorage")}
        </button>
        {state && (
          <p
            role="status"
            className={
              state === "storageTestPassed" ? "usage-copy-status" : "error-hint"
            }
          >
            {state === "storageTestPassed" && <Check size={14} />} {t(state)}
          </p>
        )}
      </div>
    </section>
  );
}
