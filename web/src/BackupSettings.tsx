import { useEffect, useRef, useState } from "react";
import { Download, ShieldCheck, RefreshCw } from "lucide-react";
import { formatDateTime } from "./date-time.mjs";

type Policy = {
  enabled: boolean;
  frequency: string;
  run_time: string;
  weekday: number;
  retention_days: number;
  password_configured: boolean;
  next_run_at: string | null;
};
type Info = {
  available: boolean;
  storage_enabled: boolean;
  timezone: string;
  schedule: Policy;
  history: Record<string, any>[];
};
type Props = {
  api: (path: string, options?: RequestInit) => Promise<any>;
  download: (
    password: string,
    signal: AbortSignal,
  ) => Promise<{ object_key: string; warning: string }>;
  t: (key: string) => string;
  onUnauthorized: () => void;
};

export function BackupSettings({ api, download, t, onUnauthorized }: Props) {
  const [info, setInfo] = useState<Info | null>(null),
    [draft, setDraft] = useState<Policy | null>(null);
  const [password, setPassword] = useState(""),
    [confirmation, setConfirmation] = useState("");
  const [schedulePassword, setSchedulePassword] = useState(""),
    [scheduleConfirmation, setScheduleConfirmation] = useState("");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [objectKey, setObjectKey] = useState("");
  const mounted = useRef(true),
    controller = useRef<AbortController | null>(null);
  function fail(e: unknown) {
    if (!mounted.current) return;
    const code = e instanceof Error ? e.message : "backup_failed";
    setError(code);
    if (code === "unauthorized") onUnauthorized();
  }
  async function load(reset = false) {
    const result = await api("backups");
    if (mounted.current) {
      setInfo(result);
      setDraft((previous) => (reset || !previous ? result.schedule : previous));
    }
  }
  useEffect(() => {
    mounted.current = true;
    void load().catch(fail);
    const timer = setInterval(() => void load().catch(fail), 30000);
    return () => {
      mounted.current = false;
      clearInterval(timer);
      controller.current?.abort();
    };
  }, []);
  async function perform(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    setNotice("");
    setObjectKey("");
    try {
      await action();
      if (mounted.current) await load();
    } catch (e) {
      fail(e);
    } finally {
      if (mounted.current) setBusy(false);
    }
  }
  return (
    <section className="panel storage-settings">
      <div className="panel-heading">
        <h2>{t("siteBackup")}</h2>
        <ShieldCheck size={18} />
      </div>
      <p className="muted">{t("siteBackupHint")}</p>
      <p className="muted">{t("siteBackupScope")}</p>
      {error && (
        <p className="error-hint" role="alert">
          {t(error)}
        </p>
      )}
      {notice && (
        <p className="usage-copy-status" role="status">
          {t(notice)}
        </p>
      )}
      {objectKey && <code className="backup-object-key">{objectKey}</code>}
      {info?.available === false && (
        <p className="error-hint">{t("backup_tool_unavailable")}</p>
      )}
      {info && !info.storage_enabled && (
        <p className="muted">{t("backupS3Required")}</p>
      )}
      {draft && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void perform(async () => {
              if (schedulePassword !== scheduleConfirmation)
                throw Error("backupPasswordMismatch");
              if (
                draft.enabled &&
                !info?.schedule.password_configured &&
                !schedulePassword
              )
                throw Error("backup_schedule_password_required");
              const input: Record<string, any> = {
                enabled: draft.enabled,
                frequency: draft.frequency,
                run_time: draft.run_time,
                weekday: draft.weekday,
                retention_days: draft.retention_days,
              };
              if (schedulePassword) input.password = schedulePassword;
              await api("backups/settings", {
                method: "PATCH",
                body: JSON.stringify(input),
              });
              if (mounted.current) {
                setSchedulePassword("");
                setScheduleConfirmation("");
                setNotice("backupScheduleSaved");
                await load(true);
              }
            });
          }}
        >
          <fieldset
            className="key-routing-fields"
            disabled={busy || !info?.storage_enabled}
          >
            <legend>{t("backupSchedule")}</legend>
            <label className="routing-check">
              <input
                type="checkbox"
                checked={draft.enabled}
                onChange={(e) =>
                  setDraft({ ...draft, enabled: e.target.checked })
                }
              />
              {t("backupScheduleEnabled")}
            </label>
            <div className="proxy-address-fields">
              <label>
                {t("backupFrequency")}
                <select
                  value={draft.frequency}
                  onChange={(e) =>
                    setDraft({ ...draft, frequency: e.target.value })
                  }
                >
                  {["hourly", "daily", "weekly"].map((frequency) => (
                    <option key={frequency} value={frequency}>
                      {t("backupFrequency_" + frequency)}
                    </option>
                  ))}
                </select>
              </label>
              {draft.frequency !== "hourly" && (
                <label>
                  {t("backupRunTime")}
                  <input
                    type="time"
                    value={draft.run_time}
                    onChange={(e) =>
                      setDraft({ ...draft, run_time: e.target.value })
                    }
                    required
                  />
                </label>
              )}
              {draft.frequency === "weekly" && (
                <label>
                  {t("backupWeekday")}
                  <select
                    value={draft.weekday}
                    onChange={(e) =>
                      setDraft({ ...draft, weekday: Number(e.target.value) })
                    }
                  >
                    {[1, 2, 3, 4, 5, 6, 0].map((day) => (
                      <option key={day} value={day}>
                        {t("backupWeekday_" + day)}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              <label>
                {t("backupRetentionDays")}
                <input
                  type="number"
                  min="1"
                  max="3650"
                  step="1"
                  value={draft.retention_days}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      retention_days: Number(e.target.value),
                    })
                  }
                  required
                />
              </label>
            </div>
            <small>
              {t("backupScheduleHint")} {info?.timezone}
            </small>
            <small>{t("backupRetentionHint")}</small>
            <div className="proxy-address-fields">
              <label>
                {t("backupSchedulePassword")}
                <input
                  type="password"
                  minLength={12}
                  maxLength={1024}
                  autoComplete="new-password"
                  value={schedulePassword}
                  onChange={(e) => setSchedulePassword(e.target.value)}
                  placeholder={
                    info?.schedule.password_configured
                      ? t("proxyKeepPassword")
                      : ""
                  }
                  required={
                    draft.enabled && !info?.schedule.password_configured
                  }
                />
              </label>
              <label>
                {t("backupPasswordConfirm")}
                <input
                  type="password"
                  minLength={12}
                  maxLength={1024}
                  autoComplete="new-password"
                  value={scheduleConfirmation}
                  onChange={(e) => setScheduleConfirmation(e.target.value)}
                />
              </label>
            </div>
            <small>{t("backupSchedulePasswordHint")}</small>
            <small>
              {t("backupNextRun")}:{" "}
              {draft.enabled && !info?.schedule.password_configured
                ? t("backup_schedule_password_required")
                : draft.enabled && info?.schedule.next_run_at
                  ? formatDateTime(info.schedule.next_run_at)
                  : "—"}
            </small>
            <div className="storage-test">
              <button className="primary">{t("save")}</button>
              <button
                type="button"
                className="secondary"
                disabled={
                  !info?.schedule.password_configured || !info?.available
                }
                onClick={() =>
                  void perform(async () => {
                    const result = await api("backups/run", {
                      method: "POST",
                      body: "{}",
                    });
                    if (mounted.current) {
                      setNotice("backupUploaded");
                      setObjectKey(result.object_key);
                      if (result.cleanup_error) setError(result.cleanup_error);
                    }
                  })
                }
              >
                {t("backupRunNow")}
              </button>
            </div>
          </fieldset>
        </form>
      )}
      <details className="backup-restore-help">
        <summary>{t("backupManualDownload")}</summary>
        <form
          className="backup-form"
          onSubmit={(e) => {
            e.preventDefault();
            void perform(async () => {
              if (password !== confirmation)
                throw Error("backupPasswordMismatch");
              const abort = new AbortController();
              controller.current = abort;
              const result = await download(password, abort.signal);
              if (mounted.current) {
                setPassword("");
                setConfirmation("");
                setNotice(
                  result.object_key
                    ? "backupUploadedAndDownloaded"
                    : "backupDownloaded",
                );
                setObjectKey(result.object_key);
                if (result.warning) setError(result.warning);
              }
            });
          }}
        >
          <div className="proxy-address-fields">
            <label>
              {t("backupPassword")}
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                minLength={12}
                maxLength={1024}
                autoComplete="new-password"
                required
                disabled={busy}
              />
            </label>
            <label>
              {t("backupPasswordConfirm")}
              <input
                type="password"
                value={confirmation}
                onChange={(e) => setConfirmation(e.target.value)}
                minLength={12}
                maxLength={1024}
                autoComplete="new-password"
                required
                disabled={busy}
              />
            </label>
          </div>
          <small className="muted">{t("backupPasswordHint")}</small>
          <small className="muted">{t("backupManualPasswordHint")}</small>
          <div className="storage-test">
            <button className="primary" disabled={busy || !info?.available}>
              <Download size={16} />
              {t(busy ? "backupGenerating" : "backupDownload")}
            </button>
          </div>
        </form>
      </details>
      {info && (
        <details className="backup-restore-help">
          <summary>{t("backupHistory")}</summary>
          <button
            className="text-button"
            type="button"
            onClick={() => void load().catch(fail)}
          >
            <RefreshCw size={14} />
            {t("refresh")}
          </button>
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  {["created_at", "kind", "status", "backupObjectPath"].map(
                    (field) => (
                      <th key={field}>{t(field)}</th>
                    ),
                  )}
                </tr>
              </thead>
              <tbody>
                {info.history.map((row) => (
                  <tr key={row.id}>
                    <td>{formatDateTime(row.created_at)}</td>
                    <td>{t("backupTrigger_" + row.trigger)}</td>
                    <td>
                      {t("backupStatus_" + row.status)}
                      {row.error && (
                        <small className="error-hint">{t(row.error)}</small>
                      )}
                      {row.cleanup_error && (
                        <small className="error-hint">
                          {t(row.cleanup_error)}
                        </small>
                      )}
                    </td>
                    <td>
                      <code className="backup-object-key">
                        {row.object_key || "—"}
                      </code>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </details>
      )}
      <details className="backup-restore-help">
        <summary>{t("backupRestoreTitle")}</summary>
        <ol>
          {[1, 2, 3, 4].map((step) => (
            <li key={step}>{t("backupRestoreStep" + step)}</li>
          ))}
        </ol>
        <p className="muted">{t("backupRestoreCaveat")}</p>
      </details>
    </section>
  );
}
