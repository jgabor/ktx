package experiment

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

func ParseStream(path string, expectedOutput *string, expectedFacts []string) Metrics {
	metrics := Metrics{Score: Score{FactsMatched: []string{}, FactsMissed: []string{}}}
	file, err := os.Open(path)
	if err != nil {
		return metrics
	}
	defer file.Close()
	var tokensIn, tokensOut, cost float64
	var cacheRead, cacheWrite float64
	var sawTokensIn, sawTokensOut, sawCost, sawCacheRead, sawCacheWrite bool
	lastText := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			Part    struct {
				Reason  string   `json:"reason"`
				Text    string   `json:"text"`
				Content string   `json:"content"`
				Cost    *float64 `json:"cost"`
				Tokens  struct {
					Input  *float64 `json:"input"`
					Output *float64 `json:"output"`
					Cache  struct {
						Read  *float64 `json:"read"`
						Write *float64 `json:"write"`
					} `json:"cache"`
				} `json:"tokens"`
			} `json:"part"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event.Type {
		case "tool_use":
			metrics.ToolCallCount++
		case "step_finish":
			metrics.UsageEvidence.StepFinishEvents++
			if event.Part.Tokens.Input != nil {
				tokensIn += *event.Part.Tokens.Input
				sawTokensIn = true
				metrics.UsageEvidence.TokensInEvents++
			}
			if event.Part.Tokens.Output != nil {
				tokensOut += *event.Part.Tokens.Output
				sawTokensOut = true
				metrics.UsageEvidence.TokensOutEvents++
			}
			if event.Part.Cost != nil {
				cost += *event.Part.Cost
				sawCost = true
				metrics.UsageEvidence.CostEvents++
			}
			// Cache telemetry on step_finish is per-request
			// (tokens.total == tokens.input + tokens.output +
			// tokens.reasoning + tokens.cache.read +
			// tokens.cache.write for every event). SUM across
			// events matches the input/output/cost aggregation.
			// A nil source field stays nil; an explicitly emitted
			// zero is preserved as a non-nil pointer to 0.
			if event.Part.Tokens.Cache.Read != nil {
				cacheRead += *event.Part.Tokens.Cache.Read
				sawCacheRead = true
				metrics.UsageEvidence.CacheReadEvents++
			}
			if event.Part.Tokens.Cache.Write != nil {
				cacheWrite += *event.Part.Tokens.Cache.Write
				sawCacheWrite = true
				metrics.UsageEvidence.CacheWriteEvents++
			}
			if event.Part.Reason == "stop" {
				metrics.StreamComplete = true
			}
		case "text":
			switch {
			case event.Part.Text != "":
				lastText = event.Part.Text
			case event.Part.Content != "":
				lastText = event.Part.Content
			default:
				var content string
				if json.Unmarshal(event.Content, &content) == nil {
					lastText = content
				}
			}
		}
	}
	metrics.Score = ScoreAnswer(lastText, expectedOutput, expectedFacts)
	answer := strings.TrimSpace(lastText)
	if len(answer) > 500 {
		answer = answer[:500]
	}
	metrics.AnswerExcerpt = answer
	steps := metrics.UsageEvidence.StepFinishEvents
	if sawTokensIn && metrics.UsageEvidence.TokensInEvents == steps {
		metrics.TokensIn = &tokensIn
	}
	if sawTokensOut && metrics.UsageEvidence.TokensOutEvents == steps {
		metrics.TokensOut = &tokensOut
	}
	if sawCost && metrics.UsageEvidence.CostEvents == steps {
		metrics.Cost = &cost
	}
	if sawCacheRead && metrics.UsageEvidence.CacheReadEvents == steps {
		metrics.CacheRead = &cacheRead
	}
	if sawCacheWrite && metrics.UsageEvidence.CacheWriteEvents == steps {
		metrics.CacheWrite = &cacheWrite
	}
	return metrics
}

func AttemptStatus(exitCode int, newSessionIDs []string, metrics Metrics) string {
	if exitCode == 124 || exitCode == 137 {
		return "timeout"
	}
	if exitCode != 0 {
		return "failed"
	}
	if len(newSessionIDs) != 1 {
		return "unlinked"
	}
	if !metrics.StreamComplete {
		return "invalid"
	}
	return "completed"
}
