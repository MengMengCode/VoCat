import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AddRegular,
  DeleteRegular,
  EditRegular,
  PlayRegular,
  SendClockRegular,
} from "@fluentui/react-icons";
import { api, apiMessage } from "../api";
import type { DeviceListItem, DevicesResponse } from "../types";
import type { EsimProfileGroup } from "../components/devices/types";
import {
  Button,
  Input,
  Modal,
  PageHeader,
  Pagination,
  Select,
  Switch,
  Tag,
  Textarea,
  confirmDialog,
  message,
} from "../components/ui";
import { useI18n } from "../lib/i18n";

type TaskType = "sms" | "call" | "public_ip";
type TaskEnvironment = "vowifi" | "cellular";

interface AutomaticTaskPayload {
  phone?: string;
  message?: string;
  durationSeconds?: number;
}

interface AutomaticTask {
  id: number;
  name: string;
  enabled: boolean;
  deviceId: string;
  profileIccid: string;
  profileAid: string;
  taskType: TaskType;
  environment: TaskEnvironment;
  intervalDays: number;
  startDate: string;
  runTime: string;
  payload: AutomaticTaskPayload;
  retryCount: number;
  notify: boolean;
  nextRunAt: string;
  lastRunAt?: string;
  lastStatus: string;
  lastError: string;
}

interface AutomaticTaskRun {
  id: number;
  taskId: number;
  deviceId: string;
  scheduledAt: string;
  startedAt?: string;
  finishedAt?: string;
  status: "queued" | "running" | "success" | "failed";
  attempts: number;
  output: string;
  error: string;
}

interface TaskForm {
  id: number;
  name: string;
  enabled: boolean;
  deviceId: string;
  profileIccid: string;
  profileAid: string;
  taskType: TaskType;
  environment: TaskEnvironment;
  intervalDays: number;
  startDate: string;
  runTime: string;
  retryCount: number;
  notify: boolean;
  phone: string;
  message: string;
  durationSeconds: number;
}

interface ProfileOption {
  iccid: string;
  aidHex: string;
  label: string;
}

