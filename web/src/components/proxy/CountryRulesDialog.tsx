import { SearchRegular } from "@fluentui/react-icons";
import { useEffect, useMemo, useState } from "react";
import type { Country, CountryRule, UpstreamProxy } from "../../types";
import { Button, EmptyState, Input, Modal, Select } from "../ui";
import { useI18n } from "../../lib/i18n";

export interface CountryRulesDialogProps {
  open: boolean;
  proxies: UpstreamProxy[];
  countries: Country[];
  rules: CountryRule[];
  busy: boolean;
  onSave: (assignments: Record<string, string>) => void;
  onClose: () => void;
}

export function CountryRulesDialog(props: CountryRulesDialogProps) {
  const { t, lang } = useI18n();
  const { open, proxies, countries, rules, busy, onSave, onClose } = props;
  const [query, setQuery] = useState("");
  const [assignments, setAssignments] = useState<Record<string, string>>({});
  const regionNames = useMemo(() => {
    try {
      return new Intl.DisplayNames([lang === "zh" ? "zh-CN" : "en"], { type: "region" });
    } catch {
      return null;
    }
  }, [lang]);
  const countryLabel = (country: Country) => regionNames?.of(country.countryCode) || country.countryName || country.countryCode;
  const proxyOptions = useMemo(() => [
    { value: "", label: t("直连") },
    ...proxies.map((proxy) => ({
      value: proxy.id,
      label: proxy.enabled ? (proxy.name || proxy.id) : `${proxy.name || proxy.id}（${t("已禁用")}）`,
      disabled: !proxy.enabled,
    })),
  ], [proxies, t, lang]);

  useEffect(() => {
    if (!open) {
      setQuery("");
      setAssignments({});
      return;
    }
    setAssignments(Object.fromEntries(rules.filter((rule) => rule.enabled).map((rule) => [rule.countryCode, rule.upstreamProxyId])));
    // Sample rules only when opening. Polling must not discard in-progress edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const filtered = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    return [...countries]
      .sort((a, b) => countryLabel(a).localeCompare(countryLabel(b), lang === "zh" ? "zh-CN" : "en"))
      .filter((country) => {
        if (!needle) return true;
        return [country.countryCode, country.countryName, countryLabel(country), ...country.mccs]
          .some((value) => String(value || "").toLocaleLowerCase().includes(needle));
      });
  }, [countries, query, lang, regionNames]);

  const configuredCount = Object.values(assignments).filter(Boolean).length;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t("MCC 国家规则")}
      footer={(
        <>
          <Button onClick={onClose} disabled={busy}>{t("取消")}</Button>
          <Button variant="primary" loading={busy} onClick={() => onSave(assignments)}>{t("保存规则")}</Button>
        </>
      )}
    >
      <div className="space-y-4 pb-1">
        <div className="rounded-lg border border-sky-200/70 bg-sky-50 px-3 py-2 text-xs leading-5 text-sky-800 dark:border-sky-800/50 dark:bg-sky-900/20 dark:text-sky-200">
          {t("为每个国家的 MCC 选择代理。未配置时直连；已有 ICCID 绑定始终优先，首次命中国家规则后会生成独立的 ICCID 绑定。")}
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="text-xs text-gray-500">{configuredCount} {t("个国家规则")}</div>
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("搜索国家、地区代码或 MCC")}
            prefix={<SearchRegular />}
            className="w-full sm:w-72"
          />
        </div>
        <div className="overflow-hidden rounded-xl border border-gray-100 dark:border-white/10">
          <div className="max-h-[55vh] overflow-auto">
            <table className="w-full min-w-[680px] text-left text-sm">
              <thead className="sticky top-0 z-10 bg-gray-50 text-xs uppercase tracking-wide text-gray-500 dark:bg-[#202027]">
                <tr>
                  <th className="px-4 py-3">{t("国家 / 地区")}</th>
                  <th className="px-4 py-3">MCC</th>
                  <th className="w-72 px-4 py-3">{t("规则")}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-white/10">
                {filtered.map((country) => (
                  <tr key={country.countryCode} className="hover:bg-sky-50/40 dark:hover:bg-sky-500/[0.04]">
                    <td className="px-4 py-3">
                      <span className="font-medium">{countryLabel(country)}</span>
                      <span className="ml-2 font-mono text-xs text-gray-400">{country.countryCode}</span>
                    </td>
                    <td className="px-4 py-3 font-mono text-xs text-gray-600 dark:text-gray-300">{country.mccs.join(", ")}</td>
                    <td className="px-4 py-2">
                      <Select
                        value={assignments[country.countryCode] || ""}
                        options={proxyOptions}
                        disabled={busy}
                        onChange={(value) => setAssignments((current) => ({ ...current, [country.countryCode]: value }))}
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {filtered.length === 0 ? <EmptyState title={t("没有匹配的国家或 MCC")} /> : null}
        </div>
      </div>
    </Modal>
  );
}
