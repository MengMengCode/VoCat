import { Button } from "../ui";
import type { OperatorCandidate } from "./types";
import { useI18n } from "../../lib/i18n";
import { CountryFlag } from "../CountryFlag";

function ratsText(c: OperatorCandidate): string {
  const list = (c.rats || []).filter(Boolean) as string[];
  return list.length ? list.map((r) => r.toUpperCase()).join(" / ") : "--";
}

export function CandidateRow({ candidate, onLock }: { candidate: OperatorCandidate; onLock: (c: OperatorCandidate) => void }) {
  const { t } = useI18n();
  const c = candidate;
  const forbidden = c.status === "forbidden";
  return (
    <div
      className={forbidden
        ? "flex cursor-not-allowed items-center justify-between bg-red-50/40 p-3 opacity-70 dark:bg-red-500/5"
        : "group flex cursor-pointer items-center justify-between p-3 transition-colors hover:bg-gray-50 dark:hover:bg-white/5"}
      onClick={() => { if (!forbidden) onLock(c); }}
    >
      <div>
        <div className="flex items-center gap-2 font-medium text-gray-900 dark:text-white">
          <CountryFlag countryCode={c.countryCode} />
          {c.operatorName || c.shortName || t("未知网络")}{" "}
          {c.status === "current" ? (
            <span className="rounded-full border border-emerald-200 bg-emerald-100 px-1.5 py-0.5 text-[10px] font-bold text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/20 dark:text-emerald-300">
              {t("当前")}
            </span>
          ) : c.status === "forbidden" ? (
            <span className="rounded-full border border-red-200 bg-red-100 px-1.5 py-0.5 text-[10px] font-bold text-red-700 dark:border-red-500/30 dark:bg-red-500/20 dark:text-red-300">
              {t("禁用")}
            </span>
          ) : null}
        </div>
        <div className="mt-0.5 font-mono text-xs text-gray-500 dark:text-gray-400">
          {c.plmn} • {ratsText(c)}
        </div>
      </div>
      <div>
        <Button variant="text" size="small" disabled={forbidden} className={forbidden ? "opacity-70" : "opacity-0 transition-opacity group-hover:opacity-100"}>
          {forbidden ? t("不可注册") : t("锁定")}
        </Button>
      </div>
    </div>
  );
}
