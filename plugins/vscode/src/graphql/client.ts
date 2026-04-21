import * as vscode from "vscode";
import {
  DISCUSS_CODE,
  REVIEW_CODE,
  REPOSITORIES,
  REQUIREMENT,
  REQUIREMENT_TO_CODE,
  REQUIREMENT_LINKS,
  CODE_TO_REQUIREMENTS,
  VERIFY_LINK,
  FEATURES,
  IDE_CAPABILITIES,
  EXTENSION_CAPABILITIES,
  KNOWLEDGE_ARTIFACTS,
  KNOWLEDGE_ARTIFACT,
  KNOWLEDGE_SCOPE_CHILDREN,
  GENERATE_CLIFF_NOTES,
  GENERATE_LEARNING_PATH,
  GENERATE_CODE_TOUR,
  EXPLAIN_SYSTEM,
  ADD_REPOSITORY,
  SYMBOLS_FOR_FILE,
  LATEST_IMPACT_REPORT,
  CREATE_REQUIREMENT,
  UPDATE_REQUIREMENT_FIELDS,
  CREATE_MANUAL_LINK,
  MOVE_TO_TRASH,
} from "./queries";
import { SessionCache } from "../state/sessionCache";
import { DocCache } from "../state/cache";
import { graphqlRequest, requestJSON, requestText, TransportError } from "./transport";
import type { CancellationToken } from "vscode";
import * as log from "../logging";

export interface GraphQLResponse<T> {
  data?: T;
  errors?: Array<{ message: string; extensions?: Record<string, unknown> }>;
}

export interface Repository {
  id: string;
  name: string;
  path: string;
  status: string;
  hasAuth: boolean;
  fileCount: number;
  functionCount: number;
}

export interface Requirement {
  id: string;
  externalId?: string | null;
  title: string;
  description: string;
  source: string;
  priority?: string | null;
  tags: string[];
}

/** Create input for the createRequirement mutation. */
export interface CreateRequirementInput {
  repositoryId: string;
  externalId?: string | null;
  title: string;
  description?: string | null;
  priority?: string | null;
  source?: string | null;
  tags?: string[] | null;
}

/** Patch input for the updateRequirementFields mutation. */
export interface UpdateRequirementFieldsInput {
  id: string;
  externalId?: string | null;
  title?: string | null;
  description?: string | null;
  priority?: string | null;
  source?: string | null;
  tags?: string[] | null;
  acceptanceCriteria?: string[] | null;
}

export interface RequirementLink {
  id: string;
  requirementId: string;
  symbolId: string;
  confidence: string;
  rationale?: string | null;
  verified: boolean;
  requirement?: Pick<Requirement, "id" | "externalId" | "title"> | null;
  symbol?: Pick<SymbolNode, "id" | "name" | "filePath" | "startLine" | "endLine"> | null;
}

export interface DiscussCodeResponse {
  discussCode: {
    answer: string;
    references: string[];
    relatedRequirements: string[];
    model: string;
    inputTokens: number;
    outputTokens: number;
  };
}

export interface ReviewFindingResponse {
  category: string;
  severity: string;
  message: string;
  filePath: string;
  startLine: number;
  endLine: number;
  suggestion: string;
}

export interface ReviewCodeResponse {
  reviewCode: {
    template: string;
    findings: ReviewFindingResponse[];
    score: number;
    model: string;
    inputTokens: number;
    outputTokens: number;
  };
}

export interface FeatureFlags {
  cliffNotes: boolean;
  learningPaths: boolean;
  codeTours: boolean;
  systemExplain: boolean;
}

export interface ExtensionCapabilities {
  repoKnowledge: boolean;
  scopedKnowledge: boolean;
  scopedExplain: boolean;
  impactReports: boolean;
  discussCode?: boolean;
  reviewCode?: boolean;
  vscode?: boolean;
  jetbrains?: boolean;
}

