import { readFile, writeFile } from "node:fs/promises";
import { transformAsync } from "@babel/core";
import typescript from "@babel/preset-typescript";
import solid from "babel-preset-solid";

// Installed node_modules files do not receive the host's Solid JSX transform.
// Compile JSX only; leave all runtime imports external for the host to share.
const source = new URL("../tui.tsx", import.meta.url);
const result = await transformAsync(await readFile(source, "utf8"), {
  filename: source.pathname,
  configFile: false,
  babelrc: false,
  presets: [[solid, { moduleName: "@opentui/solid", generate: "universal" }], typescript],
});
if (!result?.code) throw new Error("Solid JSX compilation produced no output");
await writeFile(new URL("../tui.js", import.meta.url), result.code + "\n");
