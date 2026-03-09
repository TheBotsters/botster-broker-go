package interchange

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type rawType struct {
	Type string `json:"_type"`
}

func ParseJSONL(r io.Reader) (Document, error) {
	var doc Document
	s := bufio.NewScanner(r)
	lineNo := 0
	headerSeen := false

	for s.Scan() {
		lineNo++
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}

		var rt rawType
		if err := json.Unmarshal([]byte(line), &rt); err != nil {
			return doc, fmt.Errorf("line %d: invalid json: %w", lineNo, err)
		}
		if rt.Type == "" {
			return doc, fmt.Errorf("line %d: missing _type", lineNo)
		}

		if !headerSeen {
			if rt.Type != TypeHeader {
				return doc, fmt.Errorf("line %d: first non-empty line must be _type=header", lineNo)
			}
			var h Header
			if err := json.Unmarshal([]byte(line), &h); err != nil {
				return doc, fmt.Errorf("line %d: invalid header: %w", lineNo, err)
			}
			if h.Version == "" {
				return doc, fmt.Errorf("line %d: header missing version", lineNo)
			}
			if h.Version != CurrentVersion {
				return doc, fmt.Errorf("line %d: unsupported version %q", lineNo, h.Version)
			}
			doc.Header = h
			headerSeen = true
			continue
		}

		switch rt.Type {
		case TypeHeader:
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("line %d: duplicate header skipped", lineNo))
		case TypeAccount:
			var a Account
			if err := json.Unmarshal([]byte(line), &a); err != nil {
				return doc, fmt.Errorf("line %d: invalid account: %w", lineNo, err)
			}
			doc.Accounts = append(doc.Accounts, a)
		case TypeAgent:
			var a Agent
			if err := json.Unmarshal([]byte(line), &a); err != nil {
				return doc, fmt.Errorf("line %d: invalid agent: %w", lineNo, err)
			}
			doc.Agents = append(doc.Agents, a)
		case TypeSecret:
			var sec Secret
			if err := json.Unmarshal([]byte(line), &sec); err != nil {
				return doc, fmt.Errorf("line %d: invalid secret: %w", lineNo, err)
			}
			doc.Secrets = append(doc.Secrets, sec)
		default:
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("line %d: unknown _type %q skipped", lineNo, rt.Type))
		}
	}

	if err := s.Err(); err != nil {
		return doc, fmt.Errorf("scan jsonl: %w", err)
	}
	if !headerSeen {
		return doc, fmt.Errorf("missing header")
	}
	return doc, nil
}