export type ScopeType = "repository" | "module" | "file" | "symbol" | "requirement";

export interface KnowledgeScope {
  scopeType: string;
  scopePath: string;
  modulePath?: string | null;
  filePath?: string | null;
  symbolName?: string | null;
}

export interface ScopeChild {
  scopeType: string;
  label: string;
  scopePath: string;
  hasArtifact: boolean;
  summary?: string | null;
}

export interface KnowledgeEvidence {
  id: string;
  sourceType: string;
  filePath: string;
  lineStart: number;
  lineEnd: number;
  rationale: string;
}

export interface KnowledgeSection {
  id: string;
  title: string;
  content: string;
  summary: string;
  confidence: string;
  inferred: boolean;
  orderIndex: number;
  evidence: KnowledgeEvidence[];
}

export interface KnowledgeArtifact {
  id: string;
  repositoryId: string;
  type: string;
  audience: string;
  depth: string;
  scope: KnowledgeScope;
  status: string;
  progress: number;
  stale: boolean;
  generatedAt: string;
  sections: KnowledgeSection[];
}

export interface SymbolNode {
  id: string;
  name: string;
  qualifiedName: string;
  kind: string;
  language: string;
  filePath: string;
  startLine: number;
  endLine: number;
  signature?: string | null;
  docComment?: string | null;
}

export interface ImpactReport {
  id: string;
  oldCommitSha?: string | null;
  newCommitSha?: string | null;
  staleArtifacts: string[];
  computedAt: string;
  filesChanged: Array<{ path: string; status: string; additions: number; deletions: number }>;
  affectedRequirements: Array<{
    requirementId: string;
    externalId: string;
    title: string;
    affectedLinks: number;
    totalLinks: number;
  }>;
}

export interface ExplainSystemResponse {
  explainSystem: {
    explanation: string;
    model: string;
    inputTokens: number;
    outputTokens: number;
  };
}

export interface DesktopAuthInfo {
  local_auth: boolean;
  setup_done: boolean;
  oidc_enabled: boolean;
}

export interface DesktopOIDCStart {
  session_id: string;
  auth_url: string;
  expires_in: number;
}

export interface DesktopAuthPoll {
  status: "pending" | "complete";
  token?: string;
  expires_in?: number;
}

/**
 * Operation names whose body calls an LLM and therefore can take tens
 * of seconds. Keep in sync with the server's worker-side timeouts
 * (internal/worker/client.go constants) — those cap at 120s for
 * discuss / review, up to 3600s for knowledge generation scoped at
 * repo level. We use 180s as a middle ground: generous enough to
 * cover any first-token + full answer round trip on a local 32b
 * model, strict enough that a truly stuck request still fails.
 */
const LLM_OPERATIONS = new Set([
  "DiscussCode",
  "ReviewCode",
  "ExplainSystem",
  "GenerateCliffNotes",
  "GenerateLearningPath",
  "GenerateCodeTour",
  "GenerateArchitectureDiagram",
  "GenerateWorkflowStory",
  "EnrichRequirement",
  "AnalyzeSymbol",
  "AutoLinkRequirements",
  "TriggerSpecExtraction",
]);

const LLM_TIMEOUT_MS = 180_000;

function defaultTimeoutForOperation(operationName: string): number | undefined {
  return LLM_OPERATIONS.has(operationName) ? LLM_TIMEOUT_MS : undefined;
}

export class SourceBridgeClient {
  private apiUrl: string;
  private token: string;
  private readonly context?: vscode.ExtensionContext;
  private authLoaded?: Promise<void>;
  private featureCache = new SessionCache<FeatureFlags>();
  private capabilityCache = new SessionCache<ExtensionCapabilities>();
  private repositoryCache = new SessionCache<Repository[]>();
  /**
   * Per-symbol link cache. Providers fetch the same symbol's links on
   * every CodeLens refresh; without TTL-bounded caching the extension
   * fans out N network calls per file. Two-minute TTL balances
   * "responsive after link updates" against "don't thrash the server."
   */
  private symbolLinkCache = new DocCache<RequirementLink[]>({ ttlMs: 2 * 60_000, max: 500 });
  private symbolCache = new SessionCache<SymbolNode[]>();

