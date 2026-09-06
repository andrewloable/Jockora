// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import "testing"

// TestStripReasoningKeepsTheAnswer. Most free OpenRouter models are reasoning
// models, so "choose a different one" is a poor answer where stripping works.
func TestStripReasoningKeepsTheAnswer(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<think>The user wants JSON. I should be careful.</think>{"ok":true}`, `{"ok":true}`},
		{"<thinking>\nlots\nof\nlines\n</thinking>\n\n{\"ok\":true}", `{"ok":true}`},
		{`<reasoning>hmm</reasoning>{"a":1}`, `{"a":1}`},
		{"```json\n{\"ok\":true}\n```", `{"ok":true}`},
		{"<think>x</think>```json\n{\"ok\":true}\n```", `{"ok":true}`},
		// Untouched when there is nothing to strip.
		{`{"ok":true}`, `{"ok":true}`},
		{`Midnight here. That was loud.`, `Midnight here. That was loud.`},
	}
	for _, tc := range cases {
		if got := StripReasoning(tc.in); got != tc.want {
			t.Errorf("StripReasoning(%q)\n  = %q\n want %q", tc.in, got, tc.want)
		}
	}
}

// TestStripReasoningOnTruncatedThought: an opening tag with no close means the
// answer was never reached. What remains must NOT be parsed as if it were the
// answer -- half a thought is not JSON, and pretending otherwise turns a budget
// problem into a mysterious parse failure.
func TestStripReasoningOnTruncatedThought(t *testing.T) {
	got := StripReasoning("<think>I am still thinking about this and ran out of")
	if got != "" {
		t.Errorf("StripReasoning of a truncated thought = %q, want empty", got)
	}

	// Content BEFORE an unterminated tag is still content.
	if got := StripReasoning(`{"ok":true} <think>afterthought`); got != `{"ok":true}` {
		t.Errorf("dropped content that preceded the tag: %q", got)
	}
}
