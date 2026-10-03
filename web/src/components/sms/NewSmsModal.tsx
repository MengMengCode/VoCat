import { useEffect, useMemo, useState, type ReactNode } from "react";
import { SendRegular } from "@fluentui/react-icons";
import { Button, Input, Modal, Select, Textarea } from "../ui";
import type { DeviceListItem } from "../../types";
import { analyzeSmsEncoding } from "./smsText";
import { tf, useI18n } from "../../lib/i18n";

export interface NewSmsPayload {
  deviceId: string;
  phone: string;
  message: string;
}

export interface NewSmsModalProps {
  open: boolean;
  devices: DeviceListItem[];
  defaultDeviceId: string;
  sending: boolean;
  onClose: () => void;
  onSend: (payload: NewSmsPayload) => void;
}

function FormItem({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div>
      <label className="mb-1.5 block text-sm font-medium text-gray-700 dark:text-gray-300">{label}</label>
      {children}
    </div>
  );
}

export function NewSmsModal({ open, devices, defaultDeviceId, sending, onClose, onSend }: NewSmsModalProps) {
  const { t } = useI18n();
  const [deviceId, setDeviceId] = useState(defaultDeviceId);
  const [phone, setPhone] = useState("");
  const [content, setContent] = useState("");

  // Reset the form every time the dialog opens (reference `Yt`).
  useEffect(() => {
    if (open) {
      setDeviceId(defaultDeviceId);
      setPhone("");
      setContent("");
    }
  }, [open, defaultDeviceId]);

  const deviceOptions = useMemo(
    () => devices.map((d) => ({ value: d.id, label: d.name || d.id })),
    [devices],
  );
  const info = useMemo(() => analyzeSmsEncoding(content), [content]);
  const length = useMemo(() => Array.from(content || "").length, [content]);

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t("发送短信")}
      footer={
        <>
          <Button onClick={onClose}>{t("取消")}</Button>
          <Button variant="primary" loading={sending} icon={<SendRegular />} onClick={() => onSend({ deviceId, phone, message: content })}>
            {t("发送")}
          </Button>
        </>
      }
    >
      <div className="mt-2 space-y-4">
        <FormItem label={t("发送设备")}>
          <Select value={deviceId} onChange={setDeviceId} options={deviceOptions} placeholder={t("选择设备")} />
        </FormItem>
        <FormItem label={t("目标号码")}>
          <Input value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="+12025550177" />
        </FormItem>
        <FormItem label={t("短信内容")}>
          <Textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            placeholder={t("输入短信内容...")}
            rows={4}
            className="max-h-64 resize-none [field-sizing:content]"
          />
          <div className="mt-2 flex justify-end text-xs text-gray-400">
            {tf("{encoding} · 预计 {parts} 段 · {length} 字", { encoding: info.encoding, parts: info.parts, length })}
          </div>
        </FormItem>
      </div>
    </Modal>
  );
}
