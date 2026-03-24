import { describe, it, expect, vi, afterEach } from "vitest";
import { render, fireEvent, cleanup } from "@testing-library/react";
import { EmptyState } from "./EmptyState";
import { NoRepositories } from "./NoRepositories";
import { NoRequirements } from "./NoRequirements";
import { NoResults } from "./NoResults";

afterEach(cleanup);

describe("EmptyState", () => {
  it("renders title and description", () => {
    const { getByTestId, getByText } = render(
      <EmptyState title="Nothing here" description="Add something to get started." />
    );
    expect(getByTestId("empty-state")).toBeInTheDocument();
    expect(getByText("Nothing here")).toBeInTheDocument();
    expect(getByText("Add something to get started.")).toBeInTheDocument();
  });

  it("renders action button when provided", () => {
    const onClick = vi.fn();
    const { getByTestId } = render(
      <EmptyState title="Empty" description="Desc" action={{ label: "Add Item", onClick }} />
    );
    const button = getByTestId("empty-state-action");
    expect(button).toHaveTextContent("Add Item");
    fireEvent.click(button);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("does not render action button when not provided", () => {
    const { queryByTestId } = render(<EmptyState title="Empty" description="Desc" />);
    expect(queryByTestId("empty-state-action")).not.toBeInTheDocument();
  });

  it("renders icon when provided", () => {
    const { getByTestId } = render(
      <EmptyState title="Empty" description="Desc" icon={<span data-testid="icon">!</span>} />
    );
    expect(getByTestId("icon")).toBeInTheDocument();
  });
});

describe("NoRepositories", () => {
  it("renders empty state for repositories", () => {
    const { getByText } = render(<NoRepositories />);
    expect(getByText("No repositories indexed")).toBeInTheDocument();
  });

  it("renders import action when handler provided", () => {
    const onImport = vi.fn();
    const { getByTestId } = render(<NoRepositories onImport={onImport} />);
    expect(getByTestId("empty-state-action")).toHaveTextContent("Import Repository");
  });
});

describe("NoRequirements", () => {
  it("renders empty state for requirements", () => {
    const { getByText } = render(<NoRequirements />);
    expect(getByText("No requirements found")).toBeInTheDocument();
  });
});

describe("NoResults", () => {
  it("renders with query context", () => {
    const { getByText } = render(<NoResults query="foo" />);
    expect(getByText(/No results match "foo"/)).toBeInTheDocument();
  });

  it("renders generic message without query", () => {
    const { getByText } = render(<NoResults />);
    expect(getByText("No results match your current filters.")).toBeInTheDocument();
  });

  it("renders clear action", () => {
    const onClear = vi.fn();
    const { getByTestId } = render(<NoResults onClear={onClear} />);
    fireEvent.click(getByTestId("empty-state-action"));
    expect(onClear).toHaveBeenCalledTimes(1);
  });
});
