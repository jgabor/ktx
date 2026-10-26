/** @jsxImportSource @opentui/solid */

import { TextAttributes } from "@opentui/core";
import { createMemo, createSignal, onCleanup } from "solid-js";
import type { TuiPlugin, TuiPluginModule } from "@opencode-ai/plugin/tui";
import { createPressInteraction } from "./mouse.ts";
import {
  createKtxStatsStore,
  deliveredTokensPerSecond,
  formatDeliveredTokensPerSecond,
  formatSidebarMetrics,
  formatPromptCacheTokenHitRateMetrics,
  nextKtxStatsWindow,
  throughputTone,
  type KtxSummary,
  type KtxStatsState,
  type PromptCacheTokenHitRateMetrics,
  type ThroughputTone,
} from "./ktx-stats.ts";

type TuiApi = Parameters<NonNullable<TuiPluginModule["tui"]>>[0];
type Theme = Pick<TuiApi["theme"]["current"],
  "text" | "textMuted" | "primary" | "warning" | "error" |
  "selectedListItemText" | "backgroundElement" | "borderSubtle">;

function createRuntime() {
  const store = createKtxStatsStore();
  const [state, setState] = createSignal<KtxStatsState>(store.getState());
  const unsubscribe = store.subscribe(setState);
  const toggleWindow = () =>
    void store.setWindow(nextKtxStatsWindow(state().window));

  let disposed = false;
  const dispose = () => {
    if (disposed) return;
    disposed = true;
    unsubscribe();
    store.dispose();
  };
  return {
    state,
    toggleWindow,
    dispose,
    start() {
      void store.start();
    },
  };
}

const tui: TuiPlugin = async (api) => {
  const runtime = createRuntime();
  try {
    api.lifecycle?.onDispose?.(runtime.dispose);
    // OpenCode owns slot removal; register returns a registration ID, not cleanup.
    api.slots?.register({
      order: 600,
      slots: {
        sidebar_content: () => (
          <Sidebar
            theme={api.theme.current}
            clearSelection={() => api.renderer.clearSelection()}
            state={runtime.state}
            onToggleWindow={runtime.toggleWindow}
          />
        ),
        session_prompt_right: () => (
          <SessionPromptThroughput
            theme={api.theme.current}
            summary={runtime.state().data}
          />
        ),
      },
    });
    runtime.start();
  } catch (error) {
    runtime.dispose();
    throw error;
  }
};

export function SessionPromptThroughput(props: {
  theme: Theme;
  summary: KtxSummary | null;
}) {
  return (
    <text
      fg={throughputColor(
        props.theme,
        throughputTone(deliveredTokensPerSecond(props.summary)),
      )}
      wrapMode="none"
    >
      {formatDeliveredTokensPerSecond(props.summary)}
    </text>
  );
}

function throughputColor(theme: Theme, tone: ThroughputTone) {
  if (tone === "text") return theme.text;
  if (tone === "primary") return theme.primary;
  if (tone === "warning") return theme.warning;
  if (tone === "error") return theme.error;
  return theme.textMuted;
}