  constructor(context?: vscode.ExtensionContext) {
    this.context = context;
    const config = vscode.workspace.getConfiguration("sourcebridge");
    this.apiUrl = config.get("apiUrl", "http://localhost:8080");
    this.token = "";
    log.info("client", `Initialized with apiUrl=${this.apiUrl}`);
  }

  get graphqlUrl(): string {
    return `${this.apiUrl}/api/v1/graphql`;
  }

  get baseUrl(): string {
    return this.apiUrl;
  }

  /**
   * Execute a GraphQL query through the hardened transport.
   *
   * Every call path gets a 10 s timeout, AbortSignal cancellation from
   * a VS Code CancellationToken when provided, and exponential-backoff
   * retries on 5xx + network errors. 4xx responses short-circuit
   * immediately — they're almost always config issues that retrying
   * would just thrash.
   */
  async query<T>(
    queryString: string,
    variables?: Record<string, unknown>,
    opts?: { token?: CancellationToken; timeoutMs?: number },
  ): Promise<T> {
    await this.ensureAuthLoaded();
    const operationMatch = queryString.match(/(?:query|mutation)\s+(\w+)/);
    const operationName = operationMatch ? operationMatch[1] : "anonymous";
    const hasAuth = !!this.token;

    log.debug("client", `GraphQL ${operationName} → ${this.graphqlUrl} (auth=${hasAuth})`);
    if (variables) {
      log.debug("client", `  variables: ${JSON.stringify(variables)}`);
    }

    const headers: Record<string, string> = { "X-Client-Type": "vscode" };
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;

    // LLM-backed operations routinely run 30–90s (first-token latency
    // on a 32b local model is ~5–15s alone). The shared 10s transport
    // default aborts them prematurely and the server then sees its
    // client-context cancelled, surfacing as "rpc error" to the user.
    // Give these mutations the same 120s ceiling the Go API allows.
    const effectiveTimeout =
      opts?.timeoutMs ?? defaultTimeoutForOperation(operationName);

    try {
      const data = await graphqlRequest<T>(this.graphqlUrl, queryString, variables, {
        headers,
        token: opts?.token,
        timeoutMs: effectiveTimeout,
      });
      log.debug("client", `GraphQL ${operationName} OK`);
      return data;
    } catch (err) {
      if (err instanceof TransportError) {
        log.error("client", `GraphQL ${operationName} failed (${err.kind}): ${err.message}`);
      } else {
        log.error("client", `GraphQL ${operationName} network error`, err);
      }
      throw err;
    }
  }

  async isServerRunning(opts?: { token?: CancellationToken }): Promise<boolean> {
    // Probe liveness (`/healthz`), not strict readiness (`/readyz`).
    //
    // A degraded `/readyz` — e.g. the worker gRPC is briefly down or
    // a background check is flaky — shouldn't hide the entire
    // extension, because the GraphQL + MCP surfaces still work for
    // most flows. Individual calls will still raise actionable
    // errors if they do depend on the degraded subsystem.
    await this.ensureAuthLoaded();
    for (const path of ["/healthz", "/readyz"]) {
      try {
        log.debug("client", `Health check → ${this.apiUrl}${path}`);
        await requestText({
          url: `${this.apiUrl}${path}`,
          method: "GET",
          opts: { timeoutMs: 3_000, maxRetries: 0, token: opts?.token },
        });
        log.info("client", `Server reachable at ${this.apiUrl}${path}`);
        return true;
      } catch (err) {
        log.debug(
          "client",
          `${path} probe failed: ${err instanceof Error ? err.message : err}`,
        );
      }
    }
    log.error("client", `Server unreachable at ${this.apiUrl} (both /healthz and /readyz failed)`);
    return false;
  }

