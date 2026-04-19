"""Enterprise report gRPC shim."""

from __future__ import annotations

import grpc
from enterprise.v1 import report_pb2, report_pb2_grpc
from workers.knowledge.servicer import KnowledgeServicer


class EnterpriseReportServicer(report_pb2_grpc.EnterpriseReportServiceServicer):
    """Serves enterprise report generation."""

    def __init__(self, knowledge_servicer: KnowledgeServicer) -> None:
        self._knowledge_servicer = knowledge_servicer

    async def GenerateReport(  # noqa: N802
        self,
        request: report_pb2.GenerateReportRequest,
        context: grpc.aio.ServicerContext,
    ) -> report_pb2.GenerateReportResponse:
        return await self._knowledge_servicer._generate_report(request, context)
