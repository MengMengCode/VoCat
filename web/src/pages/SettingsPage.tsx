import { useCallback, useEffect, useState } from "react";
import { AlertRegular, CheckmarkRegular } from "@fluentui/react-icons";
import { api, apiMessage, getSecuritySettings, updateSecuritySettings } from "../api";
import type { DeveloperSettings, HTTPSSettings, NotificationSettings, SecuritySettings, SystemInfo } from "../types";
import { Button, PageHeader, confirmDialog, message } from "../components/ui";
import { CardDecor, CardIcon, CardTitle, SecurityCard, SystemInfoCard } from "../components/settings/Cards";
import type { PasswordForm, UpdateInfo } from "../components/settings/Cards";
import { NetworkAccessCard } from "../components/settings/NetworkAccessCard";
import type { NetworkAccessForm } from "../components/settings/NetworkAccessCard";
import { SegmentedTabs } from "../components/settings/controls";
import { useI18n } from "../lib/i18n";
import { useAuth } from "../store/auth";
import {
  buildBarkPayload,
  buildEmailPayload,
  buildLarkPayload,
  buildNotificationsPayload,
  buildWecomPayload,
  buildWebhookPayload,
  defaultNotifyForms,
  formsFromNotifications,
  type NotifyForms,
} from "../components/settings/model";
import { PushplusTab, TelegramTab } from "../components/settings/BotTabs";
import { BarkTab, EmailTab, LarkTab, WebhookTab, WecomTab } from "../components/settings/PushTabs";
import { PluginsCard } from "../components/settings/PluginsCard";
import { HTTPSCard } from "../components/settings/HTTPSCard";
import { DeviceQuotaCard } from "../components/settings/DeviceQuotaCard";
import { SMSRateLimitCard } from "../components/settings/SMSRateLimitCard";

const EMPTY_PASSWORD: PasswordForm = { oldPassword: "", newPassword: "", confirmPassword: "" };

const NOTIFY_TABS = [
  { key: "telegram", label: "Telegram Bot" },
  { key: "bark", label: "Bark" },
  { key: "email", label: "Email" },
  { key: "pushplus", label: "Pushplus" },
  { key: "webhook", label: "Webhook" },
  { key: "wecom", label: "企业微信消息推送" },
  { key: "lark", label: "飞书 / Lark 群机器人" },
];

const EMPTY_SYSTEM_INFO: SystemInfo = { version: "", buildTime: "", config: "" };

