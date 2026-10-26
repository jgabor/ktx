import { MouseButton, type MouseEvent } from "@opentui/core";

const PRESS_STATE = Symbol.for("@jgabor/opencode-plugin/press-state-v1");
const PRESS_TIMEOUT_MS = 2_000;

type PressState = {
  owner: object | null;
  timeout: ReturnType<typeof setTimeout> | null;
};

const globals = globalThis as unknown as Record<PropertyKey, unknown>;
const state = (() => {
  const current = globals[PRESS_STATE] as PressState | undefined;
  if (current) return current;
  const created: PressState = { owner: null, timeout: null };
  globals[PRESS_STATE] = created;
  return created;
})();

function clearOwner(owner?: object): void {
  if (owner && state.owner !== owner) return;
  if (state.timeout) clearTimeout(state.timeout);
  state.owner = null;
  state.timeout = null;
}

/** Creates release-driven mouse handlers coordinated across sidebar plugins. */
export function createPressInteraction(
  action: () => unknown,
  clearSelection?: () => void,
) {
  const owner = {};
  const cancel = () => clearOwner(owner);
  const consume = (event: MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
  };

  return {
    handlers: {
      onMouseDown(event: MouseEvent) {
        if (event.button !== MouseButton.LEFT) return;
        clearOwner();
        state.owner = owner;
        state.timeout = setTimeout(cancel, PRESS_TIMEOUT_MS);
        clearSelection?.();
        consume(event);
      },
      onMouseUp(event: MouseEvent) {
        if (event.button !== MouseButton.LEFT) return;
        const activate = state.owner === owner && !event.isDragging;
        clearOwner();
        consume(event);
        if (activate) void action();
      },
      onMouseOut: cancel,
      onMouseDrag(event: MouseEvent) {
        if (event.button !== MouseButton.LEFT) return;
        cancel();
        consume(event);
      },
      onMouseDragEnd(event: MouseEvent) {
        if (event.button !== MouseButton.LEFT) return;
        cancel();
        consume(event);
      },
    },
    dispose: cancel,
  };
}
