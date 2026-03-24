"""Tests for the gold dataset evaluation harness."""

from pathlib import Path

from workers.linking.comment import extract_comment_links
from workers.linking.confidence import score_links
from workers.linking.evaluation import evaluate, evaluate_confidence_calibration, load_gold_dataset
from workers.linking.types import CodeEntity

FIXTURE_DIR = Path(__file__).parent.parent.parent / "tests" / "fixtures" / "multi-lang-repo"
GOLD_PATH = FIXTURE_DIR / "expected" / "links.json"


def _load_entities_from_fixture() -> list[CodeEntity]:
    """Load code entities from fixture source files with their comments."""
    entities: list[CodeEntity] = []

    # Map of files to parse for comment references
    source_files = [
        ("go/main.go", "go"),
        ("go/payment/processor.go", "go"),
        ("python/auth.py", "python"),
        ("typescript/src/api.ts", "typescript"),
        ("typescript/src/utils.ts", "typescript"),
        ("java/src/main/java/com/example/Service.java", "java"),
        ("rust/src/lib.rs", "rust"),
    ]

    for rel_path, language in source_files:
        full_path = FIXTURE_DIR / rel_path
        if not full_path.exists():
            continue

        content = full_path.read_text()
        lines = content.split("\n")

        # Extract entities with their surrounding comments
        # This is a simplified extraction — in production the indexer handles this
        entities.extend(_extract_entities_from_source(rel_path, lines, language))

    return entities


def _extract_entities_from_source(file_path: str, lines: list[str], language: str) -> list[CodeEntity]:
    """Extract code entities from source lines with their comments."""
    entities: list[CodeEntity] = []

    if language == "go":
        entities.extend(_extract_go_entities(file_path, lines))
    elif language == "python":
        entities.extend(_extract_python_entities(file_path, lines))
    elif language == "typescript":
        entities.extend(_extract_ts_entities(file_path, lines))
    elif language == "java":
        entities.extend(_extract_java_entities(file_path, lines))
    elif language == "rust":
        entities.extend(_extract_rust_entities(file_path, lines))

    return entities


def _extract_go_entities(file_path: str, lines: list[str]) -> list[CodeEntity]:
    """Extract Go functions with their preceding comments."""
    entities = []
    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("func "):
            name = stripped.split("(")[0].replace("func ", "").strip()
            if name.startswith("("):  # method receiver
                parts = stripped.split(")")
                if len(parts) >= 2:
                    rest = parts[1].strip()
                    name = rest.split("(")[0].strip()

            # Collect preceding comments
            comments = []
            j = i - 1
            while j >= 0 and lines[j].strip().startswith("//"):
                comments.insert(0, lines[j].strip())
                j -= 1

            # Also collect inline comments within the function body
            body_lines = []
            depth = 0
            for k in range(i, min(i + 50, len(lines))):
                body_lines.append(lines[k])
                depth += lines[k].count("{") - lines[k].count("}")
                if depth <= 0 and k > i:
                    break

            entities.append(CodeEntity(
                file_path=file_path,
                name=name,
                kind="function",
                start_line=i + 1,
                end_line=i + len(body_lines),
                content="\n".join(body_lines),
                doc_comment="\n".join(comments),
                language="go",
            ))
    return entities


