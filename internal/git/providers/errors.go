// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package providers

import "errors"

// ErrCommercialOnly is returned when a commercial-only feature is accessed in OSS mode.
var ErrCommercialOnly = errors.New("this feature requires SourceBridge Enterprise")
