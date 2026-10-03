import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../../api";
import { Modal } from "../ui";
import { useI18n } from "../../lib/i18n";

export interface CarrierWebsheet {
  id?: string;
  title?: string;
  embedUrl?: string;
}

export interface CarrierWebsheetDialogProps {
  open: boolean;
  websheet: CarrierWebsheet | null;
  onClose: () => void;
  onDone: () => void;
}

export function CarrierWebsheetDialog({ open, websheet, onClose, onDone }: CarrierWebsheetDialogProps) {
  const { t } = useI18n();
  const [loaded, setLoaded] = useState(false);
  const doneRef = useRef(false);
  const embedUrl = websheet?.embedUrl || "";
  const token = useMemo(() => {
    if (!embedUrl) return "";
    try {
      return new URL(embedUrl, window.location.origin).searchParams.get("token") || "";
    } catch {
      return "";
    }
  }, [embedUrl]);

  useEffect(() => {
    setLoaded(false);
  }, [websheet?.id]);

  useEffect(() => {
    function shouldIgnore(callback: unknown): boolean {
      if (!callback || typeof callback !== "object") return true;
      const c = callback as Record<string, unknown>;
      const k = String(c.event ?? c.method ?? c.resultCode ?? "").toLowerCase();
      return k ? !k.includes("phoneservicesaccountstatuschanged") : true;
    }
    function isValid(data: unknown): data is { type: string; token?: string; callback?: unknown } {
      if (!data || typeof data !== "object") return false;
      const d = data as Record<string, unknown>;
      if (d.type !== "vohive-websheet-callback") return false;
      const t = typeof d.token === "string" ? d.token : "";
      return !(token && t && t !== token);
    }
    async function relay(callback: unknown) {
      const id = websheet?.id;
      if (!id || !callback || typeof callback !== "object") return;
      try {
        await api(`/websheets/${id}/callback`, { method: "POST", body: callback });
      } catch (e) {
        console.error("[CarrierWebsheetDialog] relay callback failed:", e);
      }
    }
    async function done() {
      if (doneRef.current) return;
      doneRef.current = true;
      try {
        const id = websheet?.id;
        if (id) {
          try {
            await api(`/websheets/${id}/done`, { method: "POST" });
          } catch (e) {
            console.error("[CarrierWebsheetDialog] complete websheet failed:", e);
          }
        }
        onDone();
        onClose();
      } finally {
        doneRef.current = false;
      }
    }
    function handle(data: unknown) {
      if (!open || !isValid(data)) return;
      if (shouldIgnore(data.callback)) void done();
      else void relay(data.callback);
    }
    const onMessage = (e: MessageEvent) => handle(e.data);
    const onStorage = (e: StorageEvent) => {
      if (e.key !== "vohive-websheet-complete" || !e.newValue) return;
      try {
        handle(JSON.parse(e.newValue));
      } catch {
        /* ignore */
      }
    };
    window.addEventListener("message", onMessage);
    window.addEventListener("storage", onStorage);
    let channel: BroadcastChannel | null = null;
    try {
      channel = new BroadcastChannel("vohive-websheet");
      channel.onmessage = (e) => handle(e.data);
    } catch {
      channel = null;
    }
    return () => {
      window.removeEventListener("message", onMessage);
      window.removeEventListener("storage", onStorage);
      channel?.close();
    };
  }, [open, websheet?.id, token, onClose, onDone]);

  return (
    <Modal open={open} onClose={onClose} title={websheet?.title || t("E911地址")}>
      <div className="websheet-frame-shell relative overflow-hidden rounded border border-gray-200 dark:border-gray-700">
        {!loaded ? (
          <div className="absolute inset-0 z-10 flex items-center justify-center bg-white/80 text-sm text-gray-500 dark:bg-gray-900/80">
            {t("加载中...")}
          </div>
        ) : null}
        {embedUrl ? (
          <iframe
            src={embedUrl}
            title={websheet?.title || t("E911地址")}
            className="block h-full w-full border-0"
            sandbox="allow-forms allow-same-origin allow-scripts allow-popups allow-popups-to-escape-sandbox"
            onLoad={() => setLoaded(true)}
          />
        ) : null}
      </div>
    </Modal>
  );
}
