package cli

import (
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestStatsSummaryCacheIdentityPartition(t *testing.T) {
	pointer := func(value string) *string { return &value }
	row := func(provider, model *string) dbpkg.StatsSummaryCacheRow {
		return dbpkg.StatsSummaryCacheRow{Provider: provider, Model: model}
	}

	t.Run("one runtime-emitted identity authorizes an aggregate", func(t *testing.T) {
		partition := partitionStatsSummaryCacheIdentity([]dbpkg.StatsSummaryCacheRow{
			row(pointer("openai"), pointer("gpt-5")),
			row(pointer("openai"), pointer("gpt-5")),
		})
		identity, reason := partition.aggregateIdentity()
		if reason != "" || identity != (statsSummaryRuntimeIdentity{Provider: "openai", Model: "gpt-5"}) {
			t.Fatalf("aggregate identity = %#v, reason %q", identity, reason)
		}
		if len(partition.Groups) != 1 || len(partition.Groups[0].Rows) != 2 || partition.MissingModelSteps != 0 {
			t.Fatalf("partition = %#v", partition)
		}
	})

	t.Run("mixed runtime-emitted identities stay in separate groups", func(t *testing.T) {
		partition := partitionStatsSummaryCacheIdentity([]dbpkg.StatsSummaryCacheRow{
			row(pointer("provider-b"), pointer("model-b")),
			row(pointer("provider-a"), pointer("model-a")),
		})
		identity, reason := partition.aggregateIdentity()
		if reason != statsSummaryRuntimeIdentityMixed || identity != (statsSummaryRuntimeIdentity{}) {
			t.Fatalf("aggregate identity = %#v, reason %q", identity, reason)
		}
		if len(partition.Groups) != 2 || partition.Groups[0].Identity != (statsSummaryRuntimeIdentity{Provider: "provider-a", Model: "model-a"}) || partition.Groups[1].Identity != (statsSummaryRuntimeIdentity{Provider: "provider-b", Model: "model-b"}) {
			t.Fatalf("identity groups = %#v", partition.Groups)
		}
	})

	t.Run("missing source identity has no configured fallback", func(t *testing.T) {
		partition := partitionStatsSummaryCacheIdentity([]dbpkg.StatsSummaryCacheRow{
			row(pointer("source-provider"), pointer("source-model")),
			row(pointer("source-provider"), nil),
			row(nil, pointer("source-model")),
			row(pointer(" "), pointer("source-model")),
		})
		identity, reason := partition.aggregateIdentity()
		if reason != statsSummaryRuntimeIdentityMissing || identity != (statsSummaryRuntimeIdentity{}) {
			t.Fatalf("aggregate identity = %#v, reason %q", identity, reason)
		}
		if len(partition.Groups) != 1 || partition.Groups[0].Identity != (statsSummaryRuntimeIdentity{Provider: "source-provider", Model: "source-model"}) || partition.MissingModelSteps != 3 {
			t.Fatalf("partition = %#v", partition)
		}
	})
}