const EMPTY_SECURITY: NetworkAccessForm = { mode: "internal", allowedCidrs: [], trustProxyHeaders: false };
export default function SettingsPage() {
  const { refresh } = useAuth();
  const { t, lang } = useI18n();
  const [systemInfo, setSystemInfo] = useState<SystemInfo>(EMPTY_SYSTEM_INFO);
  const [password, setPassword] = useState<PasswordForm>(EMPTY_PASSWORD);
  const [forms, setForms] = useState<NotifyForms>(defaultNotifyForms);
  const [activeTab, setActiveTab] = useState("telegram");
  const [loadingNotif, setLoadingNotif] = useState(false);
  const [savingNotif, setSavingNotif] = useState(false);
  const [testingWebhook, setTestingWebhook] = useState(false);
  const [testingBark, setTestingBark] = useState(false);
  const [testingEmail, setTestingEmail] = useState(false);
  const [testingWecom, setTestingWecom] = useState(false);
  const [testingLark, setTestingLark] = useState(false);
  const [changingPassword, setChangingPassword] = useState(false);
  const [checkingUpdate, setCheckingUpdate] = useState(false);
  const [applyingUpdate, setApplyingUpdate] = useState(false);
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null);
  const [security, setSecurity] = useState<NetworkAccessForm>(EMPTY_SECURITY);
  const [clientIp, setClientIp] = useState("");
  const [clientAllowed, setClientAllowed] = useState(true);
  const [loadingSecurity, setLoadingSecurity] = useState(false);
  const [savingSecurity, setSavingSecurity] = useState(false);
  const [httpsSettings, setHTTPSSettings] = useState<HTTPSSettings | null>(null);
  const [loadingHTTPS, setLoadingHTTPS] = useState(false);
  const [savingHTTPS, setSavingHTTPS] = useState(false);
  const [developerSettings, setDeveloperSettings] = useState<DeveloperSettings | null>(null);
  const [deviceLimit, setDeviceLimit] = useState(5);
  const [smsHourlyLimit, setSMSHourlyLimit] = useState(10);
  const [loadingDeveloper, setLoadingDeveloper] = useState(false);
  const [savingDeveloper, setSavingDeveloper] = useState(false);
  const [savingSMSLimit, setSavingSMSLimit] = useState(false);

  const updateChannel = useCallback(<K extends keyof NotifyForms>(key: K, patch: Partial<NotifyForms[K]>) => {
    setForms((prev) => ({ ...prev, [key]: { ...prev[key], ...patch } }));
  }, []);

  const fetchSystemInfo = useCallback(async () => {
    try {
      const data = await api<SystemInfo>("/system/info");
      setSystemInfo(data);
    } catch (e) {
      console.error(t("系统信息读取失败"), e);
    }
  }, []);

  const fetchNotifications = useCallback(async () => {
    setLoadingNotif(true);
    try {
      const data = await api<NotificationSettings>("/settings/notifications");
      setForms(formsFromNotifications(data || {}));
    } catch {
      message.error(t("通知配置加载失败"));
    } finally {
      setLoadingNotif(false);
    }
  }, []);

  const applySecurity = useCallback((data: SecuritySettings) => {
    setSecurity({
      mode: data.mode === "public" ? "public" : "internal",
      allowedCidrs: data.allowedCidrs ?? [],
      trustProxyHeaders: !!data.trustProxyHeaders,
    });
    setClientIp(data.clientIp ?? "");
    setClientAllowed(!!data.clientAllowed);
  }, []);

  const fetchSecurity = useCallback(async () => {
    setLoadingSecurity(true);
    try {
      applySecurity(await getSecuritySettings());
    } catch {
      message.error(t("访问策略加载失败"));
    } finally {
      setLoadingSecurity(false);
    }
  }, [applySecurity]);

  const fetchHTTPS = useCallback(async () => {
    setLoadingHTTPS(true);
    try {
      setHTTPSSettings(await api<HTTPSSettings>("/settings/https"));
    } catch (error) {
      message.error(apiMessage(error) || (lang === "zh" ? "HTTPS 配置加载失败" : "Failed to load HTTPS settings"));
    } finally {
      setLoadingHTTPS(false);
    }
  }, [lang]);

  const fetchDeveloperSettings = useCallback(async () => {
    setLoadingDeveloper(true);
    try {
      const data = await api<DeveloperSettings>("/settings/developer");
      setDeveloperSettings(data);
      setDeviceLimit(data.deviceLimit);
      setSMSHourlyLimit(data.smsHourlyLimit);
    } catch (error) {
      message.error(apiMessage(error) || (lang === "zh" ? "设备配额配置加载失败" : "Failed to load device quota settings"));
    } finally {
      setLoadingDeveloper(false);
    }
  }, [lang]);

  useEffect(() => {
    void fetchSystemInfo();
    void fetchNotifications();
    void fetchSecurity();
  }, [fetchSystemInfo, fetchNotifications, fetchSecurity]);

  useEffect(() => {
    if (systemInfo.developer) {
      void fetchHTTPS();
      void fetchDeveloperSettings();
    } else {
      setHTTPSSettings(null);
      setDeveloperSettings(null);
      setDeviceLimit(5);
      setSMSHourlyLimit(10);
    }
  }, [systemInfo.developer, fetchHTTPS, fetchDeveloperSettings]);

  const onToggleHTTPS = useCallback(async (enabled: boolean) => {
    setSavingHTTPS(true);
    try {
      const data = await api<HTTPSSettings>("/settings/https", { method: "PUT", body: { enabled } });
      setHTTPSSettings(data);
      message.success(lang === "zh" ? (enabled ? "HTTPS 已开启，正在切换连接" : "HTTPS 已关闭，正在恢复 HTTP") : (enabled ? "HTTPS enabled; reconnecting" : "HTTPS disabled; returning to HTTP"));
      const target = enabled ? data.httpsUrl : data.httpUrl;
      window.setTimeout(() => window.location.replace(target + window.location.pathname + window.location.search + window.location.hash), 700);
    } catch (error) {
      message.error(apiMessage(error) || (lang === "zh" ? "HTTPS 配置保存失败" : "Failed to save HTTPS settings"));
      setSavingHTTPS(false);
    }
  }, [lang]);

  const onSaveDeviceLimit = useCallback(async () => {
    const maximum = developerSettings?.maxDeviceLimit ?? 10;
    if (!Number.isInteger(deviceLimit) || deviceLimit < 1 || deviceLimit > maximum) {
      message.error(lang === "zh" ? `设备配额必须是 1 到 ${maximum} 的整数` : `Device quota must be an integer between 1 and ${maximum}`);
      return;
    }
    setSavingDeveloper(true);
    try {
      const data = await api<DeveloperSettings>("/settings/developer", { method: "PUT", body: { deviceLimit } });
      setDeveloperSettings(data);
      setDeviceLimit(data.deviceLimit);
      message.success(lang === "zh" ? "设备配额已保存" : "Device quota saved");
    } catch (error) {
      message.error(apiMessage(error) || (lang === "zh" ? "设备配额保存失败" : "Failed to save device quota"));
    } finally {
      setSavingDeveloper(false);
    }
  }, [developerSettings, deviceLimit, lang]);

  const onSaveSMSHourlyLimit = useCallback(async () => {
    const maximum = developerSettings?.maxSmsHourlyLimit ?? 20;
    if (!Number.isInteger(smsHourlyLimit) || smsHourlyLimit < 1 || smsHourlyLimit > maximum) {
      message.error(lang === "zh" ? `短信发送限制必须是 1 到 ${maximum} 的整数` : `SMS limit must be an integer between 1 and ${maximum}`);
      return;
    }
    setSavingSMSLimit(true);
    try {
      const data = await api<DeveloperSettings>("/settings/developer", { method: "PUT", body: { smsHourlyLimit } });
      setDeveloperSettings(data);
      setSMSHourlyLimit(data.smsHourlyLimit);
      message.success(lang === "zh" ? "短信发送速率限制已保存" : "SMS rate limit saved");
    } catch (error) {
      message.error(apiMessage(error) || (lang === "zh" ? "短信发送速率限制保存失败" : "Failed to save SMS rate limit"));
    } finally {
      setSavingSMSLimit(false);
    }
  }, [developerSettings, smsHourlyLimit, lang]);

  const onSaveSecurity = useCallback(async () => {
    setSavingSecurity(true);
    try {
      const data = await updateSecuritySettings({
        mode: security.mode,
        allowedCidrs: security.allowedCidrs.map((cidr) => cidr.trim()).filter(Boolean),
        trustProxyHeaders: security.trustProxyHeaders,
      });
      applySecurity(data);
      if (data.clientAllowed) {
        message.success(t("访问策略已保存"));
      } else {
        message.warning(t("访问策略已保存，但当前连接的来源 IP 已被拒绝"));
      }
    } catch (error) {
      message.error(apiMessage(error) || t("访问策略保存失败"));
    } finally {
      setSavingSecurity(false);
    }
  }, [security, applySecurity]);

  const onChangePassword = useCallback(async () => {
    if (password.newPassword !== password.confirmPassword) {
      message.error(t("两次输入的新密码不一致"));
      return;
    }
    setChangingPassword(true);
    try {
      await api("/settings/password", {
        method: "POST",
        body: {
          oldPassword: password.oldPassword,
          newPassword: password.newPassword,
          confirmPassword: password.confirmPassword,
        },
      });
      message.success(t("密码已更新，请重新登录"));
      setPassword(EMPTY_PASSWORD);
      // vocat 后端改密成功后会注销现有会话，需要重新登录
      window.setTimeout(() => void refresh(), 1200);
    } catch (error) {
      message.error(apiMessage(error) || t("密码更新失败"));
    } finally {
      setChangingPassword(false);
    }
  }, [password, refresh]);

  const onSaveNotifications = useCallback(async () => {
    setSavingNotif(true);
    try {
      // vocat 后端 PUT 成功即返回完整配置文档（参考实现返回 {applied, warning}）
      const data = await api<NotificationSettings>("/settings/notifications", {
        method: "PUT",
        body: buildNotificationsPayload(forms),
      });
      setForms(formsFromNotifications(data));
      message.success(t("通知配置已保存"));
    } catch (error) {
      message.error(apiMessage(error) || t("通知配置保存失败"));
    } finally {
      setSavingNotif(false);
    }
  }, [forms]);

  const onTestWebhook = useCallback(async () => {
    setTestingWebhook(true);
    try {
      // vocat 后端测试成功返回 {channel, success, tested_at}，失败直接抛 ApiError
      await api("/settings/notifications/webhook/test", {
        method: "POST",
        body: buildWebhookPayload(forms.webhook, true),
      });
      message.success(t("测试通知已发送"));
    } catch (error) {
      message.error(apiMessage(error) || t("Webhook 测试失败"));
    } finally {
      setTestingWebhook(false);
    }
  }, [forms.webhook]);

  const onTestBark = useCallback(async () => {
    setTestingBark(true);
    try {
      await api("/settings/notifications/bark/test", {
        method: "POST",
        body: buildBarkPayload(forms.bark, true),
      });
      message.success(t("测试通知已发送"));
    } catch (error) {
      message.error(apiMessage(error) || t("Bark 测试失败"));
    } finally {
      setTestingBark(false);
    }
  }, [forms.bark]);

  const onTestEmail = useCallback(async () => {
    setTestingEmail(true);
    try {
      await api("/settings/notifications/email/test", {
        method: "POST",
        body: buildEmailPayload(forms.email, true),
      });
      message.success(t("测试邮件已发送"));
    } catch (error) {
      message.error(apiMessage(error) || t("Email 测试失败"));
    } finally {
      setTestingEmail(false);
    }
  }, [forms.email]);

  const onTestWecom = useCallback(async () => {
    setTestingWecom(true);
    try {
      await api("/settings/notifications/wecom/test", {
        method: "POST",
        body: buildWecomPayload(forms.wecom, true),
      });
      message.success(t("测试通知已发送"));
    } catch (error) {
      message.error(apiMessage(error) || t("企业微信消息推送测试失败"));
    } finally {
      setTestingWecom(false);
    }
  }, [forms.wecom]);

  const onTestLark = useCallback(async () => {
    setTestingLark(true);
    try {
      await api("/settings/notifications/lark/test", {
        method: "POST",
        body: buildLarkPayload(forms.lark, true),
      });
      message.success(t("测试通知已发送"));
    } catch (error) {
      message.error(apiMessage(error) || t("飞书 / Lark 群机器人通知测试失败"));
    } finally {
      setTestingLark(false);
    }
  }, [forms.lark]);

  const onCheckUpdate = useCallback(async () => {
    setCheckingUpdate(true);
    try {
      // vocat 后端返回 {available, version, message}（参考实现是 has_update 等）
      const data = await api<{ available?: boolean; version?: string; message?: string; is_docker?: boolean }>("/system/update/check");
      const info: UpdateInfo = {
        hasUpdate: !!data?.available,
        latestVersion: data?.version,
        releaseNote: data?.message,
        isDocker: !!data?.is_docker,
      };
      setUpdateInfo(info);
      if (!info.hasUpdate) message.info(data?.message || t("当前已是最新版本"));
    } catch (error) {
      message.error(apiMessage(error) || t("检查更新失败"));
    } finally {
      setCheckingUpdate(false);
    }
  }, []);

  const onApplyUpdate = useCallback(async () => {
    if (!updateInfo) return;
    if (updateInfo.isDocker) {
      await confirmDialog(
        lang === "zh"
          ? "检测到当前系统运行在 Docker 环境下。不建议在容器内直接执行文件热替换，请拉取最新镜像（如 docker pull vocat:latest）并重启容器来完成升级！"
          : "The system is running inside Docker. In-place binary replacement is not recommended; pull the latest image (e.g. docker pull vocat:latest) and restart the container to upgrade!",
        t("环境警告"),
        { confirmText: t("知道了"), type: "warning" },
      );
      return;
    }
    const confirmed = await confirmDialog(
      <div>
        <div>
            {lang === "zh"
              ? `最新版本：${updateInfo.latestVersion}，确定要现在更新并重启服务吗？`
              : `Latest version: ${updateInfo.latestVersion}. Update and restart the service now?`}
          </div>
        {updateInfo.releaseNote ? (
          <pre className="mt-2 max-h-48 overflow-y-auto whitespace-pre-wrap rounded-md bg-black/5 p-2 text-xs dark:bg-white/10">
            {updateInfo.releaseNote}
          </pre>
        ) : null}
      </div>,
      t("应用更新"),
      { confirmText: t("立即更新"), cancelText: t("取消"), type: "warning" },
    );
    if (!confirmed) return;
    setApplyingUpdate(true);
    try {
      const data = await api<{ message?: string; reauthenticationRequired?: boolean }>("/system/update/apply", { method: "POST", body: {} });
      message.success(data?.message || t("正在更新..."));
      window.setTimeout(() => {
        if (data?.reauthenticationRequired) window.location.replace("/login");
        else window.location.reload();
      }, 1500);
    } catch (e) {
      message.error(e instanceof Error ? e.message : t("应用更新失败"));
    } finally {
      setApplyingUpdate(false);
    }
  }, [updateInfo]);

  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader title={t("系统设置")} subtitle={t("管理网关参数与运行信息")} />
      <div className="grid grid-cols-1 gap-8 lg:grid-cols-2">
        <SecurityCard
          value={password}
          onChange={(patch) => setPassword((prev) => ({ ...prev, ...patch }))}
          loading={changingPassword}
          onSubmit={onChangePassword}
        />
        <SystemInfoCard
          info={systemInfo}
          updateInfo={updateInfo}
          checkingUpdate={checkingUpdate}
          applyingUpdate={applyingUpdate}
          onCheckUpdate={onCheckUpdate}
          onApplyUpdate={onApplyUpdate}
        />

        <NetworkAccessCard
          value={security}
          clientIp={clientIp}
          clientAllowed={clientAllowed}
          loading={loadingSecurity}
          saving={savingSecurity}
          onChange={(patch) => setSecurity((prev) => ({ ...prev, ...patch }))}
          onSave={onSaveSecurity}
        />

        {systemInfo.developer ? (
          <>
            <HTTPSCard
              value={httpsSettings}
              loading={loadingHTTPS}
              saving={savingHTTPS}
              onToggle={onToggleHTTPS}
            />
            <DeviceQuotaCard
              value={developerSettings}
              limit={deviceLimit}
              loading={loadingDeveloper}
              saving={savingDeveloper}
              onLimitChange={setDeviceLimit}
              onSave={onSaveDeviceLimit}
            />
            <SMSRateLimitCard
              value={developerSettings}
              limit={smsHourlyLimit}
              loading={loadingDeveloper}
              saving={savingSMSLimit}
              onLimitChange={setSMSHourlyLimit}
              onSave={onSaveSMSHourlyLimit}
            />
            <PluginsCard />
          </>
        ) : null}

        <div className="notify-card ui-card group relative overflow-hidden p-8 lg:col-span-2">
          <CardDecor />
          <div className="relative z-10 mb-6 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex items-center gap-3">
              <CardIcon>
                <AlertRegular className="text-[24px]" />
              </CardIcon>
              <CardTitle title={t("通知")} subtitle={t("Telegram / Bark / Email / Pushplus / Webhook / 企业微信 / 飞书 / Lark 群机器人")} />
            </div>
            <Button variant="primary" loading={savingNotif} disabled={loadingNotif} onClick={onSaveNotifications} className="!border-0" icon={<CheckmarkRegular />}>
              {t("保存通知配置")}
            </Button>
          </div>
          {loadingNotif ? (
            <div className="p-6 text-sm text-gray-500 dark:text-gray-400">{t("正在加载通知配置…")}</div>
          ) : (
            <div className="relative z-10 w-full overflow-hidden">
              <SegmentedTabs tabs={NOTIFY_TABS.map((tab) => ({ ...tab, label: t(tab.label) }))} value={activeTab} onChange={setActiveTab} />
              {activeTab === "telegram" ? (
                <TelegramTab value={forms.telegram} onChange={(p) => updateChannel("telegram", p)} />
              ) : null}
              {activeTab === "bark" ? (
                <BarkTab value={forms.bark} onChange={(p) => updateChannel("bark", p)} testing={testingBark} onTest={onTestBark} />
              ) : null}
              {activeTab === "email" ? (
                <EmailTab value={forms.email} onChange={(p) => updateChannel("email", p)} testing={testingEmail} onTest={onTestEmail} />
              ) : null}
              {activeTab === "pushplus" ? (
                <PushplusTab value={forms.pushplus} onChange={(p) => updateChannel("pushplus", p)} />
              ) : null}
              {activeTab === "webhook" ? (
                <WebhookTab
                  value={forms.webhook}
                  onChange={(p) => updateChannel("webhook", p)}
                  testing={testingWebhook}
                  onTest={onTestWebhook}
                />
              ) : null}
              {activeTab === "wecom" ? (
                <WecomTab value={forms.wecom} onChange={(p) => updateChannel("wecom", p)} testing={testingWecom} onTest={onTestWecom} />
              ) : null}
              {activeTab === "lark" ? (
                <LarkTab value={forms.lark} onChange={(p) => updateChannel("lark", p)} testing={testingLark} onTest={onTestLark} />
              ) : null}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