def _extract_python_entities(file_path: str, lines: list[str]) -> list[CodeEntity]:
    """Extract Python classes and functions with their docstrings."""
    entities = []

    # Extract module-level docstring
    module_doc = ""
    if lines and lines[0].strip().startswith('"""'):
        doc_lines = [lines[0].strip()]
        if not (lines[0].strip().endswith('"""') and len(lines[0].strip()) > 3):
            for k in range(1, min(20, len(lines))):
                doc_lines.append(lines[k].strip())
                if lines[k].strip().endswith('"""'):
                    break
        module_doc = "\n".join(doc_lines)

    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("class ") or stripped.startswith("def ") or stripped.startswith("async def "):
            if stripped.startswith("class "):
                name = stripped.split("(")[0].split(":")[0].replace("class ", "").strip()
                kind = "class"
            else:
                name_part = stripped.replace("async def ", "").replace("def ", "")
                name = name_part.split("(")[0].strip()
                kind = "function"

            # Skip dunder methods and private helpers for linking
            if name.startswith("_") and name != "__init__":
                continue

            # Collect inline docstring
            docstring = ""
            if i + 1 < len(lines):
                next_line = lines[i + 1].strip()
                if next_line.startswith('"""') or next_line.startswith("'''"):
                    doc_lines = [next_line]
                    not_triple_dq = not (next_line.endswith('"""') and len(next_line) > 3)
                    not_triple_sq = not (next_line.endswith("'''") and len(next_line) > 3)
                    if not_triple_dq and not_triple_sq:
                        for k in range(i + 2, min(i + 20, len(lines))):
                            doc_lines.append(lines[k].strip())
                            if lines[k].strip().endswith('"""') or lines[k].strip().endswith("'''"):
                                break
                    docstring = "\n".join(doc_lines)

            # For the primary service class (not dataclasses), include module-level docstring
            # Heuristic: dataclasses are short and don't have service methods
            if kind == "class" and module_doc:
                # Check if the class has methods (not just a dataclass)
                has_methods = False
                for k in range(i + 1, min(i + 50, len(lines))):
                    if lines[k].strip().startswith("def ") and not lines[k].strip().startswith("def __"):
                        has_methods = True
                        break
                    if lines[k].strip().startswith("class "):
                        break
                if has_methods:
                    docstring = module_doc + "\n" + docstring

            entities.append(CodeEntity(
                file_path=file_path,
                name=name,
                kind=kind,
                start_line=i + 1,
                end_line=i + 20,
                content="",
                doc_comment=docstring,
                language="python",
            ))
    return entities


def _extract_ts_entities(file_path: str, lines: list[str]) -> list[CodeEntity]:
    """Extract TypeScript functions with JSDoc comments."""
    entities = []
    for i, line in enumerate(lines):
        stripped = line.strip()
        # Only match top-level declarations (not indented)
        if line.startswith("  ") or line.startswith("\t"):
            continue

        # Match function/const/export declarations
        is_func = False
        name = ""
        for prefix in ["export function ", "function ", "export const ", "export async function ", "async function "]:
            if stripped.startswith(prefix):
                is_func = True
                rest = stripped[len(prefix):]
                name = rest.split("=")[0].strip() if "=" in rest else rest.split("(")[0].strip()
                break

        if not is_func or not name:
            continue

        # Clean name — remove type parameters, ensure it's just the identifier
        name = name.split("<")[0].strip()

        # Collect preceding JSDoc — only the immediately preceding block
        jsdoc_lines = []
        j = i - 1
        while j >= 0:
            lnk = lines[j].strip()
            if lnk.startswith("*") or lnk.startswith("/**") or lnk.startswith("*/"):
                jsdoc_lines.insert(0, lnk)
                if lnk.startswith("/**"):
                    break
            elif not lnk:
                j -= 1
                continue
            else:
                break
            j -= 1

        entities.append(CodeEntity(
            file_path=file_path,
            name=name,
            kind="function",
            start_line=i + 1,
            end_line=i + 20,
            content="",
            doc_comment="\n".join(jsdoc_lines),
            language="typescript",
        ))
    return entities


