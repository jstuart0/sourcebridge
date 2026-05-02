"""gRPC VersionService implementation.

Hosts ``sourcebridge.common.v1.VersionService.GetVersion``, which the API
server calls (with a 250ms timeout, cached for 30s) to populate the
``workerVersion`` field of ``GET /api/v1/version``.

Version metadata is read from ``workers.__version__`` etc., which is
resolved at process boot via ``workers.__init__._resolve_version()``.
"""

from __future__ import annotations

import grpc
from common.v1 import version_pb2, version_pb2_grpc

from workers import __build_date__, __commit__, __version__


class VersionServicer(version_pb2_grpc.VersionServiceServicer):
    """gRPC handler for ``VersionService.GetVersion``."""

    async def GetVersion(  # noqa: N802 — proto contract uses PascalCase
        self,
        request: version_pb2.GetVersionRequest,
        context: grpc.aio.ServicerContext,
    ) -> version_pb2.VersionInfo:
        return version_pb2.VersionInfo(
            version=__version__,
            commit=__commit__,
            build_date=__build_date__,
        )
