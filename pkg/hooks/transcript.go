package hooks

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	// transcriptTail is how much of the end of a transcript is read; older parts are not needed.
	transcriptTail = 2 << 20
	// excerptMessageChars bounds one message in a conversation excerpt.
	excerptMessageChars = 1500
)

// Message is one user or assistant turn of a Claude Code transcript, text only.
type Message struct {
	Role string `json:"role"` // "user" or "assistant"
	Text string `json:"text"`
}

type transcriptLine struct {
	Message struct {
		Content any `json:"content"`
	} `json:"message"`
	Type string `json:"type"`
}

// textOf returns the text of a message's content (a string, or the text blocks of a list).
// Tool calls and tool results carry no text and yield "".
func textOf(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok && m["type"] == "text" {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// ReadConversation returns the text turns from the end of a transcript, oldest first.
// A missing or unreadable file yields no messages.
func ReadConversation(path string) []Message {
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = strings.Replace(path, "~", home, 1)
		}
	}
	file, err := os.Open(path) // #nosec G304 -- path is the conversation file Claude Code reports
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()

	if info, statErr := file.Stat(); statErr == nil && info.Size() > transcriptTail {
		if _, seekErr := file.Seek(-transcriptTail, io.SeekEnd); seekErr == nil {
			bufio.NewScanner(file).Scan() // drop the partial first line
		}
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var out []Message
	for scanner.Scan() {
		var line transcriptLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil || (line.Type != "user" && line.Type != "assistant") {
			continue
		}
		if text := strings.TrimSpace(textOf(line.Message.Content)); text != "" {
			out = append(out, Message{Role: line.Type, Text: text})
		}
	}
	return out
}

// Conversation returns the messages a client passed inline (clients without a Claude Code transcript, such as pi,
// send them this way), else the text turns read from the transcript at path. Inline messages keep only user and
// assistant turns with text, as ReadConversation does.
func Conversation(inline []Message, path string) []Message {
	if len(inline) == 0 {
		return ReadConversation(path)
	}
	var out []Message
	for _, m := range inline {
		if text := strings.TrimSpace(m.Text); text != "" && (m.Role == "user" || m.Role == "assistant") {
			out = append(out, Message{Role: m.Role, Text: text})
		}
	}
	return out
}

// LastOf returns the text of the most recent message with the given role.
func LastOf(msgs []Message, role string) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == role {
			return msgs[i].Text
		}
	}
	return ""
}

// Excerpt renders the most recent messages that fit in maxChars, oldest first, each cut to a
// reasonable length. When older messages were left out, it says so on the first line.
func Excerpt(msgs []Message, maxChars int) string {
	var picked []string
	total, omitted := 0, false
	for i := len(msgs) - 1; i >= 0; i-- {
		label := "Assistant"
		if msgs[i].Role == "user" {
			label = "User"
		}
		entry := label + ": " + clipRunes(msgs[i].Text, excerptMessageChars)
		if total+len(entry)+2 > maxChars {
			omitted = true
			break
		}
		picked = append(picked, entry)
		total += len(entry) + 2
	}
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	text := strings.Join(picked, "\n\n")
	if omitted && text != "" {
		text = "(earlier messages left out)\n\n" + text
	}
	return text
}

func clipRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + " …"
}