  async getDesktopAuthInfo(): Promise<DesktopAuthInfo> {
    log.debug("client", `Fetching desktop auth info from ${this.apiUrl}/auth/desktop/info`);
    try {
      return await requestJSON<DesktopAuthInfo>({
        url: `${this.apiUrl}/auth/desktop/info`,
        method: "GET",
      });
    } catch (err) {
      log.error("client", `Desktop auth info failed`, err);
      throw err instanceof Error ? err : new Error(String(err));
    }
  }

  async desktopLocalLogin(password: string, tokenName = "VS Code"): Promise<string> {
    log.info("client", `Attempting local login as "${tokenName}"`);
    const data = await requestJSON<{ token?: string; error?: string }>({
      url: `${this.apiUrl}/auth/desktop/local-login`,
      method: "POST",
      body: JSON.stringify({ password, token_name: tokenName }),
      contentType: "application/json",
    });
    if (!data.token) {
      log.error("client", `Local login failed: ${data.error ?? "no token in response"}`);
      throw new Error(data.error || "login failed");
    }
    log.info("client", `Local login successful, received token (${data.token.length} chars)`);
    return data.token;
  }

  async startDesktopOIDC(): Promise<DesktopOIDCStart> {
    log.info("client", "Starting OIDC desktop auth flow");
    const data = await requestJSON<DesktopOIDCStart & { error?: string }>({
      url: `${this.apiUrl}/auth/desktop/oidc/start`,
      method: "POST",
    });
    log.info("client", `OIDC session started: ${data.session_id}, expires_in=${data.expires_in}s`);
    return data;
  }

  async pollDesktopOIDC(sessionId: string): Promise<DesktopAuthPoll> {
    log.debug("client", `OIDC poll for session ${sessionId}`);
    const data = await requestJSON<DesktopAuthPoll & { error?: string }>({
      url: `${this.apiUrl}/auth/desktop/oidc/poll?session_id=${encodeURIComponent(sessionId)}`,
      method: "GET",
    });
    log.debug("client", `OIDC poll status: ${data.status}`);
    return data;
  }

  async revokeCurrentToken(): Promise<void> {
    await this.ensureAuthLoaded();
    if (!this.token) {
      log.debug("client", "No token to revoke");
      return;
    }
    log.info("client", "Revoking current token");
    try {
      await requestText({
        url: `${this.apiUrl}/api/v1/tokens/current/revoke`,
        method: "POST",
        opts: { headers: { Authorization: `Bearer ${this.token}` } },
      });
      log.info("client", "Token revoked successfully");
    } catch (err) {
      // A 400 on revoke means the token was already gone — acceptable.
      if (err instanceof TransportError && err.status === 400) {
        log.info("client", "Token already revoked");
        return;
      }
      log.error("client", "Token revocation failed", err);
      throw err;
    }
  }

  async getRepositories(): Promise<Repository[]> {
    const cached = this.repositoryCache.get();
    if (cached) return cached;
    const data = await this.query<{ repositories: Repository[] }>(REPOSITORIES);
    return this.repositoryCache.set(data.repositories);
  }

  async discussCode(
    repositoryId: string,
    question: string,
    filePath?: string,
    code?: string,
    language?: string,
    requirementId?: string,
    artifactId?: string,
  ): Promise<DiscussCodeResponse["discussCode"]> {
    const input: Record<string, unknown> = {
      repositoryId,
      question,
    };
    if (filePath) {
      input.filePath = filePath;
    }
    if (code) {
      input.code = code;
    }
    if (language) {
      input.language = toGraphQLLanguage(language);
    }
    if (requirementId) {
      input.requirementId = requirementId;
    }
    if (artifactId) {
      input.artifactId = artifactId;
    }

    const data = await this.query<DiscussCodeResponse>(DISCUSS_CODE, { input });
    return data.discussCode;
  }

