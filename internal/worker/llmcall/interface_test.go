// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package llmcall_test

import (
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// Compile-time assertion that *worker.Client satisfies WorkerLLM. If a new
// LLM-bearing RPC is added to llmcall.WorkerLLM and not implemented on
// *worker.Client, this assertion fails at build time.
var _ llmcall.WorkerLLM = (*worker.Client)(nil)
