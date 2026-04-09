# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""CorpusSource adapters.

Each adapter wraps a concrete input (a knowledge snapshot, a
requirements document, a markdown collection) as the CorpusSource
interface consumed by every comprehension strategy in this package.
"""

from workers.comprehension.adapters.code import CodeCorpus

__all__ = ["CodeCorpus"]