  async reviewCode(
    repositoryId: string,
    filePath: string,
    template: string,
    code?: string,
    language?: string
  ): Promise<ReviewCodeResponse["reviewCode"]> {
    const input: Record<string, unknown> = {
      repositoryId,
      filePath,
      template,
    };
    if (code) {
      input.code = code;
    }
    if (language) {
      input.language = toGraphQLLanguage(language);
    }

    const data = await this.query<ReviewCodeResponse>(REVIEW_CODE, { input });
    return data.reviewCode;
  }

  async addRepository(name: string, path: string, token?: string): Promise<Repository> {
    const input: Record<string, unknown> = { name, path };
    if (token) input.token = token;
    const data = await this.query<{ addRepository: Repository }>(ADD_REPOSITORY, { input });
    return data.addRepository;
  }

  async getFeatures(): Promise<FeatureFlags> {
    const cached = this.featureCache.get();
    if (cached) return cached;
    const data = await this.query<{ features: FeatureFlags }>(FEATURES);
    return this.featureCache.set(data.features);
  }

  async getCapabilities(): Promise<ExtensionCapabilities> {
    const cached = this.capabilityCache.get();
    if (cached) {
      log.debug("client", "Capabilities served from cache");
      return cached;
    }

    try {
      log.debug("client", "Fetching ideCapabilities from server");
      const data = await this.query<{ ideCapabilities: ExtensionCapabilities }>(IDE_CAPABILITIES);
      log.info("client", `Capabilities: ${JSON.stringify(data.ideCapabilities)}`);
      return this.capabilityCache.set(data.ideCapabilities);
    } catch (capErr) {
      log.warn("client", `ideCapabilities query failed, falling back to introspection: ${capErr instanceof Error ? capErr.message : capErr}`);
      const features = await this.getFeatures();
      try {
        const data = await this.query<{
          queryType?: { fields?: Array<{ name: string }> | null } | null;
          mutationType?: { fields?: Array<{ name: string }> | null } | null;
          explainSystemInput?: { inputFields?: Array<{ name: string }> | null } | null;
          generateCliffNotesInput?: { inputFields?: Array<{ name: string }> | null } | null;
        }>(EXTENSION_CAPABILITIES);
        const queryFields = new Set((data.queryType?.fields || []).map((f) => f.name));
        const mutationFields = new Set((data.mutationType?.fields || []).map((f) => f.name));
        const explainFields = new Set((data.explainSystemInput?.inputFields || []).map((f) => f.name));
        const cliffFields = new Set((data.generateCliffNotesInput?.inputFields || []).map((f) => f.name));
        return this.capabilityCache.set({
          repoKnowledge: !!(features.cliffNotes || features.learningPaths || features.codeTours || features.systemExplain),
          scopedKnowledge:
            queryFields.has("knowledgeScopeChildren") &&
            cliffFields.has("scopeType") &&
            cliffFields.has("scopePath") &&
            mutationFields.has("generateCliffNotes"),
          scopedExplain:
            explainFields.has("scopeType") &&
            explainFields.has("scopePath") &&
            mutationFields.has("explainSystem"),
          impactReports: queryFields.has("latestImpactReport"),
          discussCode: mutationFields.has("discussCode"),
          reviewCode: mutationFields.has("reviewCode"),
          vscode: true,
          jetbrains: false,
        });
      } catch (introErr) {
        log.warn("client", `Schema introspection also failed, using hardcoded defaults: ${introErr instanceof Error ? introErr.message : introErr}`);
        return this.capabilityCache.set({
          repoKnowledge: !!(features.cliffNotes || features.learningPaths || features.codeTours || features.systemExplain),
          scopedKnowledge: false,
          scopedExplain: false,
          impactReports: false,
          discussCode: false,
          reviewCode: false,
          vscode: true,
          jetbrains: false,
        });
      }
    }
  }

