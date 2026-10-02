export type SMSExportFormat = "json" | "html";

export interface SMSExportOptions {
  deviceId: string;
  format: SMSExportFormat;
  timeRange: "all" | "range";
  startDate: string;
  endDate: string;
}

function localDay(value: string): Date {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) throw new Error("请选择有效的开始和结束日期");
  const [, year, month, day] = match.map(Number);
  const date = new Date(0);
  date.setFullYear(year, month - 1, day);
  date.setHours(0, 0, 0, 0);
  if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day) {
    throw new Error("请选择有效的开始和结束日期");
  }
  return date;
}

export function buildSMSExportQuery(options: SMSExportOptions): URLSearchParams {
  const query = new URLSearchParams({ format: options.format });
  if (options.format === "html") {
    query.set("timezone", Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  }
  if (options.deviceId && options.deviceId !== "all") query.set("device_id", options.deviceId);
  if (options.timeRange === "range") {
    const start = localDay(options.startDate);
    const end = localDay(options.endDate);
    if (start > end) throw new Error("开始日期不能晚于结束日期");
    // Calendar arithmetic, not +24 hours: selected local days may cross DST.
    end.setDate(end.getDate() + 1);
    query.set("since", start.toISOString());
    query.set("until", end.toISOString());
  }
  return query;
}

export async function readSMSExportDownload(response: Response, format: SMSExportFormat) {
  if (response.status !== 200) throw new Error("短信导出失败，请重试");
  const expectedType = format === "json" ? "application/json" : "text/html";
  const contentType = response.headers.get("Content-Type")?.split(";")[0].trim();
  const countText = response.headers.get("X-SMS-Export-Count");
  if (contentType !== expectedType || countText === null || !/^\d+$/.test(countText)) {
    throw new Error("导出响应不完整，请重试");
  }
  const count = Number(countText);
  if (!Number.isSafeInteger(count)) throw new Error("导出响应不完整，请重试");
  const blob = await response.blob();
  if (!blob.size) throw new Error("导出响应不完整，请重试");
  const name = /filename="(vocat-sms-[0-9TZ]+\.(?:json|html))"/.exec(response.headers.get("Content-Disposition") || "")?.[1];
  return { blob, count, filename: name || `vocat-sms.${format}` };
}
