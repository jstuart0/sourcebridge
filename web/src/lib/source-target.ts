export interface SourceTarget {
  filePath: string;
  line?: number;
  endLine?: number;
  tab?: "files" | "symbols" | "requirements" | "analysis" | "impact" | "knowledge" | "settings";
}

export function buildRepositorySourceHref(
  repositoryId: string,
  target: SourceTarget,
  extras?: Record<string, string | number | undefined>
) {
  const params = new URLSearchParams();
  params.set("tab", target.tab ?? "files");
  params.set("file", target.filePath);
  if (typeof target.line === "number" && target.line > 0) {
    params.set("line", String(target.line));
  }
  if (typeof target.endLine === "number" && target.endLine > 0) {
    params.set("endLine", String(target.endLine));
  }
  if (extras) {
    for (const [key, value] of Object.entries(extras)) {
      if (value !== undefined && value !== "") {
        params.set(key, String(value));
      }
    }
  }
  return `/repositories/${repositoryId}?${params.toString()}`;
}

export function parsePositiveInt(value: string | null): number | undefined {
  if (!value) return undefined;
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}

export function sourceTargetFromSearchParams(params: URLSearchParams): SourceTarget | null {
  const filePath = params.get("file");
  if (!filePath) return null;
  return {
    filePath,
    line: parsePositiveInt(params.get("line")),
    endLine: parsePositiveInt(params.get("endLine")),
    tab: (params.get("tab") as SourceTarget["tab"]) ?? "files",
  };
}