function Sidebar(props: {
  theme: Theme;
  clearSelection: () => void;
  state: () => KtxStatsState;
  onToggleWindow: () => void;
}) {
  const theme = props.theme;
  const current = props.state;
  const toggleWindow = createPressInteraction(
    props.onToggleWindow,
    props.clearSelection,
  );
  onCleanup(() => {
    toggleWindow.dispose();
  });
  const error = createMemo(() => {
    const state = current();
    if (state.outdated || !state.error) return null;
    const prefix = state.data ? "Stale: " : "Error: ";
    return (
      <text fg={theme.error} flexShrink={1}>
        {prefix}
        {shortError(state.error, 20 - prefix.length)}
      </text>
    );
  });
  const content = createMemo(() => {
    const state = current();
    if (state.outdated)
      return <text fg={theme.warning}>{state.outdated}</text>;
    const summary = state.data;
    if (!summary)
      return state.loading ? (
        <text fg={theme.textMuted}>Loading ktx stats…</text>
      ) : null;

    const metrics = formatSidebarMetrics(summary);
    const promptCacheHitRate = formatPromptCacheTokenHitRateMetrics(
      summary.promptCacheTokenHitRatePct,
    );

    return (
      <box flexDirection="column" gap={0}>
        {metrics ? (
          <box flexDirection="column" gap={0}>
            <MetricRow
              theme={theme}
              label="Compressed"
              detail={metrics.savedTokens}
              primary={metrics.savedPercentage}
              labelAttributes={TextAttributes.BOLD}
              primaryAttributes={TextAttributes.BOLD}
            />
            <PercentageBar
              theme={theme}
              percentage={metrics.compressionBar.savedPercentage}
            />
          </box>
        ) : (
          <text fg={theme.textMuted}>No calls in {state.window}m</text>
        )}
        {promptCacheHitRate ? (
          <PromptCacheTokenHitRateSidebar
            theme={theme}
            metrics={promptCacheHitRate}
          />
        ) : null}
        {metrics ? (
          <box flexDirection="column" gap={0}>
            <MetricRow
              theme={theme}
              label="Tokens"
              detail={metrics.rawTokens}
              primary={metrics.deliveredTokens}
              detailAttributes={TextAttributes.DIM}
            />
            <MetricRow
              theme={theme}
              label="Tool calls"
              detail={metrics.totalCalls}
              primary={metrics.compressedCalls}
              detailAttributes={TextAttributes.DIM}
            />
          </box>
        ) : null}
      </box>
    );
  });

  return (
    <box
      width="100%"
      flexDirection="column"
      paddingTop={1}
      gap={1}
    >
      <Divider theme={theme} />
      <box flexDirection="row" justifyContent="space-between">
        <box
          backgroundColor={theme.primary}
          paddingLeft={1}
          paddingRight={1}
        >
          <text
            fg={theme.selectedListItemText}
            attributes={TextAttributes.BOLD}
          >
            ktx
          </text>
        </box>
        {current().outdated ? null : (
          <box
            paddingLeft={1}
            {...toggleWindow.handlers}
          >
            <text fg={theme.textMuted}>last {current().window}m</text>
          </box>
        )}
      </box>

      <box flexDirection="column" gap={1}>
        {error()}
        {content()}
      </box>
    </box>
  );
}

export function PromptCacheTokenHitRateSidebar(props: {
  theme: Theme;
  metrics: PromptCacheTokenHitRateMetrics;
}) {
  return (
    <MetricRow
      theme={props.theme}
      label="Prompt cache"
      primary={props.metrics.display}
    />
  );
}

function MetricRow(props: {
  theme: Theme;
  label: string;
  detail?: string;
  primary: string;
  labelAttributes?: number;
  detailAttributes?: number;
  primaryAttributes?: number;
}) {
  return (
    <box flexDirection="row" justifyContent="space-between">
      <text
        fg={props.theme.text}
        attributes={props.labelAttributes ?? TextAttributes.NONE}
        flexShrink={1}
        wrapMode="none"
        truncate
      >
        {props.label}
      </text>
      <box flexDirection="row" gap={1} flexShrink={0}>
        {props.detail !== undefined ? (
          <text
            fg={props.theme.textMuted}
            attributes={props.detailAttributes ?? TextAttributes.NONE}
          >
            {props.detail}
          </text>
        ) : null}
        <text
          fg={props.theme.text}
          attributes={props.primaryAttributes ?? TextAttributes.NONE}
        >
          {props.primary}
        </text>
      </box>
    </box>
  );
}

function PercentageBar(props: {
  theme: Theme;
  percentage: number;
  color?: Theme["primary"];
}) {
  return (
    <box
      width="100%"
      height={1}
      flexDirection="row"
      backgroundColor={props.theme.backgroundElement}
    >
      {props.percentage > 0 ? (
        <box
          width={percentage(props.percentage)}
          height={1}
          backgroundColor={props.color ?? props.theme.primary}
        />
      ) : null}
    </box>
  );
}

function percentage(value: number): `${number}%` {
  return `${Math.max(0, Math.min(100, value))}%` as `${number}%`;
}

function Divider(props: { theme: Theme }) {
  return (
    <box
      border={["bottom"]}
      borderColor={props.theme.borderSubtle}
      height={1}
    />
  );
}

function shortError(error: string, maxLength: number): string {
  return error.length > maxLength
    ? `${error.slice(0, maxLength - 1)}…`
    : error;
}

const plugin: TuiPluginModule = { id: "ktx", tui };
export default plugin;
