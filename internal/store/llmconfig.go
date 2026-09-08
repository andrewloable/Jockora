// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// llmSetting is where the operator's chosen model lives.
//
// One row in settings rather than four, so a half-applied change is not a
// state: a provider saved without its key would refuse every break until
// somebody noticed.
const llmSetting = "llm_config"

// LLMSettings is the stored configuration, key included.
//
// THE KEY IS HERE AND NEVER LEAVES THROUGH THE API. It is stored because the
// alternative is an environment variable that only a shell can change, which is
// what made three provider migrations in one evening a sequence of ssh
// sessions. The API layer answers with a boolean saying whether one is set,
// exactly as a user answers with a role and never a password hash.
type LLMSettings struct {
	Provider string `json:"provider"`
	URL      string `json:"url,omitempty"`
	Account  string `json:"account,omitempty"`
	Model    string `json:"model,omitempty"`
	Key      string `json:"key,omitempty"`
}

// SaveLLMSettings records the operator's choice.
func (s *Store) SaveLLMSettings(ctx context.Context, c LLMSettings) error {
	// Five strings. json.Marshal cannot fail on that, and a branch no input can
	// reach is a branch no test can cover honestly.
	raw, _ := json.Marshal(c) //nolint:errcheck // cannot fail for a struct of strings
	return s.SetSetting(ctx, llmSetting, string(raw))
}

// LLMSettings reads the operator's choice, and says whether one was ever made.
func (s *Store) LLMSettings(ctx context.Context) (LLMSettings, bool, error) {
	raw, ok, err := s.Setting(ctx, llmSetting)
	if err != nil || !ok {
		return LLMSettings{}, false, err
	}
	var c LLMSettings
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		// Stored by this program and unreadable now: say so rather than
		// silently falling back to the startup configuration, which would make
		// a station quietly ignore the operator's choice.
		return LLMSettings{}, false, fmt.Errorf("store: reading the model settings: %w", err)
	}
	return c, true, nil
}
