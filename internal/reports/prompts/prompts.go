// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package prompts loads voice and audience prompt fragments from the
// repository's prompts/ directory. Templates embed voice fragments verbatim
// into LLM system prompts so the same voice rules apply consistently across
// all generation paths.
//
// # File layout
//
//	prompts/voice/engineer-to-engineer.md
//	prompts/voice/engineer-to-pm.md
//	prompts/voice/engineer-to-operator.md
//
// # Usage
//
//	voice, err := prompts.LoadVoice("engineer-to-engineer")
//	if err != nil { ... }
//	systemPrompt := fmt.Sprintf("...\n\n%s", voice)
package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// voiceCache holds loaded voice files so each name is read from disk once.
var (
	voiceCache   = map[string]string{}
	voiceCacheMu sync.RWMutex
)

// repoRoot is the absolute path to the repository root. It is computed once
// from the location of this source file. We walk up from the package
// directory until we find a go.mod file.
var repoRoot = sync.OnceValue(func() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
})

// LoadVoice loads the voice prompt fragment for the given name.
// name must be one of: "engineer-to-engineer", "engineer-to-pm",
// "engineer-to-operator". The file is read from
// prompts/voice/<name>.md relative to the repository root.
//
// The return value is the full markdown content of the file, suitable for
// embedding verbatim in an LLM system prompt. Results are cached after the
// first read.
func LoadVoice(name string) (string, error) {
	name = strings.TrimSuffix(name, ".md")

	voiceCacheMu.RLock()
	if v, ok := voiceCache[name]; ok {
		voiceCacheMu.RUnlock()
		return v, nil
	}
	voiceCacheMu.RUnlock()

	root := repoRoot()
	if root == "" {
		return "", fmt.Errorf("prompts: cannot locate repository root from source file path")
	}

	path := filepath.Join(root, "prompts", "voice", name+".md")
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("prompts: loading voice %q: %w", name, err)
	}

	voiceCacheMu.Lock()
	voiceCache[name] = string(content)
	voiceCacheMu.Unlock()

	return string(content), nil
}
