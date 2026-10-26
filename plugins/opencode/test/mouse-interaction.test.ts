import assert from "node:assert/strict";
import test from "node:test";
import { BoxRenderable, MouseButton } from "@opentui/core";
import { createTestRenderer } from "@opentui/core/testing";
import { createPressInteraction } from "../mouse.ts";

test("a sidebar reflow cannot transfer activation to another panel", async () => {
  const { renderer, mockMouse, renderOnce } = await createTestRenderer({
    width: 20,
    height: 8,
  });
  let firstActions = 0;
  let secondActions = 0;
  const firstPress = createPressInteraction(() => {
    firstActions += 1;
  });
  const secondPress = createPressInteraction(() => {
    secondActions += 1;
  });
  const column = new BoxRenderable(renderer, {
    width: 20,
    height: 8,
    flexDirection: "column",
  });
  const first = new BoxRenderable(renderer, {
    width: 20,
    height: 3,
    backgroundColor: "#ff0000",
    ...firstPress.handlers,
  });
  const second = new BoxRenderable(renderer, {
    width: 20,
    height: 3,
    backgroundColor: "#00ff00",
    ...secondPress.handlers,
  });
  column.add(first);
  column.add(second);
  renderer.root.add(column);

  try {
    await renderOnce();
    await mockMouse.pressDown(1, 2);
    first.height = 1;
    await renderOnce();
    await mockMouse.release(1, 2);
    assert.deepEqual([firstActions, secondActions], [0, 0]);

    await mockMouse.click(1, 2);
    assert.deepEqual([firstActions, secondActions], [0, 1]);

    await mockMouse.click(1, 2, MouseButton.RIGHT);
    assert.deepEqual([firstActions, secondActions], [0, 1]);
  } finally {
    firstPress.dispose();
    secondPress.dispose();
    renderer.destroy();
  }
});
