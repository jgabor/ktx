package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

const (
	screenMaxBytes = 64 << 20
	screenSourceID = "screeningcallxxx"
)

type stringList []string

func (v *stringList) String() string { return strings.Join(*v, ",") }
func (v *stringList) Set(s string) error {
	if s == "" {
		return fmt.Errorf("value must not be empty")
	}
	*v = append(*v, s)
	return nil
}

type screenPopulation struct {
	Schema string       `json:"schema"`
	Cases  []screenCase `json:"cases"`
}
type screenCase struct {
	Tool      string `json:"tool"`
	Outcome   string `json:"outcome"`
	Generator struct {
		Algorithm    string `json:"algorithm"`
		Bytes        int    `json:"bytes"`
		FillByte     string `json:"fill_byte"`
		NewlineEvery int    `json:"newline_every"`
		Placements   []struct {
			Fact    string `json:"fact"`
			Offsets []int  `json:"offsets"`
		} `json:"placements"`
		JSON *struct {
			RequiredFact  string `json:"required_fact"`
			RepeatedFact  string `json:"repeated_fact"`
			RepeatedCount int    `json:"repeated_count"`
			PaddingByte   string `json:"padding_byte"`
		} `json:"json"`
	} `json:"generator"`
	Expected struct {
		Eligible      bool `json:"eligible"`
		Passthrough   bool `json:"passthrough"`
		RequiredFacts []struct {
			Fact             string `json:"fact"`
			ExactOccurrences int    `json:"exact_occurrences"`
		} `json:"required_facts"`
		Errors struct {
			IsError          *bool   `json:"is_error"`
			RequiredText     *string `json:"required_text"`
			ExactOccurrences int     `json:"exact_occurrences"`
		} `json:"errors"`
	} `json:"expected"`
}

type screenResult struct {
	ConfigID                 string         `json:"config_id"`
	Calls                    int            `json:"calls"`
	RawBytes                 int            `json:"raw_bytes"`
	DeliveredBytes           int            `json:"delivered_bytes"`
	NetBytesSaved            int            `json:"net_bytes_saved"`
	RawEstimatedTokens       int            `json:"raw_estimated_tokens"`
	DeliveredEstimatedTokens int            `json:"delivered_estimated_tokens"`
	Dispositions             map[string]int `json:"dispositions"`
	RecoveryCueCalls         int            `json:"recovery_cue_calls"`
	RecoveryFooterBytes      int            `json:"recovery_footer_bytes"`
	ControlledFactsPass      bool           `json:"controlled_facts_pass"`
}

func runBenchScreen(args []string, opts Options, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ktx bench screen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var populationPath, anchorPath, format string
	var configPaths stringList
	fs.StringVar(&populationPath, "population", "", "frozen source population JSON")
	fs.Var(&configPaths, "config", "complete ktx configuration (repeatable)")
	fs.StringVar(&anchorPath, "anchor-config", "", "configuration that advances first when qualified")
	fs.StringVar(&format, "format", opts.Format, "json or text")
	if err := fs.Parse(args); err != nil || populationPath == "" || anchorPath == "" || len(configPaths) == 0 || len(fs.Args()) != 0 || format != FormatJSON {
		if err == nil {
			err = fmt.Errorf("--population, --anchor-config, one or more --config, and --format json are required")
		}
		printError(stderr, usageError("ktx bench screen: %v", err), opts.Format)
		return ExitUsage
	}
	paths := append([]string{}, configPaths...)
	anchorFound := false
	for _, p := range paths {
		if p == anchorPath {
			anchorFound = true
		}
	}
	if !anchorFound {
		paths = append(paths, anchorPath)
	}

	b, err := os.ReadFile(populationPath)
	if err != nil || len(b) > screenMaxBytes {
		if err == nil {
			err = fmt.Errorf("population exceeds %d bytes", screenMaxBytes)
		}
		printError(stderr, usageError("ktx bench screen: read population: %v", err), opts.Format)
		return ExitUsage
	}
	var population screenPopulation
	if err := json.Unmarshal(b, &population); err != nil || population.Schema == "" || len(population.Cases) == 0 {
		if err == nil {
			err = fmt.Errorf("population must contain schema and cases")
		}
		printError(stderr, usageError("ktx bench screen: invalid population: %v", err), opts.Format)
		return ExitUsage
	}
	outputs := make([][]byte, len(population.Cases))
	for i := range population.Cases {
		outputs[i], err = generateScreenOutput(population.Cases[i])
		if err != nil {
			printError(stderr, usageError("ktx bench screen: case %d: %v", i, err), opts.Format)
			return ExitUsage
		}
	}

	results := make([]screenResult, 0, len(paths))
	seen := map[string]bool{}
	anchorID := ""
	for _, path := range paths {
		cfgBytes, readErr := os.ReadFile(path)
		cfg, loadErr := config.LoadFile(path)
		if readErr != nil || loadErr != nil {
			if readErr != nil {
				loadErr = readErr
			}
			printError(stderr, usageError("ktx bench screen: load configuration: %v", loadErr), opts.Format)
			return ExitUsage
		}
		sum := sha256.Sum256(cfgBytes)
		id := hex.EncodeToString(sum[:8])
		if path == anchorPath {
			anchorID = id
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if len(cfg.Plugins) != 0 {
			printError(stderr, usageError("ktx bench screen: external plugins are not allowed in offline screening"), opts.Format)
			return ExitUsage
		}
		result := evaluateScreenConfiguration(id, population.Cases, outputs, cfg)
		results = append(results, result)
	}
	shortlist := screenShortlist(results, anchorID)
	popHash := sha256.Sum256(b)
	envelope := map[string]any{
		"schema":              "ktx.bench.configuration-screen.v1",
		"population_sha256":   hex.EncodeToString(popHash[:]),
		"calls":               len(population.Cases),
		"configurations":      results,
		"advanced_config_ids": shortlist,
	}
	if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
		return ExitInternal
	}
	return ExitOK
}

