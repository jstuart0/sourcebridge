"""Enterprise report gRPC shim."""

from __future__ import annotations

import grpc
from enterprise.v1 import report_pb2_grpc
from knowledge.v1 import knowledge_pb2
from workers.knowledge.servicer import KnowledgeServicer


class EnterpriseReportServicer(report_pb2_grpc.EnterpriseReportServiceServicer):
    """Delegates enterprise report RPCs to the legacy knowledge servicer."""

    def __init__(self, knowledge_servicer: KnowledgeServicer) -> None:
        self._knowledge_servicer = knowledge_servicer

    async def GenerateReport(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateReportRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateReportResponse:
        return await self._knowledge_servicer.GenerateReport(request, context)
