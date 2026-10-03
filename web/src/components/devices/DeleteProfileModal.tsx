import { Button, Input, Modal } from "../ui";
import { tf, useI18n } from "../../lib/i18n";

export interface DeleteProfileTarget {
  iccid: string;
  name?: string;
  aidHex?: string;
}

export interface DeleteProfileModalProps {
  open: boolean;
  target: DeleteProfileTarget | null;
  deleting: boolean;
  input: string;
  onInputChange: (v: string) => void;
  onCancel: () => void;
  onConfirm: () => void;
}

export function DeleteProfileModal({ open, target, deleting, input, onInputChange, onCancel, onConfirm }: DeleteProfileModalProps) {
  const { t } = useI18n();
  const last4 = (target?.iccid || "").slice(-4);
  return (
    <Modal open={open} onClose={onCancel} title={t("⚠️ 删除 Profile")}>
      <div className="text-sm leading-relaxed text-gray-600 dark:text-gray-300">
        {tf("此操作不可逆！请输入 ICCID 后 4 位「{last4}」以确认删除 Profile「{name}」", { last4, name: target?.name || target?.iccid })}
      </div>
      <div className="mt-4">
        <Input value={input} onChange={(e) => onInputChange(e.target.value)} placeholder={tf("输入 {last4}", { last4 })} autoFocus />
      </div>
      <div className="mt-6 flex items-center justify-end gap-3">
        <Button onClick={onCancel}>{t("取消")}</Button>
        <Button variant="danger" loading={deleting} disabled={input !== last4} onClick={onConfirm}>
          {t("确认删除")}
        </Button>
      </div>
    </Modal>
  );
}
