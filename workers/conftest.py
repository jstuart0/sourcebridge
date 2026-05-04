import sys
from pathlib import Path

import pytest

# Add gen/python to sys.path so protobuf-generated stubs (common.v1, reasoning.v1, etc.) are importable
_gen_python = str(Path(__file__).resolve().parent.parent / "gen" / "python")
if _gen_python not in sys.path:
    sys.path.insert(0, _gen_python)


class MockServicerContext:
    """Minimal mock for grpc.aio.ServicerContext.

    Shared across all servicer test modules.  Previously duplicated verbatim in
    test_linking_servicer.py, test_requirements_servicer.py, and
    test_reasoning_servicer.py (PY-5 / librarian #4).
    """

    def __init__(self):
        self.code = None
        self.details = None

    async def abort(self, code, details):
        self.code = code
        self.details = details
        raise Exception(f"gRPC abort: {code} {details}")


@pytest.fixture
def llm():
    """FakeLLMProvider instance for servicer unit tests."""
    from workers.common.llm.fake import FakeLLMProvider

    return FakeLLMProvider()


@pytest.fixture
def context():
    """MockServicerContext instance for servicer unit tests."""
    return MockServicerContext()
