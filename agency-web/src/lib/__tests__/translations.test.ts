import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import ts from "typescript";
import { coreMessages } from "../messages";
import { agencyMessages } from "../../features/agencies/messages";
import { pricingMessages } from "../../features/pricing/messages";
import { financeMessages } from "../../features/finance/messages";
import { exportMessages } from "../../features/exports/messages";
import { customerMessages } from "../../features/customers/messages";
import { reconciliationMessages } from "../../features/reconciliation/messages";
import { reportMessages } from "../../features/reports/messages";
import { invitationMessages } from "../../features/invitations/messages";
import { evidenceLabels } from "../../features/reconciliation/labels";
import type { Locale } from "../types";

test("every shipped language includes the same complete feature dictionary", () => {
  for (const messages of [
    coreMessages,
    agencyMessages,
    pricingMessages,
    financeMessages,
    exportMessages,
    customerMessages,
    reconciliationMessages,
    reportMessages,
    invitationMessages,
  ]) {
    const keys = Object.keys(messages.en).sort();
    for (const lang of Object.keys(messages) as Locale[]) {
      expect(Object.keys(messages[lang]).sort()).toEqual(keys);
      for (const key of keys) expect(messages[lang][key].trim()).not.toBe("");
    }
  }
});

test("rendered UI source strings have translations in every supported language", async () => {
  const source = resolve(import.meta.dir, "../..");
  const keys = new Set<string>();
  for (const label of Object.values(evidenceLabels)) keys.add(label);
  function collect(node: ts.Node) {
    if (ts.isStringLiteral(node)) keys.add(node.text);
    else if (ts.isConditionalExpression(node)) {
      collect(node.whenTrue);
      collect(node.whenFalse);
    }
  }
  function visit(node: ts.Node) {
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "t" &&
      node.arguments[0]
    ) {
      collect(node.arguments[0]);
    }
    if (
      ts.isPropertyAssignment(node) &&
      ts.isIdentifier(node.name) &&
      node.name.text === "label" &&
      ts.isStringLiteral(node.initializer)
    ) {
      collect(node.initializer);
    }
    ts.forEachChild(node, visit);
  }
  for await (const file of new Bun.Glob("**/*.tsx").scan(source)) {
    visit(
      ts.createSourceFile(
        file,
        readFileSync(resolve(source, file), "utf8"),
        ts.ScriptTarget.Latest,
        true,
        ts.ScriptKind.TSX,
      ),
    );
  }
  const missing: string[] = [];
  for (const lang of Object.keys(coreMessages) as Locale[]) {
    const messages = {
      ...coreMessages[lang],
      ...agencyMessages[lang],
      ...pricingMessages[lang],
      ...financeMessages[lang],
      ...exportMessages[lang],
      ...customerMessages[lang],
      ...reconciliationMessages[lang],
      ...reportMessages[lang],
      ...invitationMessages[lang],
    };
    for (const key of keys) if (!messages[key]) missing.push(`${lang}: ${key}`);
  }
  expect(missing).toEqual([]);
});
