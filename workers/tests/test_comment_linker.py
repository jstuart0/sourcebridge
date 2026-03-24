"""Tests for the comment-based linker."""

from workers.linking.comment import extract_comment_links
from workers.linking.types import CodeEntity


def test_extract_go_comment():
    """Comment linker finds REQ-xxx in Go comments."""
    entity = CodeEntity(
        file_path="go/main.go",
        name="StartServer",
        kind="function",
        start_line=37,
        end_line=44,
        content='// REQ-001: System must start and listen on configured port\nfunc StartServer(cfg Config) error {',
        doc_comment="// REQ-001: System must start and listen on configured port",
        language="go",
    )
    result = extract_comment_links([entity])
    assert len(result.links) == 1
    assert result.links[0].requirement_id == "REQ-001"
    assert result.links[0].entity.name == "StartServer"


def test_extract_python_docstring():
    """Comment linker finds REQ-xxx in Python docstrings."""
    entity = CodeEntity(
        file_path="python/auth.py",
        name="hash_password",
        kind="method",
        start_line=48,
        end_line=60,
        content="",
        doc_comment='"""REQ-010: Passwords must be hashed before storage"""',
        language="python",
    )
    result = extract_comment_links([entity])
    assert len(result.links) == 1
    assert result.links[0].requirement_id == "REQ-010"


def test_extract_jsdoc():
    """Comment linker finds REQ-xxx in JSDoc comments."""
    entity = CodeEntity(
        file_path="typescript/src/api.ts",
        name="createItem",
        kind="function",
        start_line=37,
        end_line=50,
        content="",
        doc_comment="* REQ-003: POST /items creates a new item\n* REQ-004: Validates required fields",
        language="typescript",
    )
    result = extract_comment_links([entity])
    assert len(result.links) == 2
    req_ids = {lnk.requirement_id for lnk in result.links}
    assert req_ids == {"REQ-003", "REQ-004"}


def test_extract_rust_doc():
    """Comment linker finds REQ-xxx in Rust doc comments."""
    entity = CodeEntity(
        file_path="rust/src/lib.rs",
        name="is_valid_identifier",
        kind="function",
        start_line=8,
        end_line=20,
        content="",
        doc_comment="/// REQ-008: Identifier validation rules",
        language="rust",
    )
    result = extract_comment_links([entity])
    assert len(result.links) == 1
    assert result.links[0].requirement_id == "REQ-008"


def test_no_requirements_in_code():
    """Comment linker returns empty for code without requirement references."""
    entity = CodeEntity(
        file_path="main.go",
        name="main",
        kind="function",
        start_line=1,
        end_line=5,
        content="func main() {\n\tfmt.Println(\"hello\")\n}",
        doc_comment="",
        language="go",
    )
    result = extract_comment_links([entity])
    assert len(result.links) == 0


def test_multiple_entities():
    """Comment linker processes multiple entities."""
    entities = [
        CodeEntity(
            file_path="a.go", name="funcA", kind="function",
            start_line=1, end_line=5,
            doc_comment="// REQ-001: first",
            language="go",
        ),
        CodeEntity(
            file_path="b.go", name="funcB", kind="function",
            start_line=1, end_line=5,
            doc_comment="// REQ-002: second",
            language="go",
        ),
        CodeEntity(
            file_path="c.go", name="funcC", kind="function",
            start_line=1, end_line=5,
            doc_comment="no requirement here",
            language="go",
        ),
    ]
    result = extract_comment_links(entities)
    assert len(result.links) == 2


def test_confidence_is_high():
    """Comment links should have high confidence."""
    entity = CodeEntity(
        file_path="a.go", name="func", kind="function",
        start_line=1, end_line=5,
        doc_comment="// REQ-100: test",
        language="go",
    )
    result = extract_comment_links([entity])
    assert result.links[0].confidence >= 0.9


def test_inline_comment_in_content():
    """Comment linker finds REQ in inline comments within function body."""
    entity = CodeEntity(
        file_path="go/payment/processor.go",
        name="ProcessPayment",
        kind="function",
        start_line=29,
        end_line=50,
        content=(
            "func ProcessPayment(ctx context.Context, order Order)"
            " (Receipt, error) {\n\t// REQ-042: Payment processing\n"
            "\t// REQ-017: Transaction logging\n\treturn charge(order)\n}"
        ),
        doc_comment="",
        language="go",
    )
    result = extract_comment_links([entity])
    req_ids = {lnk.requirement_id for lnk in result.links}
    assert "REQ-042" in req_ids
    assert "REQ-017" in req_ids
