# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

from workers.common.mermaid.validator import infer_edge_labels, validate_and_repair_mermaid


def test_validate_and_repair_mermaid_strips_fences_and_flowchart():
    result = validate_and_repair_mermaid(
        """```mermaid
        A["api"] --> B["db"]
        ```"""
    )

    assert result.validation_status == "repaired"
    assert result.mermaid_source.startswith("flowchart LR")


def test_validate_and_repair_mermaid_renames_conflicting_subgraph_ids():
    result = validate_and_repair_mermaid(
        """flowchart LR
        api["api"]
        subgraph api["api"]
            worker["worker"]
        end
        api --> worker
        """
    )

    assert result.validation_status == "repaired"
    assert "api_group" in result.mermaid_source
    assert ("api", "worker") in infer_edge_labels(result)


def test_validate_and_repair_mermaid_normalizes_quoted_edge_labels():
    result = validate_and_repair_mermaid(
        """flowchart TD
        api["api"] -->|"31 calls"| db["db"]
        """
    )

    assert result.validation_status == "repaired"
    assert "-->|31 calls|" in result.mermaid_source
    assert '"31 calls"' not in result.mermaid_source
