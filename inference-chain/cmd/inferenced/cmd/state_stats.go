package cmd

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/spf13/cobra"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const (
	flagStateStatsHeight     = "height"
	flagStateStatsTop        = "top"
	flagStateStatsLegacyOnly = "legacy-only"
)

// prefixStat accumulates on-disk size for one logical group of keys.
type prefixStat struct {
	name      string
	legacy    bool
	count     int64
	keyBytes  int64
	valBytes  int64
	unmatched bool
}

func (s prefixStat) total() int64 { return s.keyBytes + s.valBytes }

// StateStatsCommand reports how much committed state each module store — and,
// within the inference module, each prefix — consumes on disk. It is an
// offline analysis tool: it opens the node's application database directly,
// loads the latest (or a given) height, and iterates the committed KV stores.
//
// Use it to answer "why is the state so big, and what can we safely remove":
// the inference breakdown labels every prefix (see types.StatePrefixCatalog)
// and flags the ones marked legacy, which are the cleanup candidates handled by
// the v0.2.14 upgrade.
func StateStatsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state-stats",
		Short: "Report per-store and per-prefix committed state size (offline analysis)",
		Long: `Report per-store and per-prefix committed state size.

Opens the node's application database read-only, loads the latest committed
height (or --height), and iterates every module KV store reporting key count
and byte size. For the inference module it additionally attributes each key to
a named prefix and flags legacy prefixes that are cleanup candidates.

The node must not be running (the database is opened exclusively). Run it
against a stopped node's home directory or a restored snapshot.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			serverCtx := server.GetServerContextFromCmd(cmd)

			height, err := cmd.Flags().GetInt64(flagStateStatsHeight)
			if err != nil {
				return err
			}
			top, err := cmd.Flags().GetInt(flagStateStatsTop)
			if err != nil {
				return err
			}
			legacyOnly, err := cmd.Flags().GetBool(flagStateStatsLegacyOnly)
			if err != nil {
				return err
			}

			dataDir := filepath.Join(serverCtx.Config.RootDir, "data")
			db, err := dbm.NewDB("application", server.GetAppDBBackend(serverCtx.Viper), dataDir)
			if err != nil {
				return fmt.Errorf("open application db at %s: %w", dataDir, err)
			}
			defer func() { _ = db.Close() }()

			a, err := app.New(
				log.NewNopLogger(),
				db,
				io.Discard,
				height <= 0, // loadLatest when no explicit height requested
				serverCtx.Viper,
				[]wasmkeeper.Option{},
			)
			if err != nil {
				return fmt.Errorf("instantiate app: %w", err)
			}
			if height > 0 {
				if err := a.LoadHeight(height); err != nil {
					return fmt.Errorf("load height %d: %w", height, err)
				}
			}

			cms := a.CommitMultiStore()
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)

			storeStats, inferenceStats := collectStateStats(cms, a.GetStoreKeys())

			printStoreSummary(out, storeStats)
			fmt.Fprintln(out)
			printInferenceBreakdown(out, inferenceStats, top, legacyOnly)
			return out.Flush()
		},
	}

	cmd.Flags().Int64(flagStateStatsHeight, 0, "committed height to inspect (default: latest)")
	cmd.Flags().Int(flagStateStatsTop, 0, "limit the inference breakdown to the N largest prefixes (default: all)")
	cmd.Flags().Bool(flagStateStatsLegacyOnly, false, "only show legacy (cleanup-candidate) prefixes in the inference breakdown")
	return cmd
}

// collectStateStats iterates every committed KV store, returning the per-store
// totals and, for the inference module, the per-prefix breakdown.
func collectStateStats(cms storetypes.MultiStore, keys []storetypes.StoreKey) ([]prefixStat, []prefixStat) {
	catalog := inferencetypes.StatePrefixCatalog()
	inferenceBuckets := map[string]*prefixStat{}

	storeStats := make([]prefixStat, 0, len(keys))
	for _, key := range keys {
		if _, ok := key.(*storetypes.KVStoreKey); !ok {
			continue // skip transient and memory stores
		}

		store := cms.GetKVStore(key)
		isInference := key.Name() == inferencetypes.StoreKey

		st := prefixStat{name: key.Name()}
		it := store.Iterator(nil, nil)
		for ; it.Valid(); it.Next() {
			k := it.Key()
			vLen := int64(len(it.Value()))
			kLen := int64(len(k))
			st.count++
			st.keyBytes += kLen
			st.valBytes += vLen

			if isInference {
				addInferenceKey(inferenceBuckets, catalog, k, kLen, vLen)
			}
		}
		_ = it.Close()
		storeStats = append(storeStats, st)
	}

	sort.SliceStable(storeStats, func(i, j int) bool {
		return storeStats[i].total() > storeStats[j].total()
	})

	inferenceStats := make([]prefixStat, 0, len(inferenceBuckets))
	for _, v := range inferenceBuckets {
		inferenceStats = append(inferenceStats, *v)
	}
	sort.SliceStable(inferenceStats, func(i, j int) bool {
		return inferenceStats[i].total() > inferenceStats[j].total()
	})

	return storeStats, inferenceStats
}

func addInferenceKey(buckets map[string]*prefixStat, catalog []inferencetypes.StatePrefix, key []byte, kLen, vLen int64) {
	match := inferencetypes.MatchStatePrefix(catalog, key)

	var name string
	var legacy, unmatched bool
	if match != nil {
		name, legacy = match.Name, match.Legacy
	} else {
		// Group anything the catalog does not recognise by its leading byte so
		// gaps are visible rather than silently merged.
		name = fmt.Sprintf("<unmatched:0x%02x>", key[0])
		unmatched = true
	}

	b, ok := buckets[name]
	if !ok {
		b = &prefixStat{name: name, legacy: legacy, unmatched: unmatched}
		buckets[name] = b
	}
	b.count++
	b.keyBytes += kLen
	b.valBytes += vLen
}

func printStoreSummary(out io.Writer, stats []prefixStat) {
	fmt.Fprintln(out, "STORE\tKEYS\tKEY_BYTES\tVALUE_BYTES\tTOTAL")
	var totalKeys, totalKeyBytes, totalValBytes int64
	for _, s := range stats {
		totalKeys += s.count
		totalKeyBytes += s.keyBytes
		totalValBytes += s.valBytes
		fmt.Fprintf(out, "%s\t%d\t%d\t%d\t%s\n", s.name, s.count, s.keyBytes, s.valBytes, humanizeBytes(s.total()))
	}
	fmt.Fprintf(out, "TOTAL\t%d\t%d\t%d\t%s\n", totalKeys, totalKeyBytes, totalValBytes, humanizeBytes(totalKeyBytes+totalValBytes))
}

func printInferenceBreakdown(out io.Writer, stats []prefixStat, top int, legacyOnly bool) {
	fmt.Fprintln(out, "INFERENCE PREFIX BREAKDOWN")
	fmt.Fprintln(out, "PREFIX\tLEGACY\tKEYS\tKEY_BYTES\tVALUE_BYTES\tTOTAL")

	shown := 0
	for _, s := range stats {
		if legacyOnly && !s.legacy {
			continue
		}
		if top > 0 && shown >= top {
			break
		}
		legacy := "-"
		if s.legacy {
			legacy = "yes"
		}
		if s.unmatched {
			legacy = "?"
		}
		fmt.Fprintf(out, "%s\t%s\t%d\t%d\t%d\t%s\n", s.name, legacy, s.count, s.keyBytes, s.valBytes, humanizeBytes(s.total()))
		shown++
	}
}

func humanizeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
