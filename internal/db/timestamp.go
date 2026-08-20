package db

import "time"

func sortableTimestamp(text string) any {
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return nil
	}
	return parsed.UnixNano()
}
