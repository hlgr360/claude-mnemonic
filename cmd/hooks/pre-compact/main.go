// Package main provides the pre-compact hook entry point.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/hooks"
)

var debug = os.Getenv("CLAUDE_MNEMONIC_DEBUG") != ""

// excerptChars bounds the conversation excerpt sent to the worker.
const excerptChars = 24000

// Input is the hook input from Claude Code.
type Input struct {
	hooks.BaseInput
	TranscriptPath string `json:"transcript_path"`
	// Trigger is "manual" (/compact) or "auto".
	Trigger string `json:"trigger"`
}

func main() {
	if !hooks.IsWorkerAvailable() {
		hooks.WriteResponse("PreCompact", true)
		return
	}
	hooks.RunHook("PreCompact", handlePreCompact)
}

// handlePreCompact asks the worker for a summary of the conversation before Claude Code compacts it,
// so the decisions and open threads that only live in the conversation survive. It never blocks
// the compaction: whatever goes wrong, the compaction proceeds.
func handlePreCompact(ctx *hooks.HookContext, input *Input) (string, error) {
	deadline, cancel := hooks.HookDeadline(30 * time.Second)
	defer cancel()

	result, err := hooks.GET(ctx.Port, fmt.Sprintf("/api/sessions?claudeSessionId=%s", ctx.SessionID))
	if err != nil || result == nil {
		return "", nil
	}
	sessionID, ok := result["id"].(float64)
	if !ok {
		return "", nil
	}

	msgs := hooks.ReadConversation(input.TranscriptPath)
	conversation := hooks.Excerpt(msgs, excerptChars)
	if conversation == "" {
		return "", nil
	}
	lastUser, lastAssistant := hooks.LastOf(msgs, "user"), hooks.LastOf(msgs, "assistant")
	if len(lastAssistant) > 10000 {
		lastAssistant = lastAssistant[:10000]
	}
	if len(lastUser) > 5000 {
		lastUser = lastUser[:5000]
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[pre-compact] trigger=%s messages=%d excerpt=%d chars\n", input.Trigger, len(msgs), len(conversation))
	}
	if deadline.Err() != nil {
		return "", nil
	}

	if _, err = hooks.POST(ctx.Port, fmt.Sprintf("/sessions/%d/summarize", int64(sessionID)), map[string]interface{}{
		"lastUserMessage":      lastUser,
		"lastAssistantMessage": lastAssistant,
		"conversation":         conversation,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "[pre-compact] Warning: summary request failed: %v\n", err)
	}
	return "", nil
}
