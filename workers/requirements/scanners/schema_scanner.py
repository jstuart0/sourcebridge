"""Scanner for extracting specs from API schema files (OpenAPI, Protobuf, GraphQL)."""

from __future__ import annotations

import json
import os
import re

import yaml  # type: ignore[import-untyped]

from workers.requirements.spec_models import CandidateSpec

# Schema file detection patterns
OPENAPI_FILES = {"openapi.yaml", "openapi.yml", "openapi.json", "swagger.yaml", "swagger.yml", "swagger.json"}
OPENAPI_KEYS = {"openapi:", "swagger:"}


class APISchemaScanner:
    """Extracts candidate specs from API schema files."""

    def is_schema_file(self, path: str, content: str) -> bool:
        """Check if a file is an API schema file."""
        basename = os.path.basename(path)

        # Direct filename match
        if basename in OPENAPI_FILES:
            return True

        # Protobuf
        if path.endswith(".proto"):
            return True

        # GraphQL
        if path.endswith((".graphql", ".graphqls")):
            return True

        # YAML files with openapi/swagger root key
        if path.endswith((".yaml", ".yml")):
            first_lines = content[:500]
            return any(key in first_lines for key in OPENAPI_KEYS)

        return False

    def extract(self, path: str, content: str) -> list[CandidateSpec]:
        """Extract candidate specs from a schema file."""
        if path.endswith(".proto"):
            return self._extract_protobuf(path, content)
        elif path.endswith((".graphql", ".graphqls")):
            return self._extract_graphql(path, content)
        else:
            return self._extract_openapi(path, content)

    def _extract_protobuf(self, path: str, content: str) -> list[CandidateSpec]:
        """Extract specs from a protobuf file."""
        candidates: list[CandidateSpec] = []
        lines = content.split("\n")

        # Find service definitions and their RPCs
        service_name = ""
        for i, line in enumerate(lines, 1):
            svc_match = re.match(r"\s*service\s+(\w+)\s*\{", line)
            if svc_match:
                service_name = svc_match.group(1)
                continue

            rpc_match = re.match(
                r"\s*rpc\s+(\w+)\s*\(\s*(\w+)\s*\)\s*returns\s*\(\s*(\w+)\s*\)",
                line,
            )
            if rpc_match and service_name:
                method_name = rpc_match.group(1)
                request_type = rpc_match.group(2)
                response_type = rpc_match.group(3)

                # Collect comments above the RPC
                description = self._collect_comments(lines, i - 2)

                raw_text = (
                    f"RPC {method_name}: accepts {request_type}, returns {response_type}. Service: {service_name}."
                )
                if description:
                    raw_text = f"{description} {raw_text}"

                candidates.append(
                    CandidateSpec(
                        source="schema",
                        source_file=path,
                        source_line=i,
                        raw_text=raw_text,
                        group_key=f"{service_name}.{method_name}",
                        language="protobuf",
                        metadata={
                            "schema_type": "protobuf",
                            "service": service_name,
                            "method": method_name,
                            "request_type": request_type,
                            "response_type": response_type,
                            "description": description,
                        },
                    )
                )

        return candidates

    def _extract_graphql(self, path: str, content: str) -> list[CandidateSpec]:
        """Extract specs from a GraphQL schema file."""
        candidates: list[CandidateSpec] = []
        lines = content.split("\n")

        current_type = ""
        for i, line in enumerate(lines, 1):
            # Match type Query/Mutation/Subscription blocks
            type_match = re.match(r"\s*type\s+(Query|Mutation|Subscription)\s*\{", line)
            if type_match:
                current_type = type_match.group(1)
                continue

            if line.strip() == "}":
                current_type = ""
                continue

            if not current_type:
                continue

            # Match field definitions
            field_match = re.match(r"\s+(\w+)(?:\([^)]*\))?\s*:\s*(.+?)$", line.strip())
            if field_match:
                field_name = field_match.group(1)
                return_type = field_match.group(2).strip()

                # Collect comment above
                description = self._collect_graphql_comments(lines, i - 2)

                operation = current_type.lower()
                raw_text = f"{current_type} {field_name}: Returns {return_type}."
                if description:
                    raw_text = f"{description} {raw_text}"

                candidates.append(
                    CandidateSpec(
                        source="schema",
                        source_file=path,
                        source_line=i,
                        raw_text=raw_text,
                        group_key=f"{current_type}.{field_name}",
                        language="graphql",
                        metadata={
                            "schema_type": "graphql",
                            "operation": operation,
                            "name": field_name,
                            "return_type": return_type,
                            "description": description,
                        },
                    )
                )

        return candidates

    def _extract_openapi(self, path: str, content: str) -> list[CandidateSpec]:
        """Extract specs from an OpenAPI/Swagger YAML/JSON file."""
        candidates: list[CandidateSpec] = []

        # Try JSON first, then YAML
        try:
            data = json.loads(content)
        except (json.JSONDecodeError, ValueError):
            try:
                data = yaml.safe_load(content)
            except Exception:
                return candidates

        if not isinstance(data, dict):
            return candidates

        paths = data.get("paths", {})
        if not isinstance(paths, dict):
            return candidates

        for endpoint_path, methods in paths.items():
            if not isinstance(methods, dict):
                continue

            for method, details in methods.items():
                if method.upper() not in ("GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"):
                    continue
                if not isinstance(details, dict):
                    continue

                summary = details.get("summary", "")
                description = details.get("description", "")
                desc_text = summary or description

                # Build raw text
                parts = [f"{method.upper()} {endpoint_path}"]
                if desc_text:
                    parts.append(f"- {desc_text}")

                # Extract request fields
                request_body = details.get("requestBody", {})
                req_fields: list[str] = []
                req_required: list[str] = []
                if isinstance(request_body, dict):
                    for _ct, ct_data in request_body.get("content", {}).items():
                        schema = ct_data.get("schema", {}) if isinstance(ct_data, dict) else {}
                        props = schema.get("properties", {})
                        req_fields = list(props.keys()) if isinstance(props, dict) else []
                        req_required = schema.get("required", [])
                        break

                if req_fields:
                    parts.append(f"Request fields: {', '.join(req_fields)}")
                if req_required:
                    parts.append(f"Required: {', '.join(req_required)}")

                # Extract response info
                responses = details.get("responses", {})
                resp_status = ""
                resp_fields: list[str] = []
                for status, resp_data in responses.items() if isinstance(responses, dict) else []:
                    if status.startswith("2"):
                        resp_status = status
                        if isinstance(resp_data, dict):
                            for _ct, ct_data in resp_data.get("content", {}).items():
                                schema = ct_data.get("schema", {}) if isinstance(ct_data, dict) else {}
                                props = schema.get("properties", {})
                                resp_fields = list(props.keys()) if isinstance(props, dict) else []
                                break
                        break

                if resp_status:
                    parts.append(f"Response {resp_status}")
                if resp_fields:
                    parts.append(f"Response fields: {', '.join(resp_fields)}")

                raw_text = ". ".join(parts) + "."

                candidates.append(
                    CandidateSpec(
                        source="schema",
                        source_file=path,
                        source_line=1,  # YAML line numbers are hard to determine
                        raw_text=raw_text,
                        group_key=f"{method.upper()} {endpoint_path}",
                        language="openapi",
                        metadata={
                            "schema_type": "openapi",
                            "method": method.upper(),
                            "path": endpoint_path,
                            "request_fields": req_fields,
                            "required_fields": req_required,
                            "response_status": resp_status,
                            "response_fields": resp_fields,
                            "description": desc_text,
                        },
                    )
                )

        return candidates

    def _collect_comments(self, lines: list[str], end_idx: int) -> str:
        """Collect consecutive // comments above a given line index."""
        comments: list[str] = []
        idx = end_idx
        while idx >= 0:
            stripped = lines[idx].strip()
            if stripped.startswith("//"):
                comments.insert(0, stripped.lstrip("/ ").strip())
            else:
                break
            idx -= 1
        return " ".join(comments)

    def _collect_graphql_comments(self, lines: list[str], end_idx: int) -> str:
        """Collect consecutive # comments above a given line index."""
        comments: list[str] = []
        idx = end_idx
        while idx >= 0:
            stripped = lines[idx].strip()
            if stripped.startswith("#"):
                comments.insert(0, stripped.lstrip("# ").strip())
            else:
                break
            idx -= 1
        return " ".join(comments)
