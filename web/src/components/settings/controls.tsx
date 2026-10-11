import { useState, type ReactNode } from "react";
import { AddRegular, CheckmarkRegular, DeleteRegular, EyeOffRegular, EyeRegular } from "@fluentui/react-icons";
import { tl, useI18n } from "../../lib/i18n";
import { cx } from "../../lib/utils";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Switch } from "../ui/Switch";
import { message } from "../ui/message";
import type { DeviceListItem } from "../../types";

const LABEL_CLASS = "text-xs font-bold text-gray-500 uppercase tracking-wider";

// 带可见性切换的密码输入框（对应参考里的 show-password）。
export function PasswordInput({
  value,
  onChange,
  disabled,
  placeholder,
  inputSize,
  autoComplete,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  placeholder?: string;
  inputSize?: "default" | "large";
  autoComplete?: string;
}) {
  const { t } = useI18n();
  const [visible, setVisible] = useState(false);
  return (
    <Input
      type={visible ? "text" : "password"}
      value={value}
      onChange={(event) => onChange(event.target.value)}
      disabled={disabled}
      placeholder={placeholder}
      inputSize={inputSize}
      autoComplete={autoComplete}
      suffix={
        <button
          type="button"
          tabIndex={-1}
          aria-label={visible ? t("隐藏密码") : t("显示密码")}
          onClick={() => setVisible((prev) => !prev)}
          className="pointer-events-auto flex items-center text-gray-400 transition-colors hover:text-gray-600 dark:hover:text-gray-300"
        >
          {visible ? <EyeOffRegular className="h-4 w-4" /> : <EyeRegular className="h-4 w-4" />}
        </button>
      }
    />
  );
}

// 复制到剪贴板（对应参考里的 copy 工具）。
export async function copyText(value: unknown, tip?: string): Promise<boolean> {
  const tipText = tip ?? tl("已复制");
  const text = String(value ?? "").trim();
  if (!text) return false;
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      message.success(tipText);
      return true;
    }
  } catch {
    /* fallthrough */
  }
  try {
    const el = document.createElement("textarea");
    el.value = text;
    el.setAttribute("readonly", "true");
    el.className = "fixed left-0 top-0 h-px w-px opacity-0 pointer-events-none";
    document.body.appendChild(el);
    el.focus();
    el.select();
    el.setSelectionRange(0, el.value.length);
    const ok = document.execCommand("copy");
    document.body.removeChild(el);
    if (ok) {
      message.success(tipText);
      return true;
    }
  } catch {
    /* fallthrough */
  }
  message.warning(tl("浏览器限制，复制失败，请手动复制"));
  return false;
}

// FieldRow：label 在左、值在右（等宽/可复制），对应参考里的 FieldRow 组件。
export function FieldRow({
  label,
  value,
  monospace,
  copyable,
  placeholder,
  children,
}: {
  label: ReactNode;
  value?: unknown;
  monospace?: boolean;
  copyable?: boolean;
  placeholder?: string;
  children?: ReactNode;
}) {
  const text = (value === null || value === undefined ? "" : String(value)).trim() || placeholder || "--";
  const canCopy = !!copyable && !!text && text !== "--" && text !== "---";
  const title = text === "--" || text === "---" ? "" : text;
  return (
    <div className="flex w-full min-w-0 items-center justify-between gap-3 overflow-hidden">
      <span className="shrink-0 whitespace-nowrap text-gray-500">{label}</span>
      <div
        className={cx(
          "block min-w-0 max-w-full flex-1 truncate text-right",
          monospace && "font-mono",
          canCopy && "cursor-pointer hover:underline",
        )}
        title={children ? undefined : title}
        onClick={children ? undefined : () => void (canCopy && copyText(text))}
      >
        {children ?? text}
      </div>
    </div>
  );
}

// Field：label + 控件 + 可选说明，对应参考里反复出现的 space-y-1 字段块。
export function Field({
  label,
  hint,
  className,
  children,
}: {
  label: ReactNode;
  hint?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cx("space-y-1", className)}>
      <label className={LABEL_CLASS}>{label}</label>
      {children}
      {hint ? <div className="mt-1 text-[10px] text-gray-400">{hint}</div> : null}
    </div>
  );
}

// 渠道头部：左侧标题，右侧可选操作 + 启用开关。
export function ChannelHeader({
  title,
  enabled,
  onToggle,
  actions,
  onClear,
}: {
  title: ReactNode;
  enabled: boolean;
  onToggle: (enabled: boolean) => void;
  actions?: ReactNode;
  onClear?: () => void;
}) {
  return (
    <div className="mb-4 flex items-center justify-between">
      <div className="flex items-center gap-2">
        <div className="font-bold text-gray-800 dark:text-gray-100">{title}</div>
      </div>
      <div className="flex items-center gap-2">
        {onClear ? <Button size="small" variant="danger" plain onClick={onClear}>{tl("清空配置")}</Button> : null}
        {actions}
        <Switch checked={enabled} onChange={onToggle} />
      </div>
    </div>
  );
}

