// Types of the ktx mod: the summary it reads from
// `ktx stats summary --format json` and the state it keeps for the session.

export type KtxStatsWindow = 15 | 30 | 60;

/** One validated summary. The figures cover every runtime ktx records. */
export type KtxSummary = {
  windowSeconds: number;
  from: string;
  to: string;
  totalCalls: number;
  compressedCalls: number;
  rawTokens: number;
  deliveredTokens: number;
};

/** The last refresh for a window: its last good data and the latest error. */
export type KtxStatsSnapshot = {
  window: KtxStatsWindow;
  data: KtxSummary | null;
  error: string | null;
};

declare module "claude-code" {
  interface PluginState {
    ktx: {
      window: KtxStatsWindow;
      snapshot: KtxStatsSnapshot | null;
      paneOpen: boolean;
      outdated: string | null;
    };
  }
}
