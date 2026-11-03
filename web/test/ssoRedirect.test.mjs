import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const source = await readFile(new URL("../src/lib/safeRedirect.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 } });
const { safeRedirect } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`);
const vectors = JSON.parse(await readFile(new URL("./fixtures/ssoRedirect.json", import.meta.url), "utf8"));

test("front-end redirects match the shared server security vectors", () => {
  for (const { input, expected } of vectors) {
    assert.equal(safeRedirect(input, "https://vocat.example.com"), expected, JSON.stringify(input));
  }
});

test("URLSearchParams decodes a redirect exactly once and keeps query encodings", () => {
  const original = "/sms?keyword=%E6%B5%8B%E8%AF%95&encoded=%252f#latest";
  const query = new URLSearchParams({ redirect: original });
  assert.equal(safeRedirect(new URLSearchParams(query.toString()).get("redirect")), original);
  assert.equal(safeRedirect(new URLSearchParams("redirect=%252F%252Fevil.example").get("redirect")), "/");
  assert.equal(safeRedirect("/sms?keyword=" + "a".repeat(4096)), "/");
  assert.equal(safeRedirect("/" + "测".repeat(1366)), "/");
});
