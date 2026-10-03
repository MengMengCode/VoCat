import { useState } from "react";
import type { CellLockTarget } from "../../types";
import { cx } from "../../lib/utils";
import { useI18n } from "../../lib/i18n";
import { Button, Input, Modal, Tag } from "../ui";
import type { DeviceCellLockView } from "./useDeviceCellLock";

function cellTargetText(cell: CellLockTarget) {
  return `EARFCN ${cell.earfcn} / PCI ${cell.pci}`;
}

export function CellLockDialog({ cellLock }: { cellLock: DeviceCellLockView }) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<{ earfcn: string; pci: string } | null>(null);
  const { operation, status, cells, lockError, cellsError, readingStatus, readingCells } = cellLock;
  const prefill = status?.target;
  const { earfcn, pci } = draft ?? { earfcn: String(prefill?.earfcn ?? ""), pci: String(prefill?.pci ?? "") };
  function editDraft(update: Partial<{ earfcn: string; pci: string }>) {
    setDraft(current => ({ earfcn, pci, ...current, ...update }));
  }
  const busy = operation !== null;
  const earfcnValid = /^\d+$/.test(earfcn.trim()) && Number(earfcn) <= 262143;
  const pciValid = /^\d+$/.test(pci.trim()) && Number(pci) <= 503;
  const target = earfcnValid && pciValid ? { earfcn: Number(earfcn), pci: Number(pci) } : null;

  function renderStatus() {
    if (busy) return <span role="status">{operation === "locking" ? t("正在应用小区锁定配置...") : t("正在解除小区锁定...")}</span>;
    if (readingStatus) return <span>{t("正在读取硬件锁定配置...")}</span>;
    if (!status) return <span>{t("锁定状态未知")}</span>;
    return <span>{t(status.target ? "已配置锁定" : "未配置锁定")}</span>;
  }

  return (
    <Modal
      open
      onClose={cellLock.closeDialog}
      title={t("小区锁定")}
      showClose={!busy}
    >
      <div className="mt-1 space-y-4 text-sm text-gray-700 dark:text-gray-200">
        <section className="rounded-xl border border-gray-200 bg-gray-50/80 p-4 dark:border-white/10 dark:bg-white/5" aria-labelledby="cell-lock-status-title">
          <h3 id="cell-lock-status-title" className="font-semibold text-gray-900 dark:text-white">{t("硬件锁定配置")}</h3>
          <div className={cx("mt-1", status ? (status.target ? "text-amber-700 dark:text-amber-300" : "text-emerald-700 dark:text-emerald-300") : "text-gray-500 dark:text-gray-400")}>
            {renderStatus()}
          </div>
          {status && status.target ? (
            <div className="mt-3 text-xs text-gray-600 dark:text-gray-300">
              <span className="font-medium">{t("已配置目标")}: </span><span className="font-mono">{cellTargetText(status.target)}</span>
            </div>
          ) : null}
          {busy ? <p className="mt-3 text-xs text-gray-500 dark:text-gray-400">{t("正在等待硬件确认，请勿断开设备。")}</p> : null}
          <p className="mt-3 text-xs text-gray-500 dark:text-gray-400">{t("仅确认锁定配置，不保证已生效或驻网；部分固件需手动设置 LTE-only 或重启，操作可能中断联网。")}</p>
          {lockError ? (
            <div className="mt-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-xs leading-5 text-red-800 dark:border-red-500/25 dark:bg-red-500/10 dark:text-red-200" role="alert">
              <p className="whitespace-pre-wrap break-words">{lockError}</p>
            </div>
          ) : null}
        </section>

        <section aria-labelledby="cell-list-title">
          <div className="mb-2 flex items-baseline justify-between gap-3">
            <h3 id="cell-list-title" className="font-semibold text-gray-900 dark:text-white">{t("小区列表")}</h3>
            <Button onClick={cellLock.readCells} loading={readingCells} disabled={busy}>{t("刷新小区列表")}</Button>
          </div>
          {cells ? <p className="mb-2 text-xs text-gray-500 dark:text-gray-400">{cells.neighborsStatus === "available" ? t("服务小区与邻区") : t("服务小区")}</p> : null}
          {!busy && cells?.items.length ? (
            <div className="space-y-2">
              {cells.items.map((cell, index) => {
                const selected = target?.earfcn === cell.earfcn && target?.pci === cell.pci;
                const configured = status?.target?.earfcn === cell.earfcn && status?.target?.pci === cell.pci;
                return (
                  <button
                    key={`${cell.source}-${cell.earfcn}-${cell.pci}-${index}`}
                    type="button"
                    onClick={() => editDraft({ earfcn: String(cell.earfcn), pci: String(cell.pci) })}
                    aria-pressed={selected}
                    className={cx(
                      "w-full rounded-lg border px-3 py-2.5 text-left transition-colors",
                      selected ? "border-sky-400 bg-sky-50 dark:border-sky-500/50 dark:bg-sky-500/10" : "border-gray-200 hover:border-sky-300 hover:bg-sky-50/60 dark:border-white/10 dark:hover:border-sky-500/40 dark:hover:bg-sky-500/5",
                    )}
                  >
                    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1">
                      <span className="font-mono font-semibold text-gray-900 dark:text-white">{cellTargetText(cell)}</span>
                      <span className="text-xs text-gray-500 dark:text-gray-400">{cell.plmn || "--"} · LTE</span>
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                      <span>{cell.source === "serving" ? t("服务小区") : t("邻区")}</span>
                      {configured ? <Tag type="warning">{t("已配置锁定")}</Tag> : null}
                      <span>{[
                        cell.rsrp !== undefined ? `RSRP ${cell.rsrp} dBm` : null,
                        cell.rsrq !== undefined ? `RSRQ ${cell.rsrq} dB` : null,
                        cell.rssi !== undefined ? `RSSI ${cell.rssi} dBm` : null,
                        cell.sinr !== undefined ? `SINR ${cell.sinr} dB` : null,
                      ].filter(Boolean).join(" · ") || t("没有可用的信号测量值")}</span>
                    </div>
                  </button>
                );
              })}
            </div>
          ) : (
            <div className="rounded-lg border border-dashed border-gray-300 px-3 py-4 text-center text-xs text-gray-500 dark:border-white/15 dark:text-gray-400">
              {busy ? t("配置变更中，小区列表暂不显示。")
                : cells ? t("读取成功，但没有发现可用小区；仍可在下方手动输入目标。")
                : readingCells ? t("正在读取小区列表...")
                : t("小区列表不可用；仍可在下方手动输入目标。")}
            </div>
          )}
          {cellsError ? (
            <div className="mt-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-800 dark:border-amber-500/25 dark:bg-amber-500/10 dark:text-amber-200" role="status">
              <span>{cellsError}</span>
            </div>
          ) : null}
          {cells && cells.neighborsStatus !== "available" ? (
            <p className="mt-2 text-xs text-gray-500 dark:text-gray-400">
              {cells.neighborsStatus === "unsupported"
                ? t("邻区查询不受此模组或固件支持")
                : t("本次邻区查询不可用")}
            </p>
          ) : null}
        </section>

        <section className="space-y-3" aria-labelledby="manual-target-title">
          <h3 id="manual-target-title" className="font-semibold text-gray-900 dark:text-white">{t("手动输入 EARFCN / PCI")}</h3>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label className="space-y-1.5 text-xs font-medium">
              <span>EARFCN</span>
              <Input type="text" inputMode="numeric" autoComplete="off" value={earfcn} onChange={(event) => editDraft({ earfcn: event.target.value })} placeholder={t("输入 EARFCN")} disabled={busy} aria-invalid={earfcn.length > 0 && !earfcnValid} />
              <span className="block font-normal text-gray-500 dark:text-gray-400">{t("请输入 0 到 262143 的整数；实际是否支持由模组固件决定。")}</span>
            </label>
            <label className="space-y-1.5 text-xs font-medium">
              <span>PCI</span>
              <Input type="text" inputMode="numeric" autoComplete="off" value={pci} onChange={(event) => editDraft({ pci: event.target.value })} placeholder={t("输入 PCI")} disabled={busy} aria-invalid={pci.length > 0 && !pciValid} />
              <span className="block font-normal text-gray-500 dark:text-gray-400">{t("请输入 0 到 503 的整数。")}</span>
            </label>
          </div>
          {((earfcn.length > 0 && !earfcnValid) || (pci.length > 0 && !pciValid)) ? (
            <p className="text-xs text-red-600 dark:text-red-300">{t("请输入 0 到 262143 的整数 EARFCN，以及 0 到 503 的整数 PCI。")}</p>
          ) : null}

          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="danger" plain onClick={() => { editDraft({}); void cellLock.setCellLock(null); }} loading={operation === "unlocking"} disabled={busy || status?.target === null}>
              {t("解除锁定")}
            </Button>
            <Button variant="primary" onClick={() => { if (target) { editDraft({}); void cellLock.setCellLock(target); } }} loading={operation === "locking"} disabled={busy || !target}>
              {t("锁定此小区")}
            </Button>
          </div>
        </section>
      </div>
    </Modal>
  );
}
