import { useLayoutEffect, useRef, useState } from "react";
import { api, apiMessage } from "../../api";
import type { CellLockStatus, CellLockTarget, DeviceCells } from "../../types";
import { useI18n } from "../../lib/i18n";
import { message } from "../ui";

interface CellLockState {
  dialogOpen: boolean;
  status: CellLockStatus | null;
  cells: DeviceCells | null;
  lockError: string | null;
  cellsError: string | null;
  readingStatus: boolean;
  readingCells: boolean;
  operation: "locking" | "unlocking" | null;
}

const initialState: CellLockState = {
  dialogOpen: false,
  status: null,
  cells: null,
  lockError: null,
  cellsError: null,
  readingStatus: false,
  readingCells: false,
  operation: null,
};

type ReadKind = "status" | "cells";
export type DeviceCellLockView = ReturnType<typeof useDeviceCellLock>;

export function useDeviceCellLock(deviceId: string, enabled: boolean) {
  const { t } = useI18n();
  const devicePath = `/devices/${encodeURIComponent(deviceId)}`;
  const [state, setState] = useState(initialState);
  const reads = useRef<Record<ReadKind, AbortController | null>>({ status: null, cells: null });
  const writing = useRef(false);
  const mounted = useRef(false);

  function update(patch: Partial<CellLockState>) {
    setState(current => ({ ...current, ...patch }));
  }

  function cancelReads() {
    for (const kind of ["status", "cells"] as const) {
      reads.current[kind]?.abort();
      reads.current[kind] = null;
    }
  }

  async function read(kind: ReadKind) {
    if (!enabled || writing.current) return;
    reads.current[kind]?.abort();
    const request = reads.current[kind] = new AbortController();
    update(kind === "status"
      ? { lockError: null, readingStatus: true }
      : { cells: null, cellsError: null, readingCells: true });
    try {
      if (kind === "status") {
        const status = await api<CellLockStatus>(`${devicePath}/cell-lock`, { signal: request.signal });
        if (!request.signal.aborted) update({ status });
      } else {
        const cells = await api<DeviceCells>(`${devicePath}/cells`, { signal: request.signal });
        if (!request.signal.aborted) update({ cells });
      }
    } catch (cause) {
      if (!request.signal.aborted) {
        const error = apiMessage(cause);
        update(kind === "status" ? { status: null, lockError: error } : { cellsError: error });
      }
    } finally {
      if (!request.signal.aborted) {
        reads.current[kind] = null;
        update(kind === "status" ? { readingStatus: false } : { readingCells: false });
      }
    }
  }

  useLayoutEffect(() => {
    mounted.current = true;
    void read("status");
    return () => {
      mounted.current = false;
      cancelReads();
    };
  }, [deviceId, enabled]);

  function openDialog() {
    if (!enabled || writing.current) return;
    setState({ ...initialState, dialogOpen: true });
    void read("status");
    void read("cells");
  }

  function closeDialog() {
    if (writing.current) return;
    cancelReads();
    setState(current => ({ ...initialState, status: current.status }));
  }

  async function setCellLock(target: CellLockTarget | null) {
    if (!enabled || writing.current) return;
    writing.current = true;
    cancelReads();
    update({ status: null, lockError: null, readingStatus: false, readingCells: false, operation: target ? "locking" : "unlocking" });
    try {
      const status = await api<CellLockStatus>(`${devicePath}/cell-lock`, {
        method: target ? "PUT" : "DELETE",
        body: target ?? undefined,
      });
      if (mounted.current) {
        setState({ ...initialState, status });
        message.success(t(target ? "小区锁定配置已保存" : "小区锁定配置已清除"));
      }
    } catch (cause) {
      if (mounted.current) update({ lockError: apiMessage(cause), operation: null });
    } finally {
      writing.current = false;
    }
  }

  return {
    ...state,
    openDialog,
    closeDialog,
    readCells: () => void read("cells"),
    setCellLock,
  };
}
