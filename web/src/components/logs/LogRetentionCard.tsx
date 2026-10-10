import { useCallback, useEffect, useState } from "react";
import { CheckmarkRegular, HistoryRegular } from "@fluentui/react-icons";
import { apiMessage, getLoggingSettings, updateLoggingSettings } from "../../api";
import type { LoggingSettings } from "../../types";
import { useI18n } from "../../lib/i18n";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Select } from "../ui/Select";
import { message } from "../ui/message";

type RetentionMode = LoggingSettings["mode"];
type RetentionLevel = LoggingSettings["level"];

// 运行日志保留策略：默认不限制，可按条数或天数限制，并可按最低日志级别保留。
export function LogRetentionCard({ refreshKey = 0 }: { refreshKey?: number }) {
  const { t } = useI18n();
  const [mode, setMode] = useState<RetentionMode>("unlimited");
  const [count, setCount] = useState(10000);
  const [days, setDays] = useState(30);
  const [level, setLevel] = useState<RetentionLevel>("debug");
  const [storedLogs, setStoredLogs] = useState(0);
  const [maxLogs, setMaxLogs] = useState(10000);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const apply = useCallback((data: LoggingSettings) => {
    setMode(data.mode);
    setCount(data.count);
    setDays(data.days);
    setLevel(data.level || "debug");
    setStoredLogs(data.storedLogs);
    setMaxLogs(data.maxLogs || 10000);
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getLoggingSettings()
      .then((data) => {
        if (!cancelled) apply(data);
      })
      .catch(() => message.error(t("日志保留策略加载失败")))
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [apply, refreshKey]);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      const data = await updateLoggingSettings({
        mode,
        count: Math.min(maxLogs, Math.max(1, Math.trunc(count) || 1)),
        days: Math.max(1, Math.trunc(days) || 1),
        level,
      });
      apply(data);
      message.success(t("日志保留策略已保存"));
    } catch (error) {
      message.error(apiMessage(error) || t("日志保留策略保存失败"));
    } finally {
      setSaving(false);
    }
  }, [mode, count, days, level, maxLogs, apply]);

  const onNumber = (setter: (value: number) => void) => (event: React.ChangeEvent<HTMLInputElement>) => {
    const parsed = parseInt(event.target.value, 10);
    setter(Number.isNaN(parsed) ? 0 : parsed);
  };

  return (
    <div className="ui-card p-4 mb-4">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-3">
        <div className="flex items-center gap-2 text-sm font-medium text-gray-700 dark:text-gray-200">
          <HistoryRegular className="text-gray-400" />
          {t("日志保留")}
        </div>
        <Select
          value={mode}
          onChange={(value) => setMode(value as RetentionMode)}
          className="w-32"
          disabled={loading}
          options={[
            { value: "unlimited", label: t("最多 10000 条") },
            { value: "count", label: t("按条数") },
            { value: "days", label: t("按天数") },
          ]}
        />
        {mode === "count" ? (
          <label className="flex items-center gap-2">
            <Input
              type="number"
              min={1}
              max={maxLogs}
              value={count === 0 ? "" : count}
              onChange={onNumber(setCount)}
              disabled={loading}
              className="w-28"
            />
            <span className="text-sm text-gray-500 dark:text-gray-400">{t("条")}</span>
          </label>
        ) : null}
        {mode === "days" ? (
          <label className="flex items-center gap-2">
            <Input
              type="number"
              min={1}
              value={days === 0 ? "" : days}
              onChange={onNumber(setDays)}
              disabled={loading}
              className="w-28"
            />
            <span className="text-sm text-gray-500 dark:text-gray-400">{t("天")}</span>
          </label>
        ) : null}
        <div className="flex items-center gap-1.5">
          <span className="text-xs text-gray-400">{t("类型保留")}</span>
          <Select
            value={level}
            onChange={(value) => setLevel(value as RetentionLevel)}
            className="w-44"
            disabled={loading}
            options={[
              { value: "debug", label: t("DEBUG 及以上") },
              { value: "info", label: t("INFO 及以上") },
              { value: "warn", label: t("WARN 及以上") },
              { value: "error", label: t("ERROR (仅严重报错)") },
            ]}
          />
        </div>
        <span className="text-sm text-gray-400">
          {t("当前已存储")} {storedLogs} / {maxLogs} {t("条")}
        </span>
        <span className="text-xs text-gray-400">{t("达到上限后自动删除最旧日志")}</span>
        <div className="flex-1" />
        <Button
          size="small"
          variant="primary"
          className="!border-0"
          loading={saving}
          disabled={loading}
          onClick={save}
          icon={<CheckmarkRegular />}
        >
          {t("保存")}
        </Button>
      </div>
    </div>
  );
}