// 空态虚线提示行。
export function EmptyLine({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-gray-200 bg-gray-50/30 py-2 text-center text-xs text-gray-400 dark:border-white/10 dark:bg-white/5">
      {children}
    </div>
  );
}

// 分段式选项卡（对应参考里的 .settings-notify-tabs：圆角胶囊容器 + 高亮活动项）。
export function SegmentedTabs({
  tabs,
  value,
  onChange,
}: {
  tabs: { key: string; label: ReactNode }[];
  value: string;
  onChange: (key: string) => void;
}) {
  return (
    <div className="mb-6 inline-flex max-w-full items-center gap-1 overflow-x-auto rounded-xl bg-gray-100 p-1 dark:bg-white/5">
      {tabs.map((tab) => {
        const active = tab.key === value;
        return (
          <button
            key={tab.key}
            type="button"
            onClick={() => onChange(tab.key)}
            className={cx(
              "h-[38px] shrink-0 whitespace-nowrap rounded-lg px-5 text-sm font-medium outline-none transition-all duration-300",
              active
                ? "bg-white font-semibold text-[#0ea5e9] shadow-[0_1px_4px_rgba(0,0,0,0.05),0_2px_8px_rgba(0,0,0,0.03)] dark:bg-white/10 dark:text-[#7dd3fc]"
                : "text-gray-500 hover:text-[#0ea5e9] dark:text-gray-400 dark:hover:text-[#7dd3fc]",
            )}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}

export function NotificationDeviceScope({ value, onChange, devices, failed }: {
  value: string[] | null;
  onChange: (ids: string[] | null) => void;
  devices: Pick<DeviceListItem, "id" | "name">[] | null;
  failed: boolean;
}) {
  const { t } = useI18n();
  const available = devices ?? [];
  // Unknown/offline IDs remain selectable; loading never rewrites the draft.
  const choices = [...available, ...(value ?? []).filter((id) => !available.some((device) => device.id === id))
    .map((id) => ({ id, name: "" }))];
  return (
    <div className="mt-6 space-y-3 border-t border-gray-200 pt-4 text-sm dark:border-white/10">
      <div className="flex items-center justify-between gap-4">
        <div>
          <div className="font-medium text-gray-800 dark:text-gray-100">{t("仅推送指定设备")}</div>
          <p className="mt-1 text-xs text-gray-500">{t("关闭时推送全部设备的通知。")}</p>
        </div>
        <Switch checked={value !== null} onChange={enabled => onChange(enabled ? [] : null)} ariaLabel={t("仅推送指定设备")} />
      </div>
      {value !== null ? <>
        {failed ? <p className="text-xs text-amber-600 dark:text-amber-400">{t("设备列表加载失败，已保留适用设备范围。")}</p> : devices === null ? (
          <p className="text-xs text-gray-500">{t("正在加载设备…")}</p>
        ) : null}
        <div className="flex max-h-48 flex-wrap gap-2 overflow-y-auto p-1">
          {choices.map((device) => {
            const selected = value.includes(device.id);
            return (
              <Button key={device.id} size="small" plain variant={selected ? "primary" : "default"}
                className={cx("!rounded-md !shadow-none !transition-colors", !selected && "!px-5")} title={device.id} aria-pressed={selected}
                icon={selected ? <CheckmarkRegular className="h-3 w-3" /> : undefined}
                onClick={() => onChange(selected ? value.filter(id => id !== device.id) : [...value, device.id])}>
                {device.name || device.id}
              </Button>
            );
          })}
        </div>
      </> : null}
    </div>
  );
}

// URL 列表编辑器（Webhook / Bark 目标 URLs）。
export function UrlListEditor({
  urls,
  onChange,
  enabled,
  placeholder,
  emptyText,
}: {
  urls: string[];
  onChange: (urls: string[]) => void;
  enabled: boolean;
  placeholder: string;
  emptyText: string;
}) {
  const { t } = useI18n();
  const add = () => onChange([...(urls || []), ""]);
  const remove = (index: number) => onChange((urls || []).filter((_, i) => i !== index));
  const update = (index: number, value: string) =>
    onChange((urls || []).map((item, i) => (i === index ? value : item)));
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <label className={LABEL_CLASS}>{t("目标 URLs")}</label>
        <Button size="small" variant="primary" plain onClick={add} disabled={!enabled} icon={<AddRegular />}>
          {t("添加 URL")}
        </Button>
      </div>
      {!urls || urls.length === 0 ? <EmptyLine>{emptyText}</EmptyLine> : null}
      {(urls || []).map((url, index) => (
        <div key={index} className="flex items-center gap-2">
          <Input
            value={url}
            onChange={(e) => update(index, e.target.value)}
            disabled={!enabled}
            placeholder={placeholder}
            className="flex-1"
          />
          <Button variant="danger" plain onClick={() => remove(index)} disabled={!enabled} aria-label={t("删除")} icon={<DeleteRegular />} />
        </div>
      ))}
    </div>
  );
}
