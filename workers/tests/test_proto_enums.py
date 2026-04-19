from knowledge.v1 import knowledge_pb2

from workers.knowledge.proto_enums import proto_enum_fallback_counts, resolve_request_audience, resolve_request_depth


def test_request_enums_override_legacy_strings() -> None:
    request = knowledge_pb2.GenerateCliffNotesRequest(
        audience="beginner",
        depth="summary",
        audience_enum=knowledge_pb2.AUDIENCE_DEVELOPER,
        depth_enum=knowledge_pb2.DEPTH_DEEP,
    )

    assert resolve_request_audience(request) == "developer"
    assert resolve_request_depth(request) == "deep"


def test_request_legacy_strings_fall_back_when_enum_unspecified() -> None:
    before = proto_enum_fallback_counts()
    request = knowledge_pb2.GenerateCliffNotesRequest(
        audience="beginner",
        depth="summary",
    )

    assert resolve_request_audience(request) == "beginner"
    assert resolve_request_depth(request) == "summary"

    after = proto_enum_fallback_counts()
    assert after.get("audience", 0) >= before.get("audience", 0) + 1
    assert after.get("depth", 0) >= before.get("depth", 0) + 1
