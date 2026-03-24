// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import "context"

type CleanupCapable interface {
	Cleanup(ctx context.Context) (int, error)
}
