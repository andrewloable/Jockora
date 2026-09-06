// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"regexp"
	"strings"
)

// reasoningBlock matches inline chain-of-thought a model wraps around its
// answer.
//
// There are TWO ways reasoning arrives and they need different handling. A
// hosted API usually puts it in a separate `reasoning` field, where ignoring it
// is free. A local model has nowhere to put it, so it emits it INLINE, and the
// answer only starts after the closing tag. Every family spells the tag
// differently, hence the alternation.
var reasoningBlock = regexp.MustCompile(`(?is)<(think|thinking|reasoning|thought)>.*?</(think|thinking|reasoning|thought)>`)

// unterminatedReasoning matches an opening tag whose close never arrived,
// which is what a truncated reasoning model leaves behind.
var unterminatedReasoning = regexp.MustCompile(`(?is)<(think|thinking|reasoning|thought)>.*$`)

// StripReasoning removes inline chain-of-thought and returns what the model
// actually meant to say.
//
// It is deliberately conservative about the unterminated case: an opening tag
// with no close means the answer was never reached, so what remains is whatever
// preceded the tag -- usually nothing, which the caller then reports as empty
// rather than trying to parse half a thought as JSON.
func StripReasoning(s string) string {
	out := reasoningBlock.ReplaceAllString(s, "")
	out = unterminatedReasoning.ReplaceAllString(out, "")

	// Some models fence the answer after thinking. The fence is not content.
	out = strings.TrimSpace(out)
	if strings.HasPrefix(out, "```") {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
		out = strings.TrimSuffix(strings.TrimSpace(out), "```")
	}
	return strings.TrimSpace(out)
}
