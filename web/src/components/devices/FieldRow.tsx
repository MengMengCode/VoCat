import type { ReactNode } from "react";
import { cx } from "../../lib/utils";
import { copyText } from "./shared";
import { useI18n } from "../../lib/i18n";

export interface FieldRowProps {
  label: ReactNode;
  value?: unknown;
  prefix?: ReactNode;
  copyable?: boolean;
  monospace?: boolean;
  placeholder?: string;
  sensitive?: boolean;
}

// Label/value row used across the overview tab. Click to copy when copyable.
export function FieldRow({ label, value, prefix, copyable, monospace, placeholder, sensitive }: FieldRowProps) {
  const { t } = useI18n();
  const display = (value == null ? "" : String(value)).trim() || placeholder || "--";
  const canCopy = !!copyable && !!display && display !== "--" && display !== "---";
  const title = sensitive ? "" : display === "--" || display === "---" ? "" : display;

  async function handleCopy() {
    if (canCopy) await copyText(display, t("已复制"));
  }

  return (
    <div className="flex w-full min-w-0 items-center justify-between gap-3 overflow-hidden">
      <span className="shrink-0 whitespace-nowrap text-gray-500">{label}</span>
      <span
        className={cx(
          "flex min-w-0 max-w-full flex-1 items-center justify-end gap-1.5 text-right",
          monospace && "font-mono",
          canCopy && "cursor-pointer hover:underline",
          sensitive && "select-none blur-sm transition-all",
        )}
        title={title}
        onClick={handleCopy}
      >
        {prefix}
        <span className="min-w-0 truncate">{display}</span>
      </span>
    </div>
  );
}
