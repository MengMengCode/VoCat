import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const source = await readFile(new URL("../src/components/sms/smsExport.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
});
const { buildSMSExportQuery, readSMSExportDownload } = await import(
  `data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`
);
const defaults = { deviceId: "all", format: "json", timeRange: "all", startDate: "", endDate: "" };

test("all-time export ignores stale date inputs and does not impose pagination", () => {
  const query = buildSMSExportQuery({ ...defaults, startDate: "bad", endDate: "bad" });
  assert.deepEqual([...query], [["format", "json"]]);
  const device = buildSMSExportQuery({ ...defaults, deviceId: "device & 1", format: "html" });
  assert.equal(device.get("device_id"), "device & 1");
  assert.equal(device.get("format"), "html");
});

test("HTML captures the browser timezone at export time while JSON remains UTC", () => {
  const previousTZ = process.env.TZ;
  try {
    for (const zone of ["Asia/Shanghai", "America/New_York", "UTC"]) {
      process.env.TZ = zone;
      const html = buildSMSExportQuery({ ...defaults, format: "html" });
      assert.equal(html.get("timezone"), zone);
      assert.equal(buildSMSExportQuery(defaults).has("timezone"), false);
    }
  } finally {
    if (previousTZ === undefined) delete process.env.TZ;
    else process.env.TZ = previousTZ;
  }
});

test("explicit date ranges never silently broaden when dates are invalid", () => {
  for (const [startDate, endDate] of [["", "2026-10-02"], ["2026-10-02", ""], ["2026-02-30", "2026-03-01"], ["2026-10-03", "2026-10-02"], ["2026-2-01", "2026-02-02"]]) {
    assert.throws(() => buildSMSExportQuery({ ...defaults, timeRange: "range", startDate, endDate }));
  }
});

test("date filters include both complete selected local days, including DST", () => {
  const previousTZ = process.env.TZ;
  try {
    process.env.TZ = "Asia/Shanghai";
    let query = buildSMSExportQuery({ ...defaults, timeRange: "range", startDate: "2026-10-02", endDate: "2026-10-02" });
    assert.equal(query.get("since"), "2026-10-01T16:00:00.000Z");
    assert.equal(query.get("until"), "2026-10-02T16:00:00.000Z");
    process.env.TZ = "America/New_York";
    query = buildSMSExportQuery({ ...defaults, timeRange: "range", startDate: "2026-03-08", endDate: "2026-03-08" });
    assert.equal(query.get("since"), "2026-03-08T05:00:00.000Z");
    assert.equal(query.get("until"), "2026-03-09T04:00:00.000Z");
    query = buildSMSExportQuery({ ...defaults, timeRange: "range", startDate: "2026-11-01", endDate: "2026-11-01" });
    assert.equal(query.get("since"), "2026-11-01T04:00:00.000Z");
    assert.equal(query.get("until"), "2026-11-02T05:00:00.000Z");
  } finally {
    if (previousTZ === undefined) delete process.env.TZ;
    else process.env.TZ = previousTZ;
  }
});

function archiveResponse(body, options = {}) {
  return new Response(body, { headers: {
    "Content-Type": "application/json; charset=utf-8",
    "X-SMS-Export-Count": "1",
    "Content-Disposition": 'attachment; filename="vocat-sms-20261002T120000Z.json"',
    ...options,
  } });
}

test("download preserves JSON keys and message text exactly", async () => {
  const text = JSON.stringify({ messages: [{ message_id: "abc", body: "验证码 1234\n你好😀 <tag>", extra: { keep_this_key: true } }], message_count: 1 });
  const result = await readSMSExportDownload(archiveResponse(text), "json");
  assert.equal(await result.blob.text(), text);
  assert.equal(result.count, 1);
  assert.equal(result.filename, "vocat-sms-20261002T120000Z.json");
});

test("valid empty and HTML archives are downloadable", async () => {
  const empty = await readSMSExportDownload(archiveResponse('{"messages":[],"message_count":0}', { "X-SMS-Export-Count": "0" }), "json");
  assert.equal(empty.count, 0);
  const html = await readSMSExportDownload(archiveResponse("<!doctype html><p>你好</p>", { "Content-Type": "text/html; charset=utf-8", "Content-Disposition": "attachment" }), "html");
  assert.equal(html.filename, "vocat-sms.html");
  assert.equal(await html.blob.text(), "<!doctype html><p>你好</p>");
});

test("errors, partial responses, missing counts and empty bodies do not become downloads", async () => {
  for (const response of [
    new Response("denied", { status: 401 }),
    new Response("failed", { status: 500 }),
    new Response("partial", { status: 206 }),
    new Response("{}", { headers: { "Content-Type": "application/json" } }),
    archiveResponse("{}", { "Content-Type": "text/html" }),
    archiveResponse("{}", { "X-SMS-Export-Count": "NaN" }),
    archiveResponse("{}", { "X-SMS-Export-Count": "9007199254740992" }),
    archiveResponse(""),
  ]) {
    await assert.rejects(readSMSExportDownload(response, "json"));
  }
});