  async getSymbolsForFile(repositoryId: string, filePath: string): Promise<SymbolNode[]> {
    const cacheKey = `${repositoryId}:${filePath}`;
    const cached = this.symbolCache.getKey(cacheKey);
    if (cached) {
      return cached;
    }
    const data = await this.query<{ symbols: { nodes: SymbolNode[] } }>(SYMBOLS_FOR_FILE, {
      repositoryId,
      filePath,
    });
    return this.symbolCache.setKey(cacheKey, data.symbols.nodes);
  }

  async getRequirement(id: string): Promise<Requirement | null> {
    const data = await this.query<{ requirement: Requirement | null }>(REQUIREMENT, { id });
    return data.requirement;
  }

  async getCodeToRequirements(
    symbolId: string,
    opts?: { token?: CancellationToken; forceRefresh?: boolean },
  ): Promise<RequirementLink[]> {
    if (opts?.forceRefresh) this.symbolLinkCache.invalidate(symbolId);
    return this.symbolLinkCache.getOrFetch(symbolId, 1, async () => {
      const data = await this.query<{ codeToRequirements: RequirementLink[] }>(
        CODE_TO_REQUIREMENTS,
        { symbolId },
        { token: opts?.token },
      );
      return data.codeToRequirements;
    });
  }

  /** Wipe the per-symbol link cache. Called on save / repo switch / reconnect. */
  invalidateSymbolLinks(): void {
    this.symbolLinkCache.clear();
  }

  async getRequirementToCode(requirementId: string): Promise<RequirementLink[]> {
    const data = await this.query<{ requirementToCode: RequirementLink[] }>(REQUIREMENT_TO_CODE, {
      requirementId,
    });
    return data.requirementToCode;
  }

  async getRequirementLinks(requirementId: string, limit?: number, offset?: number): Promise<RequirementLink[]> {
    const data = await this.query<{ requirementLinks: RequirementLink[] }>(REQUIREMENT_LINKS, {
      requirementId,
      limit,
      offset,
    });
    return data.requirementLinks;
  }

  async verifyLink(linkId: string, verified: boolean): Promise<RequirementLink> {
    const data = await this.query<{ verifyLink: RequirementLink }>(VERIFY_LINK, {
      linkId,
      verified,
    });
    return data.verifyLink;
  }

  /** Create a new requirement (Phase 2 / plan B1). */
  async createRequirement(input: CreateRequirementInput): Promise<Requirement> {
    const data = await this.query<{ createRequirement: Requirement }>(CREATE_REQUIREMENT, {
      input,
    });
    return data.createRequirement;
  }

  /** Patch fields on an existing requirement (Phase 2 / plan B2). */
  async updateRequirementFields(input: UpdateRequirementFieldsInput): Promise<Requirement> {
    const data = await this.query<{ updateRequirementFields: Requirement }>(
      UPDATE_REQUIREMENT_FIELDS,
      { input },
    );
    return data.updateRequirementFields;
  }

  /**
   * Manually link a symbol to a requirement (confidence 1.0, verified).
   * Drives the "Link to existing requirement…" code action and the
   * drag-to-link flow on the requirements tree.
   */
  async createManualLink(input: {
    repositoryId: string;
    requirementId: string;
    symbolId: string;
    rationale?: string;
  }): Promise<RequirementLink> {
    const data = await this.query<{ createManualLink: RequirementLink }>(
      CREATE_MANUAL_LINK,
      { input },
    );
    return data.createManualLink;
  }

  /**
   * Move a requirement, requirement-link, or knowledge-artifact to the
   * soft-delete recycle bin. Because the backend subsumed the
   * dedicated delete-link mutation under moveToTrash, this is the
   * single delete entry point for the VSCode destructive flows.
   */
  async moveToTrash(
    type: "REQUIREMENT" | "REQUIREMENT_LINK" | "KNOWLEDGE_ARTIFACT",
    id: string,
    reason?: string,
  ): Promise<{ id: string; label: string; trashBatchId: string }> {
    const data = await this.query<{
      moveToTrash: { id: string; label: string; trashBatchId: string };
    }>(MOVE_TO_TRASH, { type, id, reason });
    return data.moveToTrash;
  }

