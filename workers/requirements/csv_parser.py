"""CSV requirement parser.

Parses CSV files with configurable column mapping to extract requirements.
"""

import csv
import io

from workers.requirements.models import Requirement

# Default column mapping
DEFAULT_COLUMNS = {
    "id": "id",
    "title": "title",
    "description": "description",
    "priority": "priority",
    "acceptance_criteria": "acceptance_criteria",
}


def parse_csv(
    content: str,
    source: str = "",
    column_mapping: dict[str, str] | None = None,
) -> list[Requirement]:
    """Parse a CSV string and extract requirements.

    Args:
        content: The CSV text.
        source: Optional source path for attribution.
        column_mapping: Optional mapping from logical field names to CSV column names.

    Returns:
        List of parsed Requirement objects.
    """
    mapping = {**DEFAULT_COLUMNS, **(column_mapping or {})}

    reader = csv.DictReader(io.StringIO(content))
    requirements: list[Requirement] = []

    for row in reader:
        req_id = row.get(mapping["id"], "").strip()
        title = row.get(mapping["title"], "").strip()
        description = row.get(mapping["description"], "").strip()
        priority = row.get(mapping["priority"], "").strip()
        criteria_raw = row.get(mapping["acceptance_criteria"], "").strip()

        if not req_id or not title:
            continue  # Skip rows without ID or title

        # Parse acceptance criteria (semicolon-separated)
        criteria: list[str] = []
        if criteria_raw:
            criteria = [c.strip() for c in criteria_raw.split(";") if c.strip()]

        requirements.append(
            Requirement(
                id=req_id,
                title=title,
                description=description,
                priority=priority,
                acceptance_criteria=criteria,
                source=source,
            )
        )

    return requirements
