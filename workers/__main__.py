"""SourceBridge worker entry point -- starts all gRPC services using grpc.aio."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import os
import sys

import grpc
import structlog
from grpc_health.v1 import health, health_pb2, health_pb2_grpc
from grpc_reflection.v1alpha import reflection

# Ensure generated proto stubs are importable
_GEN_PYTHON = os.path.join(os.path.dirname(__file__), "..", "gen", "python")
if _GEN_PYTHON not in sys.path:
    sys.path.insert(0, os.path.abspath(_GEN_PYTHON))

from contracts.v1 import contracts_pb2, contracts_pb2_grpc  # noqa: E402
from knowledge.v1 import knowledge_pb2, knowledge_pb2_grpc  # noqa: E402
from linking.v1 import linking_pb2, linking_pb2_grpc  # noqa: E402
from reasoning.v1 import reasoning_pb2, reasoning_pb2_grpc  # noqa: E402
from requirements.v1 import requirements_pb2, requirements_pb2_grpc  # noqa: E402

from workers.common.config import WorkerConfig  # noqa: E402
from workers.common.embedding.config import create_embedding_provider  # noqa: E402
from workers.common.llm.factory import create_llm_provider, create_report_provider  # noqa: E402
from workers.contracts.servicer import ContractsServicer  # noqa: E402
from workers.knowledge.servicer import KnowledgeServicer  # noqa: E402
from workers.linking.servicer import LinkingServicer  # noqa: E402
from workers.reasoning.servicer import ReasoningServicer  # noqa: E402
from workers.requirements.servicer import RequirementsServicer  # noqa: E402


def configure_logging() -> None:
    """Configure structured JSON logging."""
    structlog.configure(
        processors=[
            structlog.contextvars.merge_contextvars,
            structlog.processors.add_log_level,
            structlog.processors.StackInfoRenderer(),
            structlog.dev.set_exc_info,
            structlog.processors.TimeStamper(fmt="iso"),
            structlog.processors.JSONRenderer(),
        ],
        wrapper_class=structlog.make_filtering_bound_logger(logging.INFO),
        context_class=dict,
        logger_factory=structlog.PrintLoggerFactory(),
        cache_logger_on_first_use=True,
    )


async def serve() -> None:
    """Create, configure, and run the async gRPC server."""
    configure_logging()
    log = structlog.get_logger()

    config = WorkerConfig()
    log.info(
        "starting_worker",
        port=config.grpc_port,
        llm_provider=config.llm_provider,
        embedding_provider=config.embedding_provider,
        test_mode=config.test_mode,
    )

    # --- Initialize providers (long-lived, connection-pooled) ---
    llm_provider = create_llm_provider(config)
    report_llm = create_report_provider(config)
    if report_llm:
        log.info("report_llm_provider_configured", provider=config.llm_report_provider or config.llm_provider, model=config.llm_report_model)
    embedding_provider = create_embedding_provider(config)

    # --- Build async gRPC server ---
    server = grpc.aio.server(
        options=[
            ("grpc.max_receive_message_length", 50 * 1024 * 1024),  # 50 MB
            ("grpc.max_send_message_length", 50 * 1024 * 1024),
        ],
    )

    # --- Register servicers ---
    reasoning_servicer = ReasoningServicer(llm_provider, embedding_provider)
    reasoning_pb2_grpc.add_ReasoningServiceServicer_to_server(reasoning_servicer, server)

    linking_servicer = LinkingServicer(llm_provider, embedding_provider)
    linking_pb2_grpc.add_LinkingServiceServicer_to_server(linking_servicer, server)

    requirements_servicer = RequirementsServicer(llm_provider)
    requirements_pb2_grpc.add_RequirementsServiceServicer_to_server(requirements_servicer, server)

    knowledge_servicer = KnowledgeServicer(llm_provider, embedding_provider, report_llm=report_llm, worker_config=config)
    knowledge_pb2_grpc.add_KnowledgeServiceServicer_to_server(knowledge_servicer, server)

    contracts_servicer = ContractsServicer()
    contracts_pb2_grpc.add_ContractsServiceServicer_to_server(contracts_servicer, server)

    # --- Health service ---
    health_servicer = health.aio.HealthServicer()
    health_pb2_grpc.add_HealthServicer_to_server(health_servicer, server)
    await health_servicer.set("", health_pb2.HealthCheckResponse.SERVING)
    await health_servicer.set("sourcebridge.reasoning.v1.ReasoningService", health_pb2.HealthCheckResponse.SERVING)
    await health_servicer.set("sourcebridge.linking.v1.LinkingService", health_pb2.HealthCheckResponse.SERVING)
    await health_servicer.set("sourcebridge.requirements.v1.RequirementsService", health_pb2.HealthCheckResponse.SERVING)
    await health_servicer.set("sourcebridge.knowledge.v1.KnowledgeService", health_pb2.HealthCheckResponse.SERVING)
    await health_servicer.set("sourcebridge.contracts.v1.ContractsService", health_pb2.HealthCheckResponse.SERVING)

    # --- Server reflection ---
    service_names = (
        reasoning_pb2.DESCRIPTOR.services_by_name["ReasoningService"].full_name,
        linking_pb2.DESCRIPTOR.services_by_name["LinkingService"].full_name,
        requirements_pb2.DESCRIPTOR.services_by_name["RequirementsService"].full_name,
        knowledge_pb2.DESCRIPTOR.services_by_name["KnowledgeService"].full_name,
        contracts_pb2.DESCRIPTOR.services_by_name["ContractsService"].full_name,
        health_pb2.DESCRIPTOR.services_by_name["Health"].full_name,
        reflection.SERVICE_NAME,
    )
    reflection.enable_server_reflection(service_names, server)

    # --- Start listening ---
    listen_addr = f"[::]:{config.grpc_port}"
    server.add_insecure_port(listen_addr)
    await server.start()
    log.info("worker_started", address=listen_addr)

    # --- Graceful shutdown on signals ---
    loop = asyncio.get_running_loop()
    shutdown_event = asyncio.Event()

    def _signal_handler() -> None:
        log.info("shutdown_signal_received")
        shutdown_event.set()

    for sig_name in ("SIGINT", "SIGTERM"):
        with contextlib.suppress(NotImplementedError, ValueError):
            loop.add_signal_handler(getattr(__import__("signal"), sig_name), _signal_handler)

    await shutdown_event.wait()

    log.info("shutting_down")
    # Close embedding provider if it has a close method (e.g. OllamaEmbeddingProvider)
    if hasattr(embedding_provider, "close"):
        await embedding_provider.close()

    await server.stop(grace=5)
    log.info("worker_stopped")


def main() -> None:
    """Synchronous entry point for ``sourcebridge-worker`` console script."""
    asyncio.run(serve())


if __name__ == "__main__":
    main()
