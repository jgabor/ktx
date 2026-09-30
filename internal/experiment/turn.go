package experiment

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// RuntimeTurnSession identifies a completed JSON turn without consulting a
// mutable latest-session pointer. It does not score the answer or tool behavior.
func RuntimeTurnSession(reader io.Reader) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(nil, 64<<20)
	session := ""
	stopped, answered := false, false
	for scanner.Scan() {
		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Part      struct {
				SessionID string `json:"sessionID"`
				Reason    string `json:"reason"`
				Text      string `json:"text"`
			} `json:"part"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return "", err
		}
		if !strings.HasPrefix(event.SessionID, "ses_") || (session != "" && event.SessionID != session) || (event.Part.SessionID != "" && event.Part.SessionID != event.SessionID) || event.Type == "error" {
			return "", errors.New("turn has missing, conflicting or failed runtime session evidence")
		}
		session = event.SessionID
		if event.Type == "step_finish" {
			stopped = event.Part.Reason == "stop"
		}
		answered = answered || (event.Type == "text" && strings.TrimSpace(event.Part.Text) != "")
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if session == "" || !stopped || !answered {
		return "", errors.New("turn is missing a completed response")
	}
	return session, nil
}