func generateScreenOutput(c screenCase) ([]byte, error) {
	g := c.Generator
	switch g.Algorithm {
	case "fixed_lines_v1":
		if g.Bytes < 0 || len(g.FillByte) != 1 || g.NewlineEvery <= 0 {
			return nil, fmt.Errorf("invalid fixed-lines generator")
		}
		out := bytes.Repeat([]byte(g.FillByte), g.Bytes)
		for i := g.NewlineEvery - 1; i < len(out); i += g.NewlineEvery {
			out[i] = '\n'
		}
		for _, p := range g.Placements {
			for _, offset := range p.Offsets {
				if offset < 0 || offset+len(p.Fact) > len(out) {
					return nil, fmt.Errorf("placement out of bounds")
				}
				copy(out[offset:], p.Fact)
			}
		}
		return out, nil
	case "json_object_v1":
		p := g.JSON
		if p == nil || len(p.PaddingByte) != 1 || p.RepeatedCount < 1 {
			return nil, fmt.Errorf("invalid JSON generator")
		}
		v := struct {
			Required string   `json:"required"`
			Items    []string `json:"items"`
			Padding  string   `json:"padding"`
		}{Required: p.RequiredFact, Items: make([]string, p.RepeatedCount)}
		for i := range v.Items {
			v.Items[i] = p.RepeatedFact
		}
		base, _ := json.Marshal(v)
		if g.Bytes < len(base) {
			return nil, fmt.Errorf("JSON exceeds target length")
		}
		v.Padding = strings.Repeat(p.PaddingByte, g.Bytes-len(base))
		return json.Marshal(v)
	default:
		return nil, fmt.Errorf("unsupported generator")
	}
}

func evaluateScreenConfiguration(id string, cases []screenCase, outputs [][]byte, cfg *config.Config) screenResult {
	r := screenResult{ConfigID: id, Calls: len(cases), Dispositions: map[string]int{"transformed": 0, "anti_worsening_rejected": 0, "intentional_passthrough": 0}, ControlledFactsPass: true}
	for i, c := range cases {
		isError := c.Expected.Errors.IsError
		tc := &dbpkg.ToolCall{Tool: c.Tool, Output: string(outputs[i]), IsError: isError}
		execution, _ := compressPipelineWithConfig(tc, cfg, nil, io.Discard, false)
		execution = applyRecoveryCueGate(execution, screenSourceID, tc.Output)
		delivered := execution.output
		footerBytes := 0
		if execution.transformed && execution.recoverFooter && !execution.structuredFallback {
			if withCue, ok := recoveryCueOutput(screenSourceID, tc.Output, delivered); ok {
				footerBytes = len(withCue) - len(delivered)
				delivered = withCue
				r.RecoveryCueCalls++
			}
		}
		switch {
		case execution.transformed:
			r.Dispositions["transformed"]++
		case execution.antiWorseningRejected:
			r.Dispositions["anti_worsening_rejected"]++
		default:
			r.Dispositions["intentional_passthrough"]++
		}
		r.RawBytes += len(outputs[i])
		r.DeliveredBytes += len(delivered)
		r.RecoveryFooterBytes += footerBytes
		r.RawEstimatedTokens += estimatedTokens(len(outputs[i]))
		r.DeliveredEstimatedTokens += estimatedTokens(len(delivered))
		content := []byte(delivered)
		for _, fact := range c.Expected.RequiredFacts {
			if bytes.Count(content, []byte(fact.Fact)) != fact.ExactOccurrences {
				r.ControlledFactsPass = false
			}
		}
		if c.Expected.Errors.RequiredText != nil && bytes.Count(content, []byte(*c.Expected.Errors.RequiredText)) != c.Expected.Errors.ExactOccurrences {
			r.ControlledFactsPass = false
		}
		if c.Expected.Passthrough && !bytes.Equal(content, outputs[i]) {
			r.ControlledFactsPass = false
		}
	}
	r.NetBytesSaved = r.RawBytes - r.DeliveredBytes
	return r
}

func screenShortlist(results []screenResult, anchorID string) []string {
	qualified := func(r screenResult) bool { return r.NetBytesSaved > 0 && r.ControlledFactsPass }
	shortlist := []string{}
	for _, r := range results {
		if r.ConfigID == anchorID && qualified(r) {
			shortlist = append(shortlist, r.ConfigID)
		}
	}
	alternatives := append([]screenResult{}, results...)
	sort.SliceStable(alternatives, func(i, j int) bool { return alternatives[i].NetBytesSaved > alternatives[j].NetBytesSaved })
	for _, r := range alternatives {
		if len(shortlist) >= 3 {
			break
		}
		if r.ConfigID != anchorID && qualified(r) {
			shortlist = append(shortlist, r.ConfigID)
		}
	}
	return shortlist
}
