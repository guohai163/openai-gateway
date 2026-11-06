import { useId, useState } from "react";

type Platform = "openai" | "bailian";
type Translate = (key: string) => string;
const platforms: Platform[] = ["openai", "bailian"];

export function PlatformIdentity({
  platform,
  t,
}: {
  platform: string;
  t: Translate;
}) {
  if (!platforms.includes(platform as Platform))
    return <span>{platform || "—"}</span>;
  return (
    <span className="platform-identity">
      <img
        src={
          platform === "bailian" ? "/bailian-icon.svg" : "/openai-platform.svg"
        }
        alt=""
        aria-hidden="true"
      />
      <span>{t(platform)}</span>
    </span>
  );
}

export function PlatformSelector({
  value,
  defaultValue = "openai",
  onChange,
  name,
  disabled = false,
  label,
  t,
}: {
  value?: string;
  defaultValue?: string;
  onChange?: (value: Platform) => void;
  name?: string;
  disabled?: boolean;
  label: string;
  t: Translate;
}) {
  const id = useId();
  const [selected, setSelected] = useState(defaultValue);
  const current = value ?? selected;
  return (
    <fieldset className="platform-selector" disabled={disabled}>
      <legend>{label}</legend>
      <div className="platform-selector-options">
        {platforms.map((platform) => (
          <label className="platform-selector-option" key={platform}>
            <input
              type="radio"
              name={name || id}
              value={platform}
              checked={current === platform}
              onChange={() => {
                setSelected(platform);
                onChange?.(platform);
              }}
            />
            <span className="platform-selector-card">
              <PlatformIdentity platform={platform} t={t} />
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}
