import { useEffect, useState } from "react";
type Props = {
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
};
export function UserManagement({ api, t }: Props) {
  const [users, setUsers] = useState<any[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [created, setCreated] = useState<any>(null),
    [confirm, setConfirm] = useState<any>(null);
  async function load() {
    try {
      setUsers((await api("users")).data);
    } catch (e) {
      setError(e instanceof Error ? e.message : "unexpected_error");
    }
  }
  useEffect(() => {
    void load();
  }, []);
  async function change(user: any, action: string, body: any = {}) {
    setBusy(true);
    setError("");
    try {
      const result = await api(
        `users/${user.id}` + (action === "update" ? "" : `/${action}`),
        {
          method: action === "update" ? "PATCH" : "POST",
          body: JSON.stringify(body),
        },
      );
      if (result.password)
        setCreated({ email: user.email, password: result.password });
      setConfirm(null);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "unexpected_error");
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      {error && (
        <div className="alert" role="alert">
          {t(error)}
        </div>
      )}
      <section className="panel account-panel">
        <h2>{t("createUser")}</h2>
        <form
          className="spending-range"
          onSubmit={async (e) => {
            e.preventDefault();
            const form = e.currentTarget;
            const data = Object.fromEntries(new FormData(form));
            setBusy(true);
            setError("");
            try {
              setCreated(
                await api("users", {
                  method: "POST",
                  body: JSON.stringify(data),
                }),
              );
              form.reset();
              await load();
            } catch (e) {
              setError(e instanceof Error ? e.message : "unexpected_error");
            } finally {
              setBusy(false);
            }
          }}
        >
          <label>
            {t("email")}
            <input type="email" name="email" required autoComplete="off" />
          </label>
          <label>
            {t("role")}
            <select name="role" defaultValue="user">
              <option value="user">{t("roleUser")}</option>
              <option value="admin">{t("roleAdmin")}</option>
            </select>
          </label>
          <button disabled={busy}>{t("createUser")}</button>
        </form>
      </section>
      <section className="data-panel">
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                {[
                  "email",
                  "role",
                  "status",
                  "otpStatus",
                  "privateKeyCount",
                  "actions",
                ].map((k) => (
                  <th key={k}>{t(k)}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {users.map((user) => (
                <tr key={user.id}>
                  <td>{user.email}</td>
                  <td>{t(user.role === "admin" ? "roleAdmin" : "roleUser")}</td>
                  <td>{t(user.enabled ? "enabled" : "disabled")}</td>
                  <td>{t(user.otp_bound ? "otpBound" : "otpUnbound")}</td>
                  <td>{user.key_count}</td>
                  <td>
                    <div className="row-actions">
                      <button
                        disabled={busy}
                        onClick={() =>
                          setConfirm({
                            user,
                            action: "update",
                            body: { enabled: !user.enabled },
                            label: user.enabled ? "disableUser" : "enableUser",
                          })
                        }
                      >
                        {t(user.enabled ? "disableUser" : "enableUser")}
                      </button>
                      <button
                        disabled={busy}
                        onClick={() =>
                          setConfirm({
                            user,
                            action: "update",
                            body: {
                              role: user.role === "admin" ? "user" : "admin",
                            },
                            label: "changeRole",
                          })
                        }
                      >
                        {t("changeRole")}
                      </button>
                      <button
                        disabled={busy}
                        onClick={() =>
                          setConfirm({
                            user,
                            action: "reset_password",
                            label: "resetPassword",
                          })
                        }
                      >
                        {t("resetPassword")}
                      </button>
                      <button
                        disabled={busy}
                        onClick={() =>
                          setConfirm({
                            user,
                            action: "reset_otp",
                            label: "resetOTP",
                          })
                        }
                      >
                        {t("resetOTP")}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      {created && (
        <div className="modal-overlay">
          <section className="modal form-modal" role="dialog" aria-modal="true">
            <h2>{t("initialPassword")}</h2>
            <p>{t("passwordOnceHint")}</p>
            <p>{created.email}</p>
            <input readOnly value={created.password} />
            <div className="modal-footer">
              <button
                onClick={() =>
                  void navigator.clipboard.writeText(created.password)
                }
              >
                {t("copy")}
              </button>
              <button onClick={() => setCreated(null)}>{t("close")}</button>
            </div>
          </section>
        </div>
      )}
      {confirm && (
        <div className="modal-overlay">
          <section className="modal form-modal" role="dialog" aria-modal="true">
            <h2>{t(confirm.label)}</h2>
            <p>{confirm.user.email}</p>
            <p>{t("accountActionHint")}</p>
            <div className="modal-footer">
              <button className="secondary" onClick={() => setConfirm(null)}>
                {t("cancel")}
              </button>
              <button
                disabled={busy}
                onClick={() =>
                  void change(confirm.user, confirm.action, confirm.body)
                }
              >
                {t("confirmAction")}
              </button>
            </div>
          </section>
        </div>
      )}
    </>
  );
}
export function AccountSettings({ api, t }: Props) {
  const [error, setError] = useState(""),
    [done, setDone] = useState(false),
    [busy, setBusy] = useState(false);
  return (
    <section className="panel account-panel">
      <h2>{t("changePassword")}</h2>
      <p>{t("changePasswordHint")}</p>
      {error && (
        <div className="alert" role="alert">
          {t(error)}
        </div>
      )}
      {done ? (
        <div>
          <p role="status">{t("passwordChanged")}</p>
          <button onClick={() => window.location.reload()}>
            {t("backToLogin")}
          </button>
        </div>
      ) : (
        <form
          className="account-form"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setError("");
            const form = e.currentTarget;
            try {
              await api("account/password", {
                method: "POST",
                body: JSON.stringify(Object.fromEntries(new FormData(form))),
              });
              form.reset();
              setDone(true);
            } catch (e) {
              setError(e instanceof Error ? e.message : "unexpected_error");
            } finally {
              setBusy(false);
            }
          }}
        >
          <label>
            {t("currentPassword")}
            <input
              name="current_password"
              type="password"
              required
              autoComplete="current-password"
            />
          </label>
          <label>
            {t("newPassword")}
            <input
              name="password"
              type="password"
              required
              minLength={12}
              maxLength={72}
              autoComplete="new-password"
            />
          </label>
          <label>
            {t("otpCode")}
            <input
              name="otp"
              inputMode="numeric"
              pattern="[0-9]{6}"
              autoComplete="one-time-code"
              required
            />
          </label>
          <button disabled={busy}>{t("save")}</button>
        </form>
      )}
    </section>
  );
}
