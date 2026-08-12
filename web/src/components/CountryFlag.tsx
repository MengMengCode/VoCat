import { cx } from "../lib/utils";

export interface CountryFlagProps {
  countryCode?: string;
  className?: string;
}

const FLAG_CODE_ALIASES: Record<string, string> = {
  // The offline carrier table still contains the retired Netherlands Antilles
  // code for Curacao networks. flag-icons follows the current ISO list.
  an: "cw",
};

function normalizedCountryCode(countryCode?: string): string {
  const code = String(countryCode ?? "").trim().toLowerCase();
  if (!/^[a-z]{2}$/.test(code)) return "";
  return FLAG_CODE_ALIASES[code] ?? code;
}

// Render a bundled SVG instead of a flag emoji. Some platforms display the
// regional-indicator characters behind flag emoji as a plain country code.
export function CountryFlag({ countryCode, className }: CountryFlagProps) {
  const code = normalizedCountryCode(countryCode);
  if (!code) return null;

  return (
    <img
      key={code}
      src={`/flags/${code}.svg`}
      alt=""
      aria-hidden="true"
      draggable={false}
      className={cx(
        "inline-block h-[0.9em] w-[1.2em] shrink-0 rounded-[1px] object-cover align-[-0.08em] shadow-[0_0_0_1px_rgba(0,0,0,0.12)]",
        className,
      )}
      onError={(event) => {
        event.currentTarget.hidden = true;
      }}
    />
  );
}