function localDate(value = new Date()) {
  const year = value.getFullYear();
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function localTime(value = new Date(Date.now() + 5 * 60_000)) {
  return `${String(value.getHours()).padStart(2, "0")}:${String(value.getMinutes()).padStart(2, "0")}`;
}

function emptyForm(deviceId = ""): TaskForm {
  return {
    id: 0,
    name: "",
    enabled: true,
    deviceId,
    profileIccid: "",
    profileAid: "",
    taskType: "sms",
    environment: "vowifi",
    intervalDays: 1,
    startDate: localDate(),
    runTime: localTime(),
    retryCount: 1,
    notify: true,
    phone: "",
    message: "",
    durationSeconds: 30,
  };
}

function formatDateTime(value?: string) {
  if (!value || value.startsWith("0001-")) return "--";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "--" : date.toLocaleString();
}

const fieldLabel = "mb-1.5 block text-sm font-semibold text-gray-700 dark:text-gray-200";

export default function AutomaticTasksPage() {
  const { t } = useI18n();
  const [tasks, setTasks] = useState<AutomaticTask[]>([]);
  const [runs, setRuns] = useState<AutomaticTaskRun[]>([]);
  const [runsTotal, setRunsTotal] = useState(0);
  const [runsPage, setRunsPage] = useState(1);
  const [runsPageSize, setRunsPageSize] = useState(20);
  const [devices, setDevices] = useState<DeviceListItem[]>([]);
  const [profiles, setProfiles] = useState<ProfileOption[]>([]);
  const [loading, setLoading] = useState(true);
  const [profileLoading, setProfileLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<TaskForm>(() => emptyForm());
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState(0);
  // Refs mirror the runs page/pageSize so the 5s poll reloads the page the user
  // is actually looking at instead of snapping back to page 1 on every tick.
  const runsPageRef = useRef(1);
  const runsPageSizeRef = useRef(20);

  const load = useCallback(async (initial = false) => {
    if (initial) setLoading(true);
    try {
      const [taskData, deviceData] = await Promise.all([
        api<{ tasks?: AutomaticTask[] }>("/automatic-tasks"),
        api<DevicesResponse>("/devices"),
      ]);
      setTasks(taskData.tasks || []);
      setDevices(deviceData.devices || []);
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      if (initial) setLoading(false);
    }
  }, []);

  const fetchRuns = useCallback(async (page: number, pageSize: number) => {
    const request = (target: number) =>
      api<{ runs?: AutomaticTaskRun[]; total?: number }>(
        `/automatic-tasks/runs?limit=${pageSize}&offset=${(target - 1) * pageSize}`,
      );
    try {
      let data = await request(page);
      const total = data.total ?? 0;
      const pages = Math.max(1, Math.ceil(total / pageSize));
      // Clamp when the current page fell past the end (a larger page size, or
      // runs removed with a deleted task) instead of showing an empty slice.
      if (page > pages) {
        data = await request(pages);
        runsPageRef.current = pages;
        setRunsPage(pages);
      }
      setRuns(data.runs || []);
      setRunsTotal(total);
    } catch (error) {
      message.error(apiMessage(error));
    }
  }, []);

  const reloadRuns = useCallback(
    () => fetchRuns(runsPageRef.current, runsPageSizeRef.current),
    [fetchRuns],
  );

  useEffect(() => {
    void load(true);
    void reloadRuns();
    const timer = window.setInterval(() => {
      void load();
      void reloadRuns();
    }, 5000);
    return () => window.clearInterval(timer);
  }, [load, reloadRuns]);

  function changeRunsPage(page: number) {
    runsPageRef.current = page;
    setRunsPage(page);
    void fetchRuns(page, runsPageSizeRef.current);
  }

  function changeRunsPageSize(pageSize: number) {
    runsPageSizeRef.current = pageSize;
    setRunsPageSize(pageSize);
    runsPageRef.current = 1;
    setRunsPage(1);
    void fetchRuns(1, pageSize);
  }

  const loadProfiles = useCallback(async (deviceId: string, keepICCID = "") => {
    setProfiles([]);
    if (!deviceId) return;
    setProfileLoading(true);
    try {
      const data = await api<{ profiles?: EsimProfileGroup[] }>(`/devices/${encodeURIComponent(deviceId)}/esim`);
      const options = (data.profiles || []).flatMap((group, groupIndex) =>
        (group.profiles || []).map((profile) => ({
          iccid: profile.iccid,
          aidHex: group.aidHex || "",
          label: `${profile.name || profile.serviceProviderName || `Profile ${groupIndex + 1}`} · ${profile.iccid}`,
        })),
      );
      setProfiles(options);
      setForm((current) => {
        if (current.deviceId !== deviceId) return current;
        const selected = options.find((item) => item.iccid === (keepICCID || current.profileIccid)) || options[0];
        return selected ? { ...current, profileIccid: selected.iccid, profileAid: selected.aidHex } : current;
      });
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setProfileLoading(false);
    }
  }, []);

  const deviceByID = useMemo(() => new Map(devices.map((device) => [device.id, device])), [devices]);
  const taskByID = useMemo(() => new Map(tasks.map((task) => [task.id, task])), [tasks]);

  function edit(task?: AutomaticTask) {
    const deviceId = task?.deviceId || devices[0]?.id || "";
    let next = task ? {
      id: task.id,
      name: task.name,
      enabled: task.enabled,
      deviceId: task.deviceId,
      profileIccid: task.profileIccid,
      profileAid: task.profileAid || "",
      taskType: task.taskType,
      environment: task.environment,
      intervalDays: task.intervalDays,
      startDate: task.startDate,
      runTime: task.runTime,
      retryCount: task.retryCount,
      notify: task.notify,
      phone: task.payload?.phone || "",
      message: task.payload?.message || "",
      durationSeconds: task.payload?.durationSeconds || 30,
    } : emptyForm(deviceId);
	if (devices.find((device) => device.id === deviceId)?.deviceType === "usb_sim_reader") {
	  next = { ...next, taskType: next.taskType === "public_ip" ? "sms" : next.taskType, environment: "vowifi" };
	}
    setForm(next);
    setOpen(true);
    void loadProfiles(deviceId, next.profileIccid);
  }

  function chooseDevice(deviceId: string) {
	const reader = devices.find((device) => device.id === deviceId)?.deviceType === "usb_sim_reader";
    setForm((current) => ({
	  ...current, deviceId, profileIccid: "", profileAid: "",
	  taskType: reader && current.taskType === "public_ip" ? "sms" : current.taskType,
	  environment: reader ? "vowifi" : current.environment,
	}));
    void loadProfiles(deviceId);
  }

  function chooseProfile(iccid: string) {
    const selected = profiles.find((profile) => profile.iccid === iccid);
    setForm((current) => ({ ...current, profileIccid: iccid, profileAid: selected?.aidHex || "" }));
  }

  function chooseTaskType(taskType: TaskType) {
	if (deviceByID.get(form.deviceId)?.deviceType === "usb_sim_reader" && taskType === "public_ip") return;
    setForm((current) => ({
      ...current,
      taskType,
      environment: taskType === "public_ip" ? "cellular" : current.environment,
    }));
  }

  async function save() {
    if (!form.name.trim()) return message.warning(t("请输入任务名称"));
    if (!form.deviceId) return message.warning(t("请选择设备"));
    if (!form.profileIccid) return message.warning(t("请选择 eSIM Profile"));
	if (deviceByID.get(form.deviceId)?.deviceType === "usb_sim_reader" && (form.environment !== "vowifi" || form.taskType === "public_ip")) {
	  return message.warning(t("USB SIM读卡器仅支持VoWiFi短信和通话任务"));
	}
    if (form.taskType !== "public_ip" && !form.phone.trim()) return message.warning(t("请输入号码"));
    if (form.taskType === "sms" && !form.message.trim()) return message.warning(t("请输入短信内容"));
    setSaving(true);
    try {
      const body = {
        name: form.name,
        enabled: form.enabled,
        deviceId: form.deviceId,
        profileIccid: form.profileIccid,
        profileAid: form.profileAid,
        taskType: form.taskType,
        environment: form.environment,
        intervalDays: Number(form.intervalDays),
        startDate: form.startDate,
        runTime: form.runTime,
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
        retryCount: Number(form.retryCount),
        notify: form.notify,
        payload: {
          phone: form.phone,
          message: form.message,
          durationSeconds: Number(form.durationSeconds),
        },
      };
      await api(form.id ? `/automatic-tasks/${form.id}` : "/automatic-tasks", {
        method: form.id ? "PUT" : "POST",
        body,
      });
      message.success(t(form.id ? "自动任务已更新" : "自动任务已创建"));
      setOpen(false);
      await load();
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setSaving(false);
    }
  }

  async function toggle(task: AutomaticTask) {
    setBusy(task.id);
    try {
      await api(`/automatic-tasks/${task.id}`, { method: "PUT", body: { ...task, enabled: !task.enabled } });
      await load();
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setBusy(0);
    }
  }

  async function runNow(task: AutomaticTask) {
    setBusy(task.id);
    try {
      await api(`/automatic-tasks/${task.id}/run`, { method: "POST" });
      message.success(t("任务已加入设备队列"));
      await load();
      changeRunsPage(1);
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setBusy(0);
    }
  }

  async function remove(task: AutomaticTask) {
    if (!await confirmDialog(t("确定删除这个自动任务吗？"), t("确认删除"), { type: "warning", confirmText: t("删除"), cancelText: t("取消") })) return;
    setBusy(task.id);
    try {
      await api(`/automatic-tasks/${task.id}`, { method: "DELETE" });
      message.success(t("自动任务已删除"));
      await load();
      await reloadRuns();
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setBusy(0);
    }
  }

  const taskTypeLabel = (value: TaskType) => ({ sms: t("发送短信"), call: t("拨打电话并自动挂断"), public_ip: t("获取漫游公网 IP") })[value];
  const environmentLabel = (value: TaskEnvironment) => value === "vowifi" ? "VoWiFi" : t("基站直连");
	const selectedTaskDeviceIsReader = deviceByID.get(form.deviceId)?.deviceType === "usb_sim_reader";
	const taskTypeOptions = [
	  { value: "sms", label: t("发送短信") },
	  { value: "call", label: t("拨打电话并自动挂断") },
	  ...(!selectedTaskDeviceIsReader ? [{ value: "public_ip", label: t("开启漫游流量并获取一次公网 IP") }] : []),
	];
	const environmentOptions = selectedTaskDeviceIsReader
	  ? [{ value: "vowifi", label: "VoWiFi" }]
	  : [{ value: "vowifi", label: "VoWiFi" }, { value: "cellular", label: t("基站直连（自动选网）") }];

  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        title={t("自动任务")}
        subtitle={t("按周期切换指定 eSIM Profile，并在设备串行队列中执行短信、通话或漫游公网 IP 任务")}
        actions={<Button variant="primary" icon={<AddRegular />} onClick={() => edit()} disabled={!devices.length}>{t("添加任务")}</Button>}
      />

      <div className="ui-card overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full min-w-[1040px] text-left text-sm">
            <thead className="border-b border-gray-100 bg-gray-50/70 text-xs uppercase tracking-wide text-gray-500 dark:border-white/10 dark:bg-white/[0.025]">
              <tr>
                <th className="px-4 py-3">{t("任务")}</th>
                <th className="px-4 py-3">{t("设备 / Profile")}</th>
                <th className="px-4 py-3">{t("类型")}</th>
                <th className="px-4 py-3">{t("执行环境")}</th>
                <th className="px-4 py-3">{t("周期")}</th>
                <th className="px-4 py-3">{t("下次执行")}</th>
                <th className="px-4 py-3">{t("上次结果")}</th>
                <th className="px-4 py-3 text-right">{t("操作")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-white/10">
              {tasks.map((task) => (
                <tr key={task.id} className="hover:bg-sky-50/40 dark:hover:bg-sky-500/[0.04]">
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-2"><Switch checked={task.enabled} loading={busy === task.id} size="small" onChange={() => void toggle(task)} /><span className="font-semibold">{task.name}</span></div>
                    <div className="mt-1 text-xs text-gray-400">{task.notify ? t("完成后推送通知") : t("不推送通知")} · {t("失败重试 {count} 次").replace("{count}", String(task.retryCount))}</div>
                  </td>
                  <td className="px-4 py-3">
                    <div>{deviceByID.get(task.deviceId)?.name || task.deviceId}</div>
                    <div className="mt-1 font-mono text-xs text-gray-400">…{task.profileIccid.slice(-8)}</div>
                  </td>
                  <td className="px-4 py-3">{taskTypeLabel(task.taskType)}</td>
                  <td className="px-4 py-3"><Tag type={task.environment === "vowifi" ? "primary" : "warning"}>{environmentLabel(task.environment)}</Tag></td>
                  <td className="px-4 py-3">{t("每 {days} 天").replace("{days}", String(task.intervalDays))} · {task.runTime}</td>
                  <td className="px-4 py-3 text-xs">{formatDateTime(task.nextRunAt)}</td>
                  <td className="px-4 py-3">
                    {task.lastStatus ? <Tag type={task.lastStatus === "success" ? "success" : "danger"}>{task.lastStatus === "success" ? t("成功") : t("失败")}</Tag> : <span className="text-gray-400">--</span>}
                    {task.lastError ? <div className="mt-1 max-w-[220px] truncate text-xs text-red-500" title={task.lastError}>{task.lastError}</div> : null}
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex justify-end gap-2">
                      <Button size="small" icon={<PlayRegular />} loading={busy === task.id} onClick={() => void runNow(task)}>{t("立即执行")}</Button>
                      <Button size="small" icon={<EditRegular />} onClick={() => edit(task)}>{t("编辑")}</Button>
                      <Button size="small" variant="danger" plain icon={<DeleteRegular />} loading={busy === task.id} onClick={() => void remove(task)}>{t("删除")}</Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!loading && !tasks.length ? (
          <div className="flex flex-col items-center justify-center px-6 py-16 text-center text-gray-400">
            <SendClockRegular className="mb-3 text-4xl" />
            <div className="text-sm">{t("暂无自动任务")}</div>
            <div className="mt-1 text-xs">{t("添加任务后，系统会按设备排队并在执行前校验目标 Profile")}</div>
          </div>
        ) : null}
        {loading ? <div className="px-6 py-16 text-center text-sm text-gray-400">{t("加载中...")}</div> : null}
      </div>

      <div className="ui-card mt-5 overflow-hidden">
        <div className="border-b border-gray-100 px-5 py-4 dark:border-white/10"><h3 className="font-bold">{t("最近执行记录")}</h3></div>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[800px] text-left text-sm">
            <thead className="bg-gray-50/70 text-xs text-gray-500 dark:bg-white/[0.025]"><tr><th className="px-4 py-3">{t("任务")}</th><th className="px-4 py-3">{t("设备")}</th><th className="px-4 py-3">{t("状态")}</th><th className="px-4 py-3">{t("排队时间")}</th><th className="px-4 py-3">{t("尝试次数")}</th><th className="px-4 py-3">{t("结果")}</th></tr></thead>
            <tbody className="divide-y divide-gray-100 dark:divide-white/10">
              {runs.map((run) => (
                <tr key={run.id}><td className="px-4 py-3 font-medium">{taskByID.get(run.taskId)?.name || `#${run.taskId}`}</td><td className="px-4 py-3">{deviceByID.get(run.deviceId)?.name || run.deviceId}</td><td className="px-4 py-3"><Tag type={run.status === "success" ? "success" : run.status === "failed" ? "danger" : run.status === "running" ? "warning" : "info"}>{({ queued: t("排队中"), running: t("执行中"), success: t("成功"), failed: t("失败") })[run.status]}</Tag></td><td className="px-4 py-3 text-xs">{formatDateTime(run.scheduledAt)}</td><td className="px-4 py-3">{run.attempts}</td><td className="px-4 py-3"><div className={run.error ? "max-w-md text-red-500" : "max-w-md text-gray-600 dark:text-gray-300"}>{run.error || run.output || "--"}</div></td></tr>
              ))}
            </tbody>
          </table>
        </div>
        {!runs.length ? <div className="p-8 text-center text-sm text-gray-400">{t("暂无执行记录")}</div> : null}
        {runsTotal > 0 ? (
          <div className="border-t border-gray-100 px-5 py-3 dark:border-white/10">
            <Pagination
              page={runsPage}
              pageSize={runsPageSize}
              total={runsTotal}
              onPageChange={changeRunsPage}
              onPageSizeChange={changeRunsPageSize}
            />
          </div>
        ) : null}
      </div>

      <Modal open={open} onClose={() => setOpen(false)} title={form.id ? t("编辑自动任务") : t("添加自动任务")} width="max-w-3xl">
        <div className="grid gap-4 md:grid-cols-2">
          <div className="md:col-span-2"><label className={fieldLabel}>{t("任务名称")}</label><Input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder={t("例如：每日短信保活")} /></div>
          <div><label className={fieldLabel}>{t("设备")}</label><Select value={form.deviceId} onChange={chooseDevice} options={devices.map((device) => ({ value: device.id, label: `${device.name || device.id} (${device.id})` }))} /></div>
          <div><label className={fieldLabel}>{t("eSIM Profile")}</label><Select value={form.profileIccid} onChange={chooseProfile} disabled={profileLoading || !form.deviceId} placeholder={profileLoading ? t("读取 Profile 中...") : t("请选择 Profile")} options={profiles.map((profile) => ({ value: profile.iccid, label: profile.label }))} /></div>
          <div><label className={fieldLabel}>{t("任务类型")}</label><Select value={form.taskType} onChange={(value) => chooseTaskType(value as TaskType)} options={taskTypeOptions} /></div>
          <div><label className={fieldLabel}>{t("执行环境")}</label><Select value={form.environment} onChange={(value) => setForm({ ...form, environment: value as TaskEnvironment })} disabled={form.taskType === "public_ip" || selectedTaskDeviceIsReader} options={environmentOptions} /></div>
		  {selectedTaskDeviceIsReader ? <div className="md:col-span-2 rounded-lg border border-sky-200 bg-sky-50 p-3 text-sm text-sky-700 dark:border-sky-500/20 dark:bg-sky-500/10 dark:text-sky-300">{t("USB SIM读卡器仅支持VoWiFi短信和通话任务")}</div> : null}

          {form.taskType !== "public_ip" ? <div><label className={fieldLabel}>{t("号码")}</label><Input value={form.phone} onChange={(event) => setForm({ ...form, phone: event.target.value })} placeholder="+447700900123" /></div> : null}
          {form.taskType === "call" ? <div><label className={fieldLabel}>{t("自动挂断")}</label><Input type="number" min={1} max={600} value={form.durationSeconds} suffix="s" onChange={(event) => setForm({ ...form, durationSeconds: Number(event.target.value) })} /></div> : null}
          {form.taskType === "sms" ? <div className="md:col-span-2"><label className={fieldLabel}>{t("短信内容")}</label><Textarea rows={4} value={form.message} onChange={(event) => setForm({ ...form, message: event.target.value })} /></div> : null}
          {form.taskType === "public_ip" ? <div className="md:col-span-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-700 dark:border-amber-500/20 dark:bg-amber-500/10 dark:text-amber-300">{t("该任务固定使用基站直连和自动选网；执行时会开启漫游数据，并通过模块接口访问 ipinfo.io。需要开启开发者模式。")}</div> : null}

          <div><label className={fieldLabel}>{t("首次执行日期")}</label><Input type="date" value={form.startDate} onChange={(event) => setForm({ ...form, startDate: event.target.value })} /></div>
          <div><label className={fieldLabel}>{t("执行时间")}</label><Input type="time" value={form.runTime} onChange={(event) => setForm({ ...form, runTime: event.target.value })} /></div>
          <div><label className={fieldLabel}>{t("执行周期")}</label><Input type="number" min={1} max={365} value={form.intervalDays} suffix={t("天")} onChange={(event) => setForm({ ...form, intervalDays: Number(event.target.value) })} /></div>
          <div><label className={fieldLabel}>{t("任务失败重试次数")}</label><Select value={String(form.retryCount)} onChange={(value) => setForm({ ...form, retryCount: Number(value) })} options={Array.from({ length: 11 }, (_, count) => ({ value: String(count), label: t("{count} 次").replace("{count}", String(count)) }))} /></div>
          <div className="flex items-center justify-between rounded-lg border border-gray-200 p-3 dark:border-white/10"><div><div className="text-sm font-semibold">{t("启用任务")}</div><div className="text-xs text-gray-400">{t("停用后不会进入执行队列")}</div></div><Switch checked={form.enabled} onChange={(enabled) => setForm({ ...form, enabled })} /></div>
          <div className="flex items-center justify-between rounded-lg border border-gray-200 p-3 dark:border-white/10"><div><div className="text-sm font-semibold">{t("完成后推送通知")}</div><div className="text-xs text-gray-400">{t("发送到全部已配置并启用的通知渠道")}</div></div><Switch checked={form.notify} onChange={(notify) => setForm({ ...form, notify })} /></div>
        </div>
        <div className="mt-5 flex justify-end gap-2"><Button onClick={() => setOpen(false)}>{t("取消")}</Button><Button variant="primary" loading={saving} onClick={() => void save()}>{t("保存")}</Button></div>
      </Modal>
    </div>
  );
}
