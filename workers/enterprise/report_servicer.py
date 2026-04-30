"""Enterprise report gRPC shim."""

from __future__ import annotations

from collections.abc import AsyncIterator

import grpc
from enterprise.v1 import report_pb2, report_pb2_grpc
from workers.knowledge.servicer import KnowledgeServicer


class EnterpriseReportServicer(report_pb2_grpc.EnterpriseReportServiceServicer):
    """Serves enterprise report generation.

    CA-122: GenerateReport is server-streaming. The shim is a thin
    delegator to KnowledgeServicer._generate_report which produces an
    AsyncIterator[GenerateReportStreamMessage].
    """

    def __init__(self, knowledge_servicer: KnowledgeServicer) -> None:
        self._knowledge_servicer = knowledge_servicer

    async def GenerateReport(  # noqa: N802
        self,
        request: report_pb2.GenerateReportRequest,
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[report_pb2.GenerateReportStreamMessage]:
        async for msg in self._knowledge_servicer._generate_report(request, context):
            yield msg
