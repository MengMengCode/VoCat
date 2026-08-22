import { useCallback, useEffect, useMemo, useRef, useState, type PointerEvent, type UIEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { AddRegular, DeleteRegular } from "@fluentui/react-icons";
import { ApiError, apiMessage } from "../api";
import { Button, ErrorState, PageHeader, RefreshButton, Spinner, confirmDialog, message } from "../components/ui";
import { usePolling } from "../lib/usePolling";
import { cx } from "../lib/utils";
import type { DeviceListItem, SMSMessage } from "../types";
import {
  analyzeSmsEncoding,
  deriveThread,
  filterThreads,
  groupMessagesByDay,
  sortMessages,
  sortThreads,
  type SmsActionTarget,
  type SmsThread,
} from "../components/sms/smsText";
import {
  deleteMessage,
  deleteThread,
  getThread,
  listContacts,
  listSmsDevices,
  sendSms,
  type DeleteThreadQuery,
  type SmsSendResult,
  type ThreadQuery,
} from "../components/sms/smsApi";
import { ContactList } from "../components/sms/ContactList";
import { ThreadPanel } from "../components/sms/ThreadPanel";
import { NewSmsModal, type NewSmsPayload } from "../components/sms/NewSmsModal";
import { tf, tl, useI18n } from "../lib/i18n";

const THREAD_PAGE = 80;
const MOBILE_BREAKPOINT = 980;

interface LoadError {
  message: string;
  status?: number;
}

interface DeviceFilterOption {
  id: string;
  label: string;
  healthy: boolean;
}

function toLoadError(e: unknown): LoadError {
  if (e instanceof ApiError) return { message: e.message, status: e.status };
  if (e instanceof Error) return { message: e.message };
  return { message: tl("加载失败") };
}

function showSmsSendOutcome(result: SmsSendResult) {
  const accepted = Number(result.partsAccepted || 0);
  const total = Number(result.partsTotal || 0);
  switch (result.outcome) {
    case "delivered":
      message.success("短信已确认送达");
      return;
    case "accepted_unconfirmed":
      // Keep the user-facing result identical across cellular AT (EC20) and
      // VoWiFi IMS (410). The transport remains available in the API response
      // for diagnostics, but it must not change the meaning shown to users.
      message.info("模块已接受短信提交，但尚未收到运营商送达确认", 5000);
      return;
    case "partial":
      message.warning(`短信仅有 ${accepted}/${total} 段被接受，不能判定为发送成功`, 5000);
      return;
    default:
      message.error("短信未被模块或 IMS 接受");
  }
}

export default function SmsPage() {
  const { t, lang } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();

  const [devices, setDevices] = useState<DeviceListItem[]>([]);
  const [devicesError, setDevicesError] = useState<LoadError | null>(null);
  const [devicesOkAt, setDevicesOkAt] = useState<number | null>(null);
  const [contacts, setContacts] = useState<SmsThread[]>([]);
  const [contactsLoading, setContactsLoading] = useState(false);
  const [contactsError, setContactsError] = useState<LoadError | null>(null);
  const [contactsOkAt, setContactsOkAt] = useState<number | null>(null);
  const [selectedDevice, setSelectedDevice] = useState<string>(() => searchParams.get("device") || "all");
  const [selectedKey, setSelectedKey] = useState<string>(() => searchParams.get("contact") || "");
  const [searchQuery, setSearchQuery] = useState("");
  const [messages, setMessages] = useState<SMSMessage[]>([]);
  const [messagesLoading, setMessagesLoading] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [sending, setSending] = useState(false);
  const [newSmsOpen, setNewSmsOpen] = useState(false);
  const [newSmsDevice, setNewSmsDevice] = useState("");
  const [composer, setComposer] = useState("");
  const [deletingMessageId, setDeletingMessageId] = useState<number | null>(null);
  const [deletingThreadKey, setDeletingThreadKey] = useState<string | null>(null);
  const [actionTarget, setActionTarget] = useState<SmsActionTarget | null>(null);
  const [actionSheetOpen, setActionSheetOpen] = useState(false);
  const [canHover, setCanHover] = useState(false);
  const [width, setWidth] = useState(0);

  // Refs mirroring the values read inside async flows (avoid stale closures).
  const deviceRef = useRef(selectedDevice);
  const keyRef = useRef(selectedKey);
  const contactsRef = useRef(contacts);
  const messagesRef = useRef(messages);
  const hasMoreRef = useRef(hasMore);
  const loadingMoreRef = useRef(loadingMore);
  const contactsLoadingRef = useRef(contactsLoading);
  const messagesLoadingRef = useRef(messagesLoading);
  const searchRef = useRef(searchQuery);
  const isMobileRef = useRef(false);

  const pageRef = useRef<HTMLDivElement | null>(null);
  const detailRef = useRef<HTMLDivElement | null>(null);
  const composerRef = useRef<HTMLTextAreaElement | null>(null);
  const deviceReqId = useRef(0);
  const contactReqId = useRef(0);
  const threadReqId = useRef(0);
  const lpTimer = useRef<number | null>(null);
  const lpStart = useRef({ x: 0, y: 0 });

  const isMobile = width > 0 && width < MOBILE_BREAKPOINT;
  const isDesktop = !isMobile;
  const showContactColumn = isDesktop || !selectedKey;
  const showDetailColumn = isDesktop || !!selectedKey;

  // Setter helpers keep state and its mirror ref in sync immediately.
  const setDevice = (d: string) => { deviceRef.current = d; setSelectedDevice(d); };
  const setKey = (k: string) => { keyRef.current = k; setSelectedKey(k); };
  const setContactsState = (c: SmsThread[]) => { contactsRef.current = c; setContacts(c); };
  const setMessagesState = (m: SMSMessage[]) => { messagesRef.current = m; setMessages(m); };
  const setHasMoreState = (h: boolean) => { hasMoreRef.current = h; setHasMore(h); };
  const setSearch = (q: string) => { searchRef.current = q; setSearchQuery(q); };
  const setLoadingMoreState = (v: boolean) => { loadingMoreRef.current = v; setLoadingMore(v); };
  const setContactsLoadingState = (v: boolean) => { contactsLoadingRef.current = v; setContactsLoading(v); };
  const setMessagesLoadingState = (v: boolean) => { messagesLoadingRef.current = v; setMessagesLoading(v); };

  // Opening a thread prompts the backend (handleSMSThread GET) to flip those
  // inbound messages to is_read, so clear the unread badge locally right away.
  // The next /sms/contacts poll reconfirms the authoritative server state.
  const markRead = (key: string) => {
    setContactsState(contactsRef.current.map((t) => (t.key === key && t.unread ? { ...t, unread: false } : t)));
  };

  const filteredContacts = useMemo(() => filterThreads(contacts, searchQuery), [contacts, searchQuery]);
  const activeThread = useMemo(() => contacts.find((t) => t.key === selectedKey) || null, [contacts, selectedKey]);
  const groups = useMemo(() => groupMessagesByDay(messages), [messages]);
  const composerInfo = useMemo(() => analyzeSmsEncoding(composer), [composer]);
  const composerLength = useMemo(() => Array.from(composer || "").length, [composer]);
  const deviceFilters = useMemo<DeviceFilterOption[]>(
    () => [
      { id: "all", label: t("全部设备"), healthy: true },
      ...devices.map((d) => ({
        id: d.id,
        label: d.name || d.id,
        healthy: !!d.running && (d.controlOnline ?? d.healthy) === true,
      })),
    ],
    [devices],
  );
  const deviceSelectOptions = useMemo(() => deviceFilters.map((o) => ({ value: o.id, label: o.label })), [deviceFilters]);

  const isUnread = (t: SmsThread) => t.unread;

  const syncQuery = useCallback(
    (device: string, contactKey: string) => {
      setSearchParams(
        (prev) => {
          const p = new URLSearchParams(prev);
          if (device && device !== "all") p.set("device", device);
          else p.delete("device");
          if (contactKey) p.set("contact", contactKey);
          else p.delete("contact");
          return p;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  const scrollToBottomNow = useCallback(() => {
    const scroll = () => {
      const el = detailRef.current;
      if (el) el.scrollTop = el.scrollHeight;
    };
    requestAnimationFrame(() => {
      scroll();
      window.setTimeout(scroll, 150);
    });
  }, []);

  const clearLongPress = useCallback(() => {
    if (lpTimer.current != null) {
      window.clearTimeout(lpTimer.current);
      lpTimer.current = null;
    }
  }, []);

  const startLongPress = useCallback(
    (target: SmsActionTarget, e: PointerEvent) => {
      if (!isMobileRef.current || e.pointerType === "mouse") return;
      clearLongPress();
      lpStart.current = { x: e.clientX, y: e.clientY };
      lpTimer.current = window.setTimeout(() => {
        lpTimer.current = null;
        setActionTarget(target);
        setActionSheetOpen(true);
        if (typeof navigator !== "undefined" && typeof navigator.vibrate === "function") navigator.vibrate(20);
      }, 450);
    },
    [clearLongPress],
  );

  const moveLongPress = useCallback(
    (e: PointerEvent) => {
      if (
        lpTimer.current != null &&
        (Math.abs(e.clientX - lpStart.current.x) > 10 || Math.abs(e.clientY - lpStart.current.y) > 10)
      ) {
        clearLongPress();
      }
    },
    [clearLongPress],
  );

  const onThreadPointerDown = useCallback(
    (t: SmsThread, e: PointerEvent) => startLongPress({ type: "thread", thread: t }, e),
    [startLongPress],
  );
  const onMsgPointerDown = useCallback(
    (m: SMSMessage, e: PointerEvent) => startLongPress({ type: "message", message: m }, e),
    [startLongPress],
  );

  const loadDevices = useCallback(async (): Promise<boolean> => {
    const id = ++deviceReqId.current;
    setDevicesError(null);
    try {
      const list = await listSmsDevices();
      if (id !== deviceReqId.current) return false;
      setDevices(list);
      setDevicesOkAt(Date.now());
      if (deviceRef.current !== "all" && !list.some((d) => d.id === deviceRef.current)) {
        setDevice("all");
        setKey("");
        setMessagesState([]);
        setHasMoreState(false);
        syncQuery("all", "");
      }
      return true;
    } catch (e) {
      if (id !== deviceReqId.current) return false;
      setDevicesError(toLoadError(e));
      return false;
    }
  }, [syncQuery]);

  const loadContacts = useCallback(async (device: string, silent: boolean): Promise<SmsThread[] | null> => {
    const id = ++contactReqId.current;
    if (!silent) setContactsLoadingState(true);
    setContactsError(null);
    try {
      const raw = await listContacts(device);
      if (id !== contactReqId.current) return null;
      const threads = sortThreads(raw.map(deriveThread));
      setContactsState(threads);
      setContactsOkAt(Date.now());
      return threads;
    } catch (e) {
      if (id !== contactReqId.current) return null;
      setContactsError(toLoadError(e));
      return null;
    } finally {
      if (id === contactReqId.current && !silent) setContactsLoadingState(false);
    }
  }, []);

  const loadThreadFor = useCallback(async (thread: SmsThread | null, device: string, silent: boolean): Promise<boolean> => {
    if (!thread) {
      setMessagesState([]);
      setHasMoreState(false);
      return false;
    }
    const id = ++threadReqId.current;
    if (!silent) setMessagesLoadingState(true);
    const query: ThreadQuery = { peer: thread.peer, limit: THREAD_PAGE };
    if (device !== "all") query.deviceId = device;
    else {
      query.modemImei = thread.modemImei;
      query.imsi = thread.imsi;
    }
    try {
      const list = sortMessages(await getThread(query));
      if (id !== threadReqId.current) return false;
      setMessagesState(list);
      setHasMoreState(list.length === THREAD_PAGE);
      return true;
    } catch (e) {
      if (id !== threadReqId.current) return false;
      setContactsError(toLoadError(e));
      return false;
    } finally {
      if (id === threadReqId.current && !silent) setMessagesLoadingState(false);
    }
  }, []);

  const selectContact = useCallback(
    async (key: string, opts: { syncRoute?: boolean; silent?: boolean; scrollToBottom?: boolean } = {}) => {
      const { syncRoute = true, silent = false, scrollToBottom = true } = opts;
      if (!key) return;
      setKey(key);
      if (syncRoute) syncQuery(deviceRef.current, key);
      const thread = contactsRef.current.find((t) => t.key === key) || null;
      if (!thread) {
        setMessagesState([]);
        setHasMoreState(false);
        return;
      }
      const ok = await loadThreadFor(thread, deviceRef.current, silent);
      if (ok) {
        markRead(thread.key);
        if (scrollToBottom) scrollToBottomNow();
      }
    },
    [loadThreadFor, syncQuery, scrollToBottomNow],
  );

  const resolveSelection = useCallback(
    async (
      device: string,
      contactsList: SmsThread[],
      opts: { syncRoute?: boolean; silent?: boolean; scrollToBottom?: boolean } = {},
    ) => {
      const { silent = false, scrollToBottom = false } = opts;
      const active = (keyRef.current && contactsList.find((t) => t.key === keyRef.current)) || null;
      if (active) {
        const ok = await loadThreadFor(active, device, silent);
        if (ok) {
          markRead(active.key);
          if (scrollToBottom) scrollToBottomNow();
        }
        return;
      }
      setMessagesState([]);
      setHasMoreState(false);
    },
    [loadThreadFor, scrollToBottomNow],
  );

  const clearSelection = useCallback(
    (syncRoute: boolean) => {
      setKey("");
      setMessagesState([]);
      setHasMoreState(false);
      setMessagesLoadingState(false);
      if (syncRoute) syncQuery(deviceRef.current, "");
    },
    [syncQuery],
  );

  const selectDevice = useCallback(
    async (raw: string) => {
      const device = String(raw || "all").trim() || "all";
      setDevice(device);
      setKey("");
      setMessagesState([]);
      setHasMoreState(false);
      setMessagesLoadingState(false);
      syncQuery(device, "");
      const contactsList = await loadContacts(device, false);
      if (contactsList && deviceRef.current === device) {
        await resolveSelection(device, contactsList, { syncRoute: false, silent: false, scrollToBottom: false });
      }
    },
    [loadContacts, resolveSelection, syncQuery],
  );

  const refreshCurrent = useCallback(
    async (silent: boolean) => {
      const contactsList = await loadContacts(deviceRef.current, silent);
      if (contactsList) {
        await resolveSelection(deviceRef.current, contactsList, { syncRoute: false, silent, scrollToBottom: !silent });
      }
    },
    [loadContacts, resolveSelection],
  );

  const refreshAll = useCallback(async () => {
    await loadDevices();
    await refreshCurrent(false);
  }, [loadDevices, refreshCurrent]);

  const pollRefresh = useCallback(async () => {
    if (contactsLoadingRef.current || messagesLoadingRef.current || loadingMoreRef.current) return;
    try {
      await refreshCurrent(true);
    } catch {
      /* ignore */
    }
  }, [refreshCurrent]);
  usePolling(pollRefresh, 5000, false);

  const loadMore = useCallback(async () => {
    const device = deviceRef.current;
    const thread = contactsRef.current.find((t) => t.key === keyRef.current) || null;
    if (!thread || !hasMoreRef.current || loadingMoreRef.current || messagesRef.current.length === 0) return;
    const el = detailRef.current;
    const prevTop = el?.scrollTop || 0;
    const prevHeight = el?.scrollHeight || 0;
    setLoadingMoreState(true);
    try {
      const oldest = messagesRef.current[0];
      const query: ThreadQuery = { peer: thread.peer, limit: THREAD_PAGE, beforeTs: oldest.timestamp, beforeId: oldest.id };
      if (device !== "all") query.deviceId = device;
      else {
        query.modemImei = thread.modemImei;
        query.imsi = thread.imsi;
      }
      const older = sortMessages(await getThread(query));
      setMessagesState([...older, ...messagesRef.current]);
      setHasMoreState(older.length === THREAD_PAGE);
      requestAnimationFrame(() => {
        const el2 = detailRef.current;
        if (!el2) return;
        el2.scrollTop = prevTop + Math.max(0, el2.scrollHeight - prevHeight);
      });
    } catch {
      /* ignore */
    } finally {
      setLoadingMoreState(false);
    }
  }, []);

  const onDetailScroll = useCallback(
    (e: UIEvent<HTMLDivElement>) => {
      clearLongPress();
      if (e.currentTarget.scrollTop <= 80) void loadMore();
    },
    [loadMore, clearLongPress],
  );

  const sendReply = useCallback(async () => {
    const thread = contactsRef.current.find((t) => t.key === keyRef.current) || null;
    if (!thread) return;
    const text = composer.trim();
    if (!text) return;
    const device = deviceRef.current;
    const deviceId = device !== "all" ? device : thread.deviceId || devices[0]?.id || "";
    if (!deviceId) {
      message.warning(t("暂无可用设备"));
      return;
    }
    setSending(true);
    try {
      const result = await sendSms({ deviceId, phone: thread.peer, message: text });
      showSmsSendOutcome(result);
      setComposer("");
      scrollToBottomNow();
      window.setTimeout(() => {
        void refreshCurrent(false);
      }, 800);
    } catch (e) {
      message.error(t("发送失败：") + apiMessage(e));
    } finally {
      setSending(false);
    }
  }, [composer, devices, refreshCurrent, scrollToBottomNow, t]);

  const openNewSms = useCallback(() => {
    setNewSmsDevice(deviceRef.current !== "all" ? deviceRef.current : devices[0]?.id || "");
    setNewSmsOpen(true);
  }, [devices]);

  const sendNewSms = useCallback(
    async (payload: NewSmsPayload) => {
      if (!payload.deviceId || !payload.phone || !payload.message) {
        message.warning(t("请填写完整信息"));
        return;
      }
      setSending(true);
      try {
        const res = await sendSms({ deviceId: payload.deviceId, phone: payload.phone, message: payload.message });
        showSmsSendOutcome(res);
        setNewSmsOpen(false);
        window.setTimeout(() => {
          void refreshCurrent(false);
        }, 800);
      } catch (e) {
        message.error(t("发送失败：") + apiMessage(e));
      } finally {
        setSending(false);
      }
    },
    [refreshCurrent, t],
  );

  const deleteMessageAction = useCallback(
    async (m: SMSMessage) => {
      if (!m.id || deletingMessageId === m.id) return;
      const ok = await confirmDialog(t("删除后无法恢复。仅删除短信检测历史记录。"), t("删除这条短信？"), {
        confirmText: t("删除"),
        cancelText: t("取消"),
        type: "warning",
      });
      if (!ok) return;
      setDeletingMessageId(m.id);
      try {
        const res = await deleteMessage(m.id);
        message.success(t("已删除短信"));
        await refreshCurrent(false);
        if (res?.threadEmpty) clearSelection(true);
      } catch (e) {
        message.error(tl("删除失败：") + apiMessage(e));
      } finally {
        setDeletingMessageId(null);
      }
    },
    [deletingMessageId, refreshCurrent, clearSelection, t],
  );

  const deleteThreadAction = useCallback(
    async (t: SmsThread) => {
      if (deletingThreadKey === t.key) return;
      const ok = await confirmDialog(
        lang === "zh"
          ? `将删除与 ${t.peer} 的全部短信历史，无法恢复。仅删除短信检测历史记录。`
          : `Delete ALL message history with ${t.peer}? This cannot be undone. Only the SMS test history is removed.`,
        tl("永久删除整个对话？"),
        { confirmText: tl("删除对话"), cancelText: tl("取消"), type: "warning" },
      );
      if (!ok) return;
      setDeletingThreadKey(t.key);
      try {
        const q: DeleteThreadQuery =
          deviceRef.current !== "all"
            ? { deviceId: deviceRef.current, peer: t.peer }
            : { deviceId: "all", modemImei: t.modemImei, imsi: t.imsi, peer: t.peer };
        await deleteThread(q);
        message.success(tl("已删除对话"));
        if (keyRef.current === t.key) clearSelection(true);
        await loadContacts(deviceRef.current, false);
      } catch (e) {
        message.error(tl("删除失败：") + apiMessage(e));
      } finally {
        setDeletingThreadKey(null);
      }
    },
    [deletingThreadKey, clearSelection, loadContacts, lang],
  );

  const closeActionSheet = useCallback(() => {
    setActionSheetOpen(false);
    setActionTarget(null);
  }, []);

  const confirmSheetAction = useCallback(async () => {
    const target = actionTarget;
    closeActionSheet();
    if (!target) return;
    if (target.type === "thread") await deleteThreadAction(target.thread);
    else await deleteMessageAction(target.message);
  }, [actionTarget, closeActionSheet, deleteThreadAction, deleteMessageAction]);

  const onBack = useCallback(() => {
    if (keyRef.current) clearSelection(true);
  }, [clearSelection]);

  useEffect(() => {
    setCanHover(
      typeof window.matchMedia === "function" && window.matchMedia("(hover: hover) and (pointer: fine)").matches,
    );
    const measure = () => {
      const w = pageRef.current?.clientWidth || 0;
      setWidth(w);
      isMobileRef.current = w > 0 && w < MOBILE_BREAKPOINT;
    };
    measure();
    let ro: ResizeObserver | null = null;
    if (typeof ResizeObserver !== "undefined" && pageRef.current) {
      ro = new ResizeObserver(measure);
      ro.observe(pageRef.current);
    } else {
      window.addEventListener("resize", measure);
    }
    return () => {
      ro?.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const initialDevice = deviceRef.current;
      const [, contactsList] = await Promise.all([loadDevices(), loadContacts(initialDevice, false)]);
      if (cancelled) return;
      let list = contactsList;
      if (!list && deviceRef.current !== initialDevice) {
        list = await loadContacts(deviceRef.current, false);
        if (cancelled) return;
      }
      await resolveSelection(deviceRef.current, list ?? [], {});
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => () => clearLongPress(), [clearLongPress]);

  return (
    <div ref={pageRef} className="sms-page flex h-[calc(100vh-140px)] flex-col">
      <PageHeader
        title={t("短信功能检测")}
        subtitle={t("通过收发测试验证模组 SMS 收发功能是否正常")}
        actions={
          <div className="flex items-center gap-2">
            <RefreshButton loading={contactsLoading} onClick={refreshAll} />
            <Button variant="primary" onClick={openNewSms} className="font-bold !border-0" icon={<AddRegular />}>
              {t("发送测试短信")}
            </Button>
          </div>
        }
      />
      {devicesError ? (
        <ErrorState
          className="mb-4"
          title={t("设备列表加载失败")}
          message={devicesError.message}
          statusCode={devicesError.status}
          lastSuccessAt={devicesOkAt}
          retryText={t("重试")}
          onRetry={loadDevices}
        />
      ) : null}
      {contactsError ? (
        <ErrorState
          className="mb-6"
          title={t("短信加载失败")}
          message={contactsError.message}
          statusCode={contactsError.status}
          lastSuccessAt={contactsOkAt}
          retryText={t("重试")}
          onRetry={refreshAll}
        />
      ) : null}
      <div className="relative flex-1 overflow-hidden ui-card">
        {contactsLoading && contacts.length === 0 ? (
          <div className="absolute inset-0 z-20 flex items-center justify-center bg-white/50 backdrop-blur-sm dark:bg-black/20">
            <Spinner className="h-7 w-7 text-[#0ea5e9]" />
          </div>
        ) : null}
        <div className="sms-main-layout">
          {isDesktop ? (
            <div className="flex flex-col border-r border-gray-100 dark:border-white/10">
              <div className="border-b border-gray-100 p-4 dark:border-white/10">
                <div className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("设备")}</div>
              </div>
              <div className="space-y-1 overflow-auto p-3">
                {deviceFilters.map((d) => (
                  <button
                    key={d.id}
                    type="button"
                    onClick={() => void selectDevice(d.id)}
                    className={cx(
                      "flex w-full items-center justify-between gap-3 rounded-xl border px-3 py-2 text-left transition-all",
                      selectedDevice === d.id
                        ? "border-indigo-200 bg-indigo-50/70 dark:border-indigo-500/30 dark:bg-indigo-500/10"
                        : "border-transparent hover:bg-gray-50/60 dark:hover:bg-white/5",
                    )}
                  >
                    <div className="min-w-0">
                      <div className="truncate text-sm font-bold text-gray-800 dark:text-gray-100">{d.label}</div>
                      <div className="truncate text-xs text-gray-400">{d.id === "all" ? t("汇总全部设备检测记录") : d.id}</div>
                    </div>
                    {d.id !== "all" ? (
                      <span className={cx("h-2 w-2 rounded-full", d.healthy ? "bg-green-500" : "bg-red-500")} />
                    ) : null}
                  </button>
                ))}
              </div>
            </div>
          ) : null}
          {showContactColumn ? (
            <ContactList
              isMobile={isMobile}
              isDesktop={isDesktop}
              selectedDevice={selectedDevice}
              deviceOptions={deviceSelectOptions}
              onSelectDevice={(id) => void selectDevice(id)}
              searchQuery={searchQuery}
              onSearchChange={setSearch}
              loading={contactsLoading}
              contacts={filteredContacts}
              activeKey={selectedKey}
              isUnread={isUnread}
              deletingKey={deletingThreadKey}
              canHover={canHover}
              onSelect={(key) => void selectContact(key)}
              onDelete={(t) => void deleteThreadAction(t)}
              onRowPointerDown={onThreadPointerDown}
              onRowPointerMove={moveLongPress}
              onRowPointerEnd={clearLongPress}
            />
          ) : null}
          {showDetailColumn ? (
            <ThreadPanel
              isMobile={isMobile}
              isDesktop={isDesktop}
              selectedDevice={selectedDevice}
              activeThread={activeThread}
              canLoadMore={!!activeThread && hasMore}
              loadingMore={loadingMore}
              groups={groups}
              deletingMessageId={deletingMessageId}
              canHover={canHover}
              composer={composer}
              composerInfo={composerInfo}
              composerLength={composerLength}
              sending={sending}
              detailRef={detailRef}
              composerRef={composerRef}
              onBack={onBack}
              onScrollToBottom={scrollToBottomNow}
              onLoadMore={() => void loadMore()}
              onDeleteMessage={(m) => void deleteMessageAction(m)}
              onComposerChange={setComposer}
              onSend={() => void sendReply()}
              onDetailScroll={onDetailScroll}
              onMsgPointerDown={onMsgPointerDown}
              onMsgPointerMove={moveLongPress}
              onMsgPointerEnd={clearLongPress}
            />
          ) : null}
        </div>
      </div>
      {actionSheetOpen && isMobile && actionTarget ? (
        <div className="sms-action-sheet-mask animate-[fade-slide-in_0.18s_ease]" onClick={closeActionSheet}>
          <div className="sms-action-sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sms-action-sheet-title">{t("操作")}</div>
            <Button
              className="sms-danger-ghost-btn !w-full !justify-center"
              icon={<DeleteRegular />}
              onClick={() => void confirmSheetAction()}
            >
              {actionTarget.type === "thread" ? t("删除对话") : t("删除短信")}
            </Button>
            <Button className="!w-full !justify-center" onClick={closeActionSheet}>
              {t("取消")}
            </Button>
          </div>
        </div>
      ) : null}
      <NewSmsModal
        open={newSmsOpen}
        devices={devices}
        defaultDeviceId={newSmsDevice}
        sending={sending}
        onClose={() => setNewSmsOpen(false)}
        onSend={sendNewSms}
      />
    </div>
  );
}