  async getKnowledgeArtifacts(
    repositoryId: string,
    scopeType?: string,
    scopePath?: string
  ): Promise<KnowledgeArtifact[]> {
    const data = await this.query<{ knowledgeArtifacts: KnowledgeArtifact[] }>(
      KNOWLEDGE_ARTIFACTS,
      { repositoryId, scopeType, scopePath }
    );
    return data.knowledgeArtifacts;
  }

  async getKnowledgeArtifact(id: string): Promise<KnowledgeArtifact | null> {
    const data = await this.query<{ knowledgeArtifact: KnowledgeArtifact | null }>(KNOWLEDGE_ARTIFACT, { id });
    return data.knowledgeArtifact;
  }

  async getKnowledgeScopeChildren(
    repositoryId: string,
    scopeType: string,
    scopePath: string,
    audience = "DEVELOPER",
    depth = "MEDIUM"
  ): Promise<ScopeChild[]> {
    const data = await this.query<{ knowledgeScopeChildren: ScopeChild[] }>(KNOWLEDGE_SCOPE_CHILDREN, {
      repositoryId,
      scopeType,
      scopePath,
      audience,
      depth,
    });
    return data.knowledgeScopeChildren;
  }

  async generateCliffNotes(
    repositoryId: string,
    audience?: string,
    depth?: string,
    scopeType?: string,
    scopePath?: string
  ): Promise<KnowledgeArtifact> {
    const input: Record<string, unknown> = { repositoryId };
    if (audience) input.audience = audience;
    if (depth) input.depth = depth;
    if (scopeType) input.scopeType = scopeType;
    if (scopePath) input.scopePath = scopePath;

    const data = await this.query<{ generateCliffNotes: KnowledgeArtifact }>(
      GENERATE_CLIFF_NOTES,
      { input }
    );
    return data.generateCliffNotes;
  }

  async generateLearningPath(
    repositoryId: string,
    audience?: string,
    depth?: string
  ): Promise<KnowledgeArtifact> {
    const input: Record<string, unknown> = { repositoryId };
    if (audience) input.audience = audience;
    if (depth) input.depth = depth;

    const data = await this.query<{ generateLearningPath: KnowledgeArtifact }>(
      GENERATE_LEARNING_PATH,
      { input }
    );
    return data.generateLearningPath;
  }

  async generateCodeTour(
    repositoryId: string,
    audience?: string,
    depth?: string
  ): Promise<KnowledgeArtifact> {
    const input: Record<string, unknown> = { repositoryId };
    if (audience) input.audience = audience;
    if (depth) input.depth = depth;

    const data = await this.query<{ generateCodeTour: KnowledgeArtifact }>(
      GENERATE_CODE_TOUR,
      { input }
    );
    return data.generateCodeTour;
  }

  async explainSystem(
    repositoryId: string,
    question: string,
    audience?: string,
    scopeType?: string,
    scopePath?: string
  ): Promise<ExplainSystemResponse["explainSystem"]> {
    const input: Record<string, unknown> = { repositoryId, question };
    if (audience) input.audience = audience;
    if (scopeType) input.scopeType = scopeType;
    if (scopePath) input.scopePath = scopePath;
    const data = await this.query<ExplainSystemResponse>(EXPLAIN_SYSTEM, { input });
    return data.explainSystem;
  }

  async getLatestImpactReport(repositoryId: string): Promise<ImpactReport | null> {
    const data = await this.query<{ latestImpactReport: ImpactReport | null }>(LATEST_IMPACT_REPORT, {
      repositoryId,
    });
    return data.latestImpactReport;
  }

