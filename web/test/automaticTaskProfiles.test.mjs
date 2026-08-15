import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const source = await readFile(new URL("../src/lib/automaticTaskProfiles.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
});
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`;
const {
  buildAutomaticTaskProfileOptions,
  createAutomaticTaskProfileRequestGuard,
  selectAutomaticTaskProfileOption,
} = await import(moduleURL);

test("uses the current physical SIM when the device has no eSIM profiles", () => {
  const iccid = "89441000400128014257";

  assert.deepEqual(buildAutomaticTaskProfileOptions([], iccid, "Current SIM"), [
    {
      iccid,
      aidHex: "",
      label: `Current SIM · ${iccid}`,
    },
  ]);
});

test("does not duplicate the current SIM when it is already in the eSIM inventory", () => {
  const iccid = "89441000400128014257";

  assert.deepEqual(
    buildAutomaticTaskProfileOptions(
      [{ aidHex: "a0000005591010ffffffff8900000100", profiles: [{ iccid, name: "Travel" }] }],
      iccid,
      "Current SIM",
    ),
    [
      {
        iccid,
        aidHex: "a0000005591010ffffffff8900000100",
        label: `Travel · ${iccid}`,
      },
    ],
  );
});

test("does not replace a saved profile when a failed inventory only exposes the current SIM", () => {
  const currentICCID = "89441000400128014257";
  const savedICCID = "89104100000028106378";
  const options = buildAutomaticTaskProfileOptions([], currentICCID, "Current SIM");

  assert.equal(selectAutomaticTaskProfileOption(options, savedICCID), undefined);
});

test("accepts state updates only from the latest profile request", () => {
  const guard = createAutomaticTaskProfileRequestGuard();
  const first = guard.begin();
  const second = guard.begin();

  assert.equal(guard.isCurrent(first), false);
  assert.equal(guard.isCurrent(second), true);
  guard.invalidate();
  assert.equal(guard.isCurrent(second), false);
});
