// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"log/slog"
	"sync/atomic"

	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
)

var knowledgeProgressWriteErrorsTotal atomic.Int64
var knowledgeJobLogWriteErrorsTotal atomic.Int64

func KnowledgeProgressWriteErrorsTotal() int64 {
	return knowledgeProgressWriteErrorsTotal.Load()
}

func KnowledgeJobLogWriteErrorsTotal() int64 {
	return knowledgeJobLogWriteErrorsTotal.Load()
}

func appendJobLog(orch *orchestrator.Orchestrator, rt llm.Runtime, level llm.JobLogLevel, phase, event, message string, payload map[string]any) {
	if orch == nil || rt == nil || rt.JobID() == "" {
		return
	}
	if err := orch.AppendJobLog(rt.JobID(), level, phase, event, message, payload); err != nil {
		knowledgeJobLogWriteErrorsTotal.Add(1)
		slog.Warn("knowledge_job_log_write_failed",
			"event", "knowledge_job_log_write_failed",
			"job_id", rt.JobID(),
			"phase", phase,
			"log_event", event,
			"error", err)
	}
}
