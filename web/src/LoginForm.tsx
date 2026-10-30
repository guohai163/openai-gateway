import { useEffect, useState } from "react";
import QRCode from "qrcode";
type Props = {
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
  onLogin: (value: any) => void;
};
export function LoginForm({ api, t, onLogin }: Props) {
  const [challenge, setChallenge] = useState<any>(null),
    [qr, setQR] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    let cancelled = false;
    setQR("");
    if (challenge?.otpauth_url)
      QRCode.toDataURL(challenge.otpauth_url, { width: 220, margin: 2 })
        .then((value) => {
          if (!cancelled) setQR(value);
        })
        .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [challenge]);
  return (
    <form
      className="login-card"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError("");
        const data = Object.fromEntries(new FormData(e.currentTarget));
        try {
          const result = await api(challenge ? "mfa" : "login", {
            method: "POST",
            body: JSON.stringify(data),
          });
          if (result.mfa_required) setChallenge(result);
          else onLogin(result);
        } catch (e) {
          const message = e instanceof Error ? e.message : "unexpected_error";
          setError(message);
          if (message === "challenge_expired") setChallenge(null);
        } finally {
          setBusy(false);
        }
      }}
    >
      <h1>
        {t(
          challenge
            ? challenge.enroll
              ? "bindOTP"
              : "verifyOTP"
            : "loginTitle",
        )}
      </h1>
      <p>
        {t(
          challenge
            ? challenge.enroll
              ? "bindOTPHint"
              : "verifyOTPHint"
            : "multiuserLoginHint",
        )}
      </p>
      {error && (
        <div className="alert" role="alert">
          {t(error)}
        </div>
      )}
      {challenge ? (
        <>
          {challenge.enroll && (
            <div className="otp-enrollment">
              {qr && <img src={qr} alt={t("otpQRCode")} />}
              <label>
                {t("otpSecret")}
                <input readOnly value={challenge.secret} />
              </label>
              <small>{t("otpSecretHint")}</small>
            </div>
          )}
          <label>
            {t("otpCode")}
            <input
              name="otp"
              autoFocus
              autoComplete="one-time-code"
              inputMode="numeric"
              pattern="[0-9]{6}"
              minLength={6}
              maxLength={6}
              required
            />
          </label>
        </>
      ) : (
        <>
          <label>
            {t("loginIdentifier")}
            <input name="username" autoComplete="username" autoFocus required />
          </label>
          <label>
            {t("password")}
            <input
              name="password"
              type="password"
              autoComplete="current-password"
              required
            />
          </label>
        </>
      )}
      <button className="primary" disabled={busy}>
        {t(busy ? "loading" : challenge ? "verifyOTP" : "login")}
      </button>
      {challenge && (
        <button
          type="button"
          className="text-button"
          onClick={() => {
            setChallenge(null);
            setError("");
          }}
        >
          {t("backToLogin")}
        </button>
      )}
    </form>
  );
}