def _extract_java_entities(file_path: str, lines: list[str]) -> list[CodeEntity]:
    """Extract Java methods with Javadoc comments (skip class declarations)."""
    entities = []
    for i, line in enumerate(lines):
        stripped = line.strip()

        # Skip class declarations — we link to methods, not classes
        if "class " in stripped:
            continue

        # Method declaration
        is_method = False
        name = ""
        for access in ["public ", "private ", "protected "]:
            if (
                stripped.startswith(access)
                and "(" in stripped
                and (")" in stripped or "{" in stripped)
                and "class " not in stripped
            ):
                before_paren = stripped.split("(")[0]
                parts = before_paren.split()
                if len(parts) >= 2:
                    name = parts[-1]
                    is_method = True
                    break

        if not is_method or not name:
            continue

        # Collect preceding Javadoc
        javadoc_lines = []
        j = i - 1
        while j >= 0:
            lnk = lines[j].strip()
            if lnk.startswith("*") or lnk.startswith("/**") or lnk.startswith("*/"):
                javadoc_lines.insert(0, lnk)
                if lnk.startswith("/**"):
                    break
            elif lnk.startswith("@"):
                javadoc_lines.insert(0, lnk)
            elif not lnk:
                j -= 1
                continue
            else:
                break
            j -= 1

        entities.append(CodeEntity(
            file_path=file_path,
            name=name,
            kind="method",
            start_line=i + 1,
            end_line=i + 20,
            content="",
            doc_comment="\n".join(javadoc_lines),
            language="java",
        ))
    return entities


def _extract_rust_entities(file_path: str, lines: list[str]) -> list[CodeEntity]:
    """Extract Rust functions with doc comments."""
    entities = []

    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("pub fn ") or (stripped.startswith("fn ") and not stripped.startswith("fn main")):
            name = stripped.replace("pub fn ", "").replace("fn ", "").split("(")[0].strip()

            # Collect only immediately preceding /// doc comments
            doc_lines = []
            j = i - 1
            while j >= 0 and lines[j].strip().startswith("///"):
                doc_lines.insert(0, lines[j].strip())
                j -= 1

            entities.append(CodeEntity(
                file_path=file_path,
                name=name,
                kind="function",
                start_line=i + 1,
                end_line=i + 20,
                content="",
                doc_comment="\n".join(doc_lines),
                language="rust",
            ))
    return entities


def test_load_gold_dataset():
    """Gold dataset loads correctly."""
    gold = load_gold_dataset(GOLD_PATH)
    assert len(gold) == 24  # 24 links in expected/links.json


def test_evaluation_precision_recall():
    """Comment linker achieves required precision and recall on gold dataset."""
    gold = load_gold_dataset(GOLD_PATH)
    entities = _load_entities_from_fixture()

    # Run comment linker
    result = extract_comment_links(entities)
    scored = score_links(result.links)

    # Evaluate
    eval_result = evaluate(scored, gold)

    # Print details for debugging
    print(f"\nPrecision: {eval_result.precision:.2%}")
    print(f"Recall: {eval_result.recall:.2%}")
    print(f"F1: {eval_result.f1:.2%}")
    print(f"TP: {eval_result.true_positives}, FP: {eval_result.false_positives}, FN: {eval_result.false_negatives}")
    if eval_result.fn_links:
        print(f"False negatives: {eval_result.fn_links[:5]}")
    if eval_result.fp_links:
        print(f"False positives: {eval_result.fp_links[:5]}")

    # Phase 4 thresholds
    assert eval_result.precision >= 0.75, f"Precision {eval_result.precision:.2%} < 75%"
    assert eval_result.recall >= 0.55, f"Recall {eval_result.recall:.2%} < 55%"


def test_confidence_calibration():
    """High-confidence links (>= 0.9) should be correct >= 80% of the time."""
    gold = load_gold_dataset(GOLD_PATH)
    entities = _load_entities_from_fixture()

    result = extract_comment_links(entities)
    scored = score_links(result.links)

    calibration = evaluate_confidence_calibration(scored, gold, threshold=0.9)
    print(f"\nConfidence calibration at 0.9: {calibration:.2%}")
    assert calibration >= 0.80, f"Calibration {calibration:.2%} < 80%"


def test_false_positive_rate():
    """False positive rate should be < 20%."""
    gold = load_gold_dataset(GOLD_PATH)
    entities = _load_entities_from_fixture()

    result = extract_comment_links(entities)
    scored = score_links(result.links)
    eval_result = evaluate(scored, gold)

    if eval_result.total_predicted > 0:
        fp_rate = eval_result.false_positives / eval_result.total_predicted
        print(f"\nFalse positive rate: {fp_rate:.2%}")
        assert fp_rate < 0.20, f"FP rate {fp_rate:.2%} >= 20%"