  clearCaches(): void {
    this.featureCache.clear();
    this.capabilityCache.clear();
    this.repositoryCache.clear();
    this.symbolCache.clear();
  }

  async reloadConfiguration(): Promise<void> {
    const config = vscode.workspace.getConfiguration("sourcebridge");
    const oldUrl = this.apiUrl;
    this.apiUrl = config.get("apiUrl", "http://localhost:8080");
    this.token = "";
    this.authLoaded = undefined;
    log.info("client", `Configuration reloaded: apiUrl=${this.apiUrl}${oldUrl !== this.apiUrl ? ` (was ${oldUrl})` : ""}`);
    await this.ensureAuthLoaded();
    this.clearCaches();
  }

  async storeToken(token: string): Promise<void> {
    log.info("client", `Storing token (${token.length} chars, prefix=${token.slice(0, 6)}...)`);
    const config = vscode.workspace.getConfiguration("sourcebridge");
    if (this.context) {
      await this.context.secrets.store("sourcebridge.token", token);
      log.debug("client", "Token written to secret storage");
    } else {
      log.warn("client", "No extension context — token stored in memory only");
    }
    await this.clearLegacyToken(config);
    this.token = token;
    this.authLoaded = Promise.resolve();
    this.clearCaches();
  }

  async clearStoredToken(): Promise<void> {
    log.info("client", "Clearing stored token");
    const config = vscode.workspace.getConfiguration("sourcebridge");
    if (this.context) {
      await this.context.secrets.delete("sourcebridge.token");
    }
    await this.clearLegacyToken(config);
    this.token = "";
    this.authLoaded = Promise.resolve();
    this.clearCaches();
  }

  private async ensureAuthLoaded(): Promise<void> {
    if (!this.authLoaded) {
      this.authLoaded = this.loadAuth();
    }
    await this.authLoaded;
  }

  private async loadAuth(): Promise<void> {
    log.debug("client", "Loading auth credentials...");
    const config = vscode.workspace.getConfiguration("sourcebridge");
    const legacyToken = config.get<string>("token", "");
    if (!this.context) {
      this.token = legacyToken;
      log.info("client", `Auth loaded (no context): token=${this.token ? "present" : "empty"}`);
      return;
    }

    const storedToken = await this.context.secrets.get("sourcebridge.token");
    if (storedToken) {
      this.token = storedToken;
      log.info("client", `Auth loaded from secret storage (${storedToken.length} chars, prefix=${storedToken.slice(0, 6)}...)`);
      if (legacyToken) {
        log.info("client", "Clearing legacy token from settings");
        await this.clearLegacyToken(config);
      }
      return;
    }

    if (legacyToken) {
      log.info("client", "Migrating legacy token to secret storage");
      this.token = legacyToken;
      await this.context.secrets.store("sourcebridge.token", legacyToken);
      await this.clearLegacyToken(config);
      return;
    }

    this.token = "";
    log.warn("client", "No auth token found — requests will be unauthenticated");
  }

  private async clearLegacyToken(config: vscode.WorkspaceConfiguration): Promise<void> {
    await config.update("token", undefined, vscode.ConfigurationTarget.Workspace);
    await config.update("token", undefined, vscode.ConfigurationTarget.Global);
  }
}

function toGraphQLLanguage(language?: string): string | undefined {
  if (!language) {
    return undefined;
  }
  switch (language.toLowerCase()) {
    case "go":
      return "GO";
    case "python":
      return "PYTHON";
    case "typescript":
    case "typescriptreact":
      return "TYPESCRIPT";
    case "javascript":
    case "javascriptreact":
      return "JAVASCRIPT";
    case "java":
      return "JAVA";
    case "rust":
      return "RUST";
    case "csharp":
      return "CSHARP";
    case "cpp":
    case "c":
      return "CPP";
    case "ruby":
      return "RUBY";
    case "php":
      return "PHP";
    default:
      return "UNKNOWN";
  }
}
