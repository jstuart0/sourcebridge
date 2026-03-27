"""gRPC servicer for the ContractsService."""

from __future__ import annotations

import grpc
import structlog
from contracts.v1 import contracts_pb2, contracts_pb2_grpc

from workers.contracts.detector import detect_contracts

log = structlog.get_logger()


class ContractsServicer(contracts_pb2_grpc.ContractsServiceServicer):
    """Implements the ContractsService gRPC service."""

    async def DetectContracts(  # noqa: N802
        self,
        request: contracts_pb2.DetectContractsRequest,
        context: grpc.aio.ServicerContext,
    ) -> contracts_pb2.DetectContractsResponse:
        """Detect API contracts in provided files."""
        log.info(
            "detect_contracts",
            repository_id=request.repository_id,
            file_count=len(request.files),
        )

        try:
            files = [(f.path, f.content) for f in request.files]
            detected = detect_contracts(files)
        except Exception as exc:
            log.error("detect_contracts_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Detection failed: {exc}")
            return contracts_pb2.DetectContractsResponse()  # type: ignore[return-value]

        proto_contracts = []
        for c in detected:
            endpoints = [
                contracts_pb2.ContractEndpoint(
                    path=ep.path,
                    method=ep.method,
                    description=ep.description,
                )
                for ep in c.endpoints
            ]
            proto_contracts.append(
                contracts_pb2.ContractFile(
                    file_path=c.file_path,
                    contract_type=c.contract_type,
                    version=c.version,
                    content_hash=c.content_hash,
                    endpoints=endpoints,
                )
            )

        log.info("detect_contracts_done", found=len(proto_contracts))
        return contracts_pb2.DetectContractsResponse(contracts=proto_contracts)

    async def MatchConsumers(  # noqa: N802
        self,
        request: contracts_pb2.MatchConsumersRequest,
        context: grpc.aio.ServicerContext,
    ) -> contracts_pb2.MatchConsumersResponse:
        """Match source files to detected API contracts.

        This is a placeholder — full consumer matching requires LLM analysis
        of import patterns and HTTP client calls. For now, returns empty.
        """
        log.info(
            "match_consumers",
            repository_id=request.repository_id,
            contract_count=len(request.contracts),
            source_file_count=len(request.source_files),
        )
        return contracts_pb2.MatchConsumersResponse()
