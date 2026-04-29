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
from enterprise.v1 import report_pb2, report_pb2_grpc  # noqa: E402
from knowledge.v1 import knowledge_pb2, knowledge_pb2_grpc  # noqa: E402
from linking.v1 import linking_pb2, linking_pb2_grpc  # noqa: E402
from reasoning.v1 import reasoning_pb2, reasoning_pb2_grpc  # noqa: E402
from requirements.v1 import requirements_pb2, requirements_pb2_grpc  # noqa: E402

from workers.common.config import WorkerConfig  # noqa: E402
from workers.common.embedding.config import create_embedding_provider  # noqa: E402
from workers.common.llm.factory import create_llm_provider, create_report_provider  # noqa: E402
from workers.contracts.servicer import ContractsServicer  # noqa: E402
from workers.enterprise.report_servicer import EnterpriseReportServicer  # noqa: E402
from workers.knowledge.servicer import KnowledgeServicer  # noqa: E402
from workers.knowledge.summary_nodes import SurrealSummaryNodeCache  # noqa: E402
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
        log.info(
            "report_llm_provider_configured",
            provider=config.llm_report_provider or config.llm_provider,
            model=config.llm_report_model,
        )
    embedding_provider = create_embedding_provider(config)
    summary_node_cache = SurrealSummaryNodeCache.from_config(config)

    # --- Build async gRPC server ---
    server = grpc.aio.server(
        options=[
            ("grpc.max_receive_message_length", 50 * 1024 * 1024),  # 50 MB
            ("grpc.max_send_message_length", 50 * 1024 * 1024),
        ],
    )

    # --- Register servicers ---
    reasoning_servicer = ReasoningServicer(llm_provider, embedding_provider, worker_config=config)
    reasoning_pb2_grpc.add_ReasoningServiceServicer_to_server(reasoning_servicer, server)

    linking_servicer = LinkingServicer(llm_provider, embedding_provider)
    linking_pb2_grpc.add_LinkingServiceServicer_to_server(linking_servicer, server)

    requirements_servicer = RequirementsServicer(llm_provider, worker_config=config)
    requirements_pb2_grpc.add_RequirementsServiceServicer_to_server(requirements_servicer, server)

    knowledge_servicer = KnowledgeServicer(
        llm_provider,
        embedding_provider,
        default_model_id=config.llm_model,
        report_llm=report_llm,
        worker_config=config,
        summary_node_cache=summary_node_cache,
    )
    knowledge_pb2_grpc.add_KnowledgeServiceServicer_to_server(knowledge_servicer, server)
    report_pb2_grpc.add_EnterpriseReportServiceServicer_to_server(
        EnterpriseReportServicer(knowledge_servicer),
        server,
    )

    contracts_servicer = ContractsServicer()
    contracts_pb2_grpc.add_ContractsServiceServicer_to_server(contracts_servicer, server)

    # --- Health service ---
    health_servicer = health.aio.HealthServicer()
    health_pb2_grpc.add_HealthServicer_to_server(health_servicer, server)
    await health_servicer.set("", health_pb2.HealthCheckResponse.SERVING)
    await health_servicer.set("sourcebridge.reasoning.v1.ReasoningService", health_pb2.HealthCheckResponse.SERVING)
    await health_servicer.set("sourcebridge.linking.v1.LinkingService", health_pb2.HealthCheckResponse.SERVING)
    await health_servicer.set(
        "sourcebridge.requirements.v1.RequirementsService", health_pb2.HealthCheckResponse.SERVING
    )
    await health_servicer.set("sourcebridge.knowledge.v1.KnowledgeService", health_pb2.HealthCheckResponse.SERVING)
    await health_servicer.set(
        "sourcebridge.enterprise.v1.EnterpriseReportService",
        health_pb2.HealthCheckResponse.SERVING,
    )
    await health_servicer.set("sourcebridge.contracts.v1.ContractsService", health_pb2.HealthCheckResponse.SERVING)

    # --- Server reflection ---
    service_names = (
        reasoning_pb2.DESCRIPTOR.services_by_name["ReasoningService"].full_name,
        linking_pb2.DESCRIPTOR.services_by_name["LinkingService"].full_name,
        requirements_pb2.DESCRIPTOR.services_by_name["RequirementsService"].full_name,
        knowledge_pb2.DESCRIPTOR.services_by_name["KnowledgeService"].full_name,
        report_pb2.DESCRIPTOR.services_by_name["EnterpriseReportService"].full_name,
        contracts_pb2.DESCRIPTOR.services_by_name["ContractsService"].full_name,
        health_pb2.DESCRIPTOR.services_by_name["Health"].full_name,
        reflection.SERVICE_NAME,
    )
    reflection.enable_server_reflection(service_names, server)

    # --- Start listening ---
    # mTLS (slice 4 of plan 2026-04-29-workspace-llm-source-of-truth-r2.md).
    # When all three TLS env vars are set + tls_enabled=true, bind with
    # mutual auth via grpc.ssl_server_credentials. Otherwise fall back
    # to add_insecure_port (OSS dev / no-cert-manager environments).
    # Fail-closed: if tls_enabled=true but any path is missing/unreadable,
    # log fatal and exit non-zero. No silent fallback to insecure.
    listen_addr = f"[::]:{config.grpc_port}"
    if config.tls_enabled:
        # Validate the cert/key/CA paths are non-empty + readable + parse
        # cleanly. Fail-closed: any validation error logs fatal and exits
        # non-zero. No silent fallback to insecure once tls_enabled=true.
        if not (config.tls_cert_path and config.tls_key_path and config.tls_ca_path):
            log.error(
                "worker_tls_misconfigured",
                error="tls_enabled=true but cert/key/ca paths are not all set",
            )
            raise SystemExit(2)
        try:
            with open(config.tls_cert_path, "rb") as f:
                server_cert = f.read()
            with open(config.tls_key_path, "rb") as f:
                server_key = f.read()
            with open(config.tls_ca_path, "rb") as f:
                ca_bundle = f.read()
        except OSError as exc:
            log.error("worker_tls_load_failed", error=str(exc))
            raise SystemExit(2) from exc

        # Cheap parse validation — if any blob is empty or doesn't begin
        # with a PEM header, fail before binding.
        for label, blob in (
            ("cert", server_cert),
            ("key", server_key),
            ("ca", ca_bundle),
        ):
            if not blob.lstrip().startswith(b"-----BEGIN"):
                log.error(
                    "worker_tls_invalid_pem",
                    field=label,
                    path={
                        "cert": config.tls_cert_path,
                        "key": config.tls_key_path,
                        "ca": config.tls_ca_path,
                    }[label],
                )
                raise SystemExit(2)

        creds = grpc.ssl_server_credentials(
            [(server_key, server_cert)],
            root_certificates=ca_bundle,
            require_client_auth=True,
        )
        bound_port = server.add_secure_port(listen_addr, creds)
        if bound_port == 0:
            # gRPC returns 0 when the bind failed (port in use, bad creds,
            # etc.). Without this check the server start succeeds but
            # accepts no connections.
            log.error("worker_tls_bind_failed", listen_addr=listen_addr)
            raise SystemExit(2)
        log.info(
            "worker_tls_enabled",
            cert_path=config.tls_cert_path,
            ca_path=config.tls_ca_path,
            bound_port=bound_port,
        )
    else:
        server.add_insecure_port(listen_addr)
    await server.start()
    log.info("worker_started", address=listen_addr, tls_enabled=config.tls_enabled)

    # --- Graceful shutdown on signals ---
    #
    # R3 followups T1.8: a Reloader-triggered rollout (cert rotation) or
    # any other SIGTERM must NOT cancel a 30+ minute knowledge-generation
    # RPC mid-flight. The drain sequence is:
    #
    #   1. SIGTERM arrives → flip the health-servicer aggregate to
    #      NOT_SERVING. The kubelet's gRPC readiness probe stops
    #      considering this pod healthy; the Service endpoint flips
    #      AWAY from this pod for new work. In-flight RPCs continue.
    #   2. Call server.stop(grace=N) where N is config.shutdown_grace_seconds
    #      (default 3600s). gRPC keeps existing RPCs running for up to N
    #      seconds; only at the N-second mark does it force-cancel
    #      anything still active.
    #   3. After server.stop returns (drain complete OR grace expired),
    #      close the embedding provider. Ordering matters: closing
    #      providers BEFORE server.stop is wrong — in-flight embedding
    #      RPCs would see a half-closed provider during the grace window.
    #
    # The Kubernetes Deployment's terminationGracePeriodSeconds must
    # exceed shutdown_grace_seconds so kubelet's SIGKILL stays the
    # OUTER bound. Today: 3900s (65 minutes) outer vs 3600s (60 minutes)
    # inner.
    loop = asyncio.get_running_loop()
    shutdown_event = asyncio.Event()

    def _signal_handler() -> None:
        log.info("shutdown_signal_received")
        shutdown_event.set()

    for sig_name in ("SIGINT", "SIGTERM"):
        with contextlib.suppress(NotImplementedError, ValueError):
            loop.add_signal_handler(getattr(__import__("signal"), sig_name), _signal_handler)

    await shutdown_event.wait()

    log.info("shutting_down", grace_seconds=config.shutdown_grace_seconds)

    # Step 1: flip health to NOT_SERVING for every registered service
    # so the kubelet's gRPC readiness probe stops directing new work
    # here. Any RPC in flight when this fires keeps running — we only
    # stopped accepting NEW connections through the readiness path.
    await health_servicer.set("", health_pb2.HealthCheckResponse.NOT_SERVING)
    for service_name in (
        "sourcebridge.reasoning.v1.ReasoningService",
        "sourcebridge.linking.v1.LinkingService",
        "sourcebridge.requirements.v1.RequirementsService",
        "sourcebridge.knowledge.v1.KnowledgeService",
        "sourcebridge.enterprise.v1.EnterpriseReportService",
        "sourcebridge.contracts.v1.ContractsService",
    ):
        await health_servicer.set(service_name, health_pb2.HealthCheckResponse.NOT_SERVING)
    log.info("worker_drain_started", grace_seconds=config.shutdown_grace_seconds)

    # Step 2: graceful server stop with the configured grace window.
    # Existing RPCs run to completion; new ones get rejected. After
    # min(actual_drain_time, grace_seconds) the call returns.
    await server.stop(grace=config.shutdown_grace_seconds)
    log.info("worker_drain_complete")

    # Step 3: close providers AFTER the server has finished draining.
    # Closing them before server.stop would yank the provider out from
    # under any in-flight RPC.
    if hasattr(embedding_provider, "close"):
        await embedding_provider.close()

    log.info("worker_stopped")


def main() -> None:
    """Synchronous entry point for ``sourcebridge-worker`` console script."""
    asyncio.run(serve())


if __name__ == "__main__":
    main()
