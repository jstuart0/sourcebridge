import * as vscode from "vscode";
import {
  DISCUSS_CODE,
  REVIEW_CODE,
  REPOSITORIES,
  REQUIREMENT,
  REQUIREMENT_TO_CODE,
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
} from "./queries";
import { SessionCache } from "../state/sessionCache";

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

export type ScopeType = "repository" | "module" | "file" | "symbol";

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

export class SourceBridgeClient {
  private apiUrl: string;
  private token: string;
  private readonly context?: vscode.ExtensionContext;
  private authLoaded?: Promise<void>;
  private featureCache = new SessionCache<FeatureFlags>();
  private capabilityCache = new SessionCache<ExtensionCapabilities>();
  private repositoryCache = new SessionCache<Repository[]>();
  private symbolCache = new SessionCache<SymbolNode[]>();

  constructor(context?: vscode.ExtensionContext) {
    this.context = context;
    const config = vscode.workspace.getConfiguration("sourcebridge");
    this.apiUrl = config.get("apiUrl", "http://localhost:8080");
    this.token = "";
  }

  get graphqlUrl(): string {
    return `${this.apiUrl}/api/v1/graphql`;
  }

  get baseUrl(): string {
    return this.apiUrl;
  }

  async query<T>(queryString: string, variables?: Record<string, unknown>): Promise<T> {
    await this.ensureAuthLoaded();
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };
    if (this.token) {
      headers["Authorization"] = `Bearer ${this.token}`;
    }

    const response = await fetch(this.graphqlUrl, {
      method: "POST",
      headers,
      body: JSON.stringify({ query: queryString, variables }),
    });

    if (!response.ok) {
      throw new Error(`GraphQL request failed: ${response.status} ${response.statusText}`);
    }

    const result = (await response.json()) as GraphQLResponse<T>;
    if (result.errors?.length) {
      throw new Error(`GraphQL errors: ${result.errors.map((e) => e.message).join(", ")}`);
    }
    if (!result.data) {
      throw new Error("No data returned from GraphQL");
    }
    return result.data;
  }

  async isServerRunning(): Promise<boolean> {
    try {
      await this.ensureAuthLoaded();
      const response = await fetch(`${this.apiUrl}/readyz`, {
        method: "GET",
        signal: AbortSignal.timeout(3000),
      });
      return response.ok;
    } catch {
      return false;
    }
  }

  async getDesktopAuthInfo(): Promise<DesktopAuthInfo> {
    const response = await fetch(`${this.apiUrl}/auth/desktop/info`);
    if (!response.ok) {
      throw new Error(`auth info failed: ${response.status}`);
    }
    return (await response.json()) as DesktopAuthInfo;
  }

  async desktopLocalLogin(password: string, tokenName = "VS Code"): Promise<string> {
    const response = await fetch(`${this.apiUrl}/auth/desktop/local-login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password, token_name: tokenName }),
    });
    const data = (await response.json()) as { token?: string; error?: string };
    if (!response.ok || !data.token) {
      throw new Error(data.error || `login failed: ${response.status}`);
    }
    return data.token;
  }

  async startDesktopOIDC(): Promise<DesktopOIDCStart> {
    const response = await fetch(`${this.apiUrl}/auth/desktop/oidc/start`, {
      method: "POST",
    });
    const data = (await response.json()) as DesktopOIDCStart & { error?: string };
    if (!response.ok) {
      throw new Error(data.error || `oidc start failed: ${response.status}`);
    }
    return data;
  }

  async pollDesktopOIDC(sessionId: string): Promise<DesktopAuthPoll> {
    const response = await fetch(
      `${this.apiUrl}/auth/desktop/oidc/poll?session_id=${encodeURIComponent(sessionId)}`
    );
    const data = (await response.json()) as DesktopAuthPoll & { error?: string };
    if (!response.ok) {
      throw new Error(data.error || `oidc poll failed: ${response.status}`);
    }
    return data;
  }

  async revokeCurrentToken(): Promise<void> {
    await this.ensureAuthLoaded();
    if (!this.token) {
      return;
    }
    const response = await fetch(`${this.apiUrl}/api/v1/tokens/current/revoke`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${this.token}`,
      },
    });
    if (!response.ok && response.status !== 400) {
      const data = (await response.json().catch(() => ({}))) as { error?: string };
      throw new Error(data.error || `revoke failed: ${response.status}`);
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
    language?: string
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
    if (cached) return cached;

    try {
      const data = await this.query<{ ideCapabilities: ExtensionCapabilities }>(IDE_CAPABILITIES);
      return this.capabilityCache.set(data.ideCapabilities);
    } catch {
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
      } catch {
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

  async getCodeToRequirements(symbolId: string): Promise<RequirementLink[]> {
    const data = await this.query<{ codeToRequirements: RequirementLink[] }>(CODE_TO_REQUIREMENTS, {
      symbolId,
    });
    return data.codeToRequirements;
  }

  async getRequirementToCode(requirementId: string): Promise<RequirementLink[]> {
    const data = await this.query<{ requirementToCode: RequirementLink[] }>(REQUIREMENT_TO_CODE, {
      requirementId,
    });
    return data.requirementToCode;
  }

  async verifyLink(linkId: string, verified: boolean): Promise<RequirementLink> {
    const data = await this.query<{ verifyLink: RequirementLink }>(VERIFY_LINK, {
      linkId,
      verified,
    });
    return data.verifyLink;
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
    this.apiUrl = config.get("apiUrl", "http://localhost:8080");
    this.token = "";
    this.authLoaded = undefined;
    await this.ensureAuthLoaded();
    this.clearCaches();
  }

  async storeToken(token: string): Promise<void> {
    const config = vscode.workspace.getConfiguration("sourcebridge");
    if (this.context) {
      await this.context.secrets.store("sourcebridge.token", token);
    }
    await this.clearLegacyToken(config);
    this.token = token;
    this.authLoaded = Promise.resolve();
    this.clearCaches();
  }

  async clearStoredToken(): Promise<void> {
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
    const config = vscode.workspace.getConfiguration("sourcebridge");
    const legacyToken = config.get<string>("token", "");
    if (!this.context) {
      this.token = legacyToken;
      return;
    }

    const storedToken = await this.context.secrets.get("sourcebridge.token");
    if (storedToken) {
      this.token = storedToken;
      if (legacyToken) {
        await this.clearLegacyToken(config);
      }
      return;
    }

    if (legacyToken) {
      this.token = legacyToken;
      await this.context.secrets.store("sourcebridge.token", legacyToken);
      await this.clearLegacyToken(config);
      return;
    }

    this.token = "";
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
