"""API contract detection from repository files.

Detects OpenAPI/Swagger specs, Protobuf service definitions, and GraphQL schemas
by inspecting file paths and content patterns.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass, field


@dataclass
class Endpoint:
    path: str
    method: str
    description: str = ""


@dataclass
class DetectedContract:
    file_path: str
    contract_type: str  # openapi, protobuf, graphql
    version: str
    content_hash: str
    endpoints: list[Endpoint] = field(default_factory=list)


def detect_openapi(path: str, content: str) -> DetectedContract | None:
    """Detect OpenAPI/Swagger specification files."""
    # Quick path-based filter
    lower_path = path.lower()
    if not any(kw in lower_path for kw in ("openapi", "swagger", "api-spec", "api_spec")) and not (
        lower_path.endswith((".yaml", ".yml", ".json"))
    ):
        return None

    # Check for OpenAPI markers in content
    is_openapi = False
    version = ""

    if '"openapi"' in content or "openapi:" in content:
        is_openapi = True
        m = re.search(r'openapi["\s:]+["\s]*(\d+\.\d+\.\d+)', content)
        if m:
            version = m.group(1)
    elif '"swagger"' in content or "swagger:" in content:
        is_openapi = True
        version = "2.0"
        m = re.search(r'swagger["\s:]+["\s]*(\d+\.\d+)', content)
        if m:
            version = m.group(1)

    if not is_openapi:
        return None

    endpoints = _extract_openapi_endpoints(content)
    return DetectedContract(
        file_path=path,
        contract_type="openapi",
        version=version,
        content_hash=hashlib.sha256(content.encode()).hexdigest()[:16],
        endpoints=endpoints,
    )


def _extract_openapi_endpoints(content: str) -> list[Endpoint]:
    """Extract endpoint paths from OpenAPI content."""
    endpoints: list[Endpoint] = []

    # Try JSON parsing first
    try:
        spec = json.loads(content)
        paths = spec.get("paths", {})
        for path, methods in paths.items():
            if not isinstance(methods, dict):
                continue
            for method, details in methods.items():
                if method.lower() in ("get", "post", "put", "patch", "delete", "head", "options"):
                    desc = ""
                    if isinstance(details, dict):
                        raw_desc = details.get("summary", details.get("description", ""))
                        desc = raw_desc if isinstance(raw_desc, str) else ""
                    endpoints.append(Endpoint(path=path, method=method.upper(), description=desc))
        return endpoints
    except (json.JSONDecodeError, AttributeError):
        pass

    # Fallback: regex-based extraction for YAML
    re.compile(r"^  (/[^\s:]+):\s*$", re.MULTILINE)
    re.compile(r"^    (get|post|put|patch|delete|head|options):\s*$", re.MULTILINE)

    current_path = ""
    for line in content.split("\n"):
        pm = re.match(r"^  (/[^\s:]+):\s*$", line)
        if pm:
            current_path = pm.group(1)
            continue
        mm = re.match(r"^    (get|post|put|patch|delete|head|options):\s*$", line)
        if mm and current_path:
            endpoints.append(Endpoint(path=current_path, method=mm.group(1).upper()))

    return endpoints


def detect_protobuf(path: str, content: str) -> DetectedContract | None:
    """Detect Protobuf service definitions."""
    if not path.endswith(".proto"):
        return None

    # Must contain a service definition
    service_match = re.search(r"service\s+(\w+)\s*\{", content)
    if not service_match:
        return None

    version = "proto3" if 'syntax = "proto3"' in content else "proto2"

    endpoints: list[Endpoint] = []
    # Extract RPCs
    for m in re.finditer(r"rpc\s+(\w+)\s*\(\s*(\w+)\s*\)\s*returns\s*\(\s*(\w+)\s*\)", content):
        rpc_name = m.group(1)
        service_name = service_match.group(1)
        endpoints.append(
            Endpoint(
                path=f"{service_name}.{rpc_name}",
                method="RPC",
            )
        )

    return DetectedContract(
        file_path=path,
        contract_type="protobuf",
        version=version,
        content_hash=hashlib.sha256(content.encode()).hexdigest()[:16],
        endpoints=endpoints,
    )


def detect_graphql_schema(path: str, content: str) -> DetectedContract | None:
    """Detect GraphQL schema definition files."""
    lower_path = path.lower()
    is_gql = lower_path.endswith((".graphql", ".graphqls", ".gql"))

    if not is_gql:
        return None

    # Must contain type definitions
    if not re.search(r"type\s+(Query|Mutation|Subscription)\s*\{", content):
        return None

    endpoints: list[Endpoint] = []
    # Extract query/mutation fields
    for type_match in re.finditer(r"type\s+(Query|Mutation)\s*\{([^}]+)\}", content, re.DOTALL):
        type_name = type_match.group(1)
        body = type_match.group(2)
        for field_match in re.finditer(r"(\w+)\s*(?:\([^)]*\))?\s*:", body):
            endpoints.append(
                Endpoint(
                    path=field_match.group(1),
                    method=type_name.upper(),
                )
            )

    return DetectedContract(
        file_path=path,
        contract_type="graphql",
        version="",
        content_hash=hashlib.sha256(content.encode()).hexdigest()[:16],
        endpoints=endpoints,
    )


def detect_contracts(files: list[tuple[str, str]]) -> list[DetectedContract]:
    """Detect all API contracts in the given files.

    Args:
        files: List of (file_path, content) tuples.

    Returns:
        List of detected contracts.
    """
    contracts: list[DetectedContract] = []
    for path, content in files:
        for detector in (detect_openapi, detect_protobuf, detect_graphql_schema):
            result = detector(path, content)
            if result:
                contracts.append(result)
                break  # One contract type per file
    return contracts
