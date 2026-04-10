import { gql } from "urql";

export const FEATURES_QUERY = gql`
  query Features {
    features {
      multiTenant
      sso
      linearConnector
      jiraConnector
      githubApp
      gitlabApp
      auditLog
      webhooks
      customTemplates
      billing
      cliffNotes
      learningPaths
      codeTours
      systemExplain
      symbolScopedAnalysis
      multiAudienceKnowledge
      customKnowledgeTemplates
      advancedLearningPaths
      slideGeneration
      podcastGeneration
      knowledgeScheduling
      knowledgeExport
    }
  }
`;

export const HEALTH_QUERY = gql`
  query Health {
    health {
      status
      services {
        name
        status
      }
    }
  }
`;

export const REPOSITORIES_QUERY = gql`
  query Repositories {
    repositories {
      id
      name
      path
      status
      hasAuth
      fileCount
      functionCount
      classCount
      requirementCount
      lastIndexedAt
      createdAt
      understandingScore {
        overall
      }
    }
  }
`;

/** Lightweight repo list — no understandingScore computation. Use on pages that only need repo metadata. */
export const REPOSITORIES_LIGHT_QUERY = gql`
  query RepositoriesLight {
    repositories {
      id
      name
      path
      status
      hasAuth
      fileCount
      functionCount
      classCount
      requirementCount
      lastIndexedAt
      createdAt
    }
  }
`;

export const UNDERSTANDING_SCORE_QUERY = gql`
  query UnderstandingScore($repositoryId: ID!) {
    understandingScore(repositoryId: $repositoryId) {
      overall
      traceabilityCoverage
      documentationCoverage
      reviewCoverage
      testCoverage
      knowledgeFreshness
      aiCodeRatio
      computedAt
    }
  }
`;

export const REPOSITORY_QUERY = gql`
  query Repository($id: ID!) {
    repository(id: $id) {
      id
      name
      path
      remoteUrl
      status
      fileCount
      functionCount
      classCount
      lastIndexedAt
      createdAt
      files {
        nodes {
          id
          path
          language
          lineCount
          aiScore
          aiSignals
        }
        totalCount
      }
      modules {
        id
        name
        path
        fileCount
      }
    }
  }
`;

export const SYMBOLS_QUERY = gql`
  query Symbols($repositoryId: ID!, $query: String, $kind: SymbolKind, $limit: Int, $offset: Int) {
    symbols(repositoryId: $repositoryId, query: $query, kind: $kind, limit: $limit, offset: $offset) {
      nodes {
        id
        name
        qualifiedName
        kind
        language
        filePath
        startLine
        endLine
        signature
        docComment
      }
      totalCount
    }
  }
`;

export const REQUIREMENTS_QUERY = gql`
  query Requirements($repositoryId: ID!, $limit: Int, $offset: Int) {
    requirements(repositoryId: $repositoryId, limit: $limit, offset: $offset) {
      nodes {
        id
        externalId
        title
        description
        source
        priority
        tags
        createdAt
      }
      totalCount
    }
  }
`;

export const REQUIREMENT_QUERY = gql`
  query Requirement($id: ID!) {
    requirement(id: $id) {
      id
      externalId
      title
      description
      source
      priority
      tags
      links {
        id
        confidence
        symbolId
        rationale
        verified
        symbol {
          id
          name
          filePath
          kind
          startLine
          endLine
        }
      }
      createdAt
      updatedAt
    }
  }
`;

export const REQUIREMENT_LINKS_QUERY = gql`
  query RequirementLinks($requirementId: ID!, $limit: Int, $offset: Int) {
    requirementLinks(requirementId: $requirementId, limit: $limit, offset: $offset) {
      id
      confidence
      symbolId
      rationale
      verified
      symbol {
        id
        name
        filePath
        kind
        startLine
        endLine
      }
    }
  }
`;

export const TRACEABILITY_MATRIX_QUERY = gql`
  query TraceabilityMatrix($repositoryId: ID!) {
    traceabilityMatrix(repositoryId: $repositoryId) {
      requirements {
        id
        externalId
        title
      }
      symbols {
        id
        name
        filePath
        kind
      }
      links {
        id
        requirementId
        symbolId
        confidence
        verified
      }
      coverage
    }
  }
`;

export const REQUIREMENT_TO_CODE_QUERY = gql`
  query RequirementToCode($requirementId: ID!) {
    requirementToCode(requirementId: $requirementId) {
      id
      symbolId
      confidence
      rationale
      verified
      symbol {
        id
        name
        qualifiedName
        kind
        filePath
        startLine
        endLine
      }
    }
  }
`;

export const IMPORT_REQUIREMENTS_MUTATION = gql`
  mutation ImportRequirements($input: ImportRequirementsInput!) {
    importRequirements(input: $input) {
      imported
      skipped
      warnings
    }
  }
`;

export const VERIFY_LINK_MUTATION = gql`
  mutation VerifyLink($linkId: ID!, $verified: Boolean!) {
    verifyLink(linkId: $linkId, verified: $verified) {
      id
      confidence
      verified
    }
  }
`;

export const SEARCH_QUERY = gql`
  query Search($query: String!, $repositoryId: ID, $limit: Int) {
    search(query: $query, repositoryId: $repositoryId, limit: $limit) {
      type
      id
      title
      description
      filePath
      line
      repositoryId
      repositoryName
    }
  }
`;

export const SOURCE_FILE_QUERY = gql`
  query SourceFile($repositoryId: ID!, $filePath: String!) {
    sourceFile(repositoryId: $repositoryId, filePath: $filePath) {
      ok
      errorCode
      message
      file {
        repositoryId
        filePath
        language
        lineCount
        content
        contentHash
      }
    }
  }
`;

export const ADD_REPOSITORY_MUTATION = gql`
  mutation AddRepository($input: AddRepositoryInput!) {
    addRepository(input: $input) {
      id
      name
      path
      status
    }
  }
`;

export const REMOVE_REPOSITORY_MUTATION = gql`
  mutation RemoveRepository($id: ID!) {
    removeRepository(id: $id)
  }
`;

export const REINDEX_REPOSITORY_MUTATION = gql`
  mutation ReindexRepository($id: ID!) {
    reindexRepository(id: $id) {
      id
      status
      lastIndexedAt
    }
  }
`;

export const ANALYZE_SYMBOL_MUTATION = gql`
  mutation AnalyzeSymbol($repositoryId: ID!, $symbolId: ID!) {
    analyzeSymbol(repositoryId: $repositoryId, symbolId: $symbolId) {
      summary
      purpose
      concerns
      suggestions
      model
      inputTokens
      outputTokens
    }
  }
`;

export const DISCUSS_CODE_MUTATION = gql`
  mutation DiscussCode($input: DiscussCodeInput!) {
    discussCode(input: $input) {
      answer
      references
      relatedRequirements
      model
      inputTokens
      outputTokens
    }
  }
`;

export const REVIEW_CODE_MUTATION = gql`
  mutation ReviewCode($input: ReviewCodeInput!) {
    reviewCode(input: $input) {
      template
      findings {
        category
        severity
        message
        filePath
        startLine
        endLine
        suggestion
      }
      score
      model
      inputTokens
      outputTokens
    }
  }
`;

export const AUTO_LINK_MUTATION = gql`
  mutation AutoLinkRequirements($repositoryId: ID!, $minConfidence: Float) {
    autoLinkRequirements(repositoryId: $repositoryId, minConfidence: $minConfidence) {
      linksCreated
      requirementsProcessed
      links {
        id
        requirementId
        symbolId
        confidence
      }
    }
  }
`;

export const CREATE_MANUAL_LINK_MUTATION = gql`
  mutation CreateManualLink($input: CreateManualLinkInput!) {
    createManualLink(input: $input) {
      id
      requirementId
      symbolId
      confidence
      rationale
      verified
    }
  }
`;

export const LLM_USAGE_QUERY = gql`
  query LLMUsage($repositoryId: ID, $limit: Int) {
    llmUsage(repositoryId: $repositoryId, limit: $limit) {
      id
      provider
      model
      operation
      inputTokens
      outputTokens
      createdAt
    }
  }
`;

export const PLATFORM_STATS_QUERY = gql`
  query PlatformStats {
    platformStats {
      repositories
      files
      symbols
      requirements
      links
      totalInputTokens
      totalOutputTokens
    }
  }
`;

export const ENRICH_REQUIREMENT_MUTATION = gql`
  mutation EnrichRequirement($requirementId: ID!) {
    enrichRequirement(requirementId: $requirementId) {
      id
      externalId
      title
      description
      tags
    }
  }
`;

// ---------------------------------------------------------------------------
// AI-Generated Code Detection
// ---------------------------------------------------------------------------

export const AI_GENERATED_FILES_QUERY = gql`
  query AiGeneratedFiles($repositoryId: ID!, $minScore: Float) {
    aiGeneratedFiles(repositoryId: $repositoryId, minScore: $minScore) {
      id
      path
      language
      lineCount
      aiScore
      aiSignals
    }
  }
`;

// ---------------------------------------------------------------------------
// Change Impact Analysis
// ---------------------------------------------------------------------------

export const LATEST_IMPACT_REPORT_QUERY = gql`
  query LatestImpactReport($repositoryId: ID!) {
    latestImpactReport(repositoryId: $repositoryId) {
      id
      repositoryId
      oldCommitSha
      newCommitSha
      filesChanged {
        path
        oldPath
        status
        additions
        deletions
      }
      symbolsAdded {
        symbolId
        name
        filePath
        changeType
        newSignature
      }
      symbolsModified {
        symbolId
        name
        filePath
        changeType
        oldSignature
        newSignature
      }
      symbolsRemoved {
        symbolId
        name
        filePath
        changeType
        oldSignature
      }
      affectedLinks {
        linkId
        requirementId
        symbolId
        impact
        confidence
      }
      affectedRequirements {
        requirementId
        externalId
        title
        affectedLinks
        totalLinks
      }
      staleArtifacts
      computedAt
    }
  }
`;

export const IMPACT_REPORTS_QUERY = gql`
  query ImpactReports($repositoryId: ID!, $limit: Int) {
    impactReports(repositoryId: $repositoryId, limit: $limit) {
      id
      oldCommitSha
      newCommitSha
      filesChanged {
        path
        status
        additions
        deletions
      }
      symbolsAdded { name filePath changeType }
      symbolsModified { name filePath changeType }
      symbolsRemoved { name filePath changeType }
      affectedLinks { linkId impact }
      affectedRequirements { requirementId externalId title affectedLinks totalLinks }
      staleArtifacts
      computedAt
    }
  }
`;

// ---------------------------------------------------------------------------
// Knowledge Engine
// ---------------------------------------------------------------------------

export const KNOWLEDGE_ARTIFACTS_QUERY = gql`
  query KnowledgeArtifacts($repositoryId: ID!, $scopeType: KnowledgeScopeType, $scopePath: String) {
    knowledgeArtifacts(repositoryId: $repositoryId, scopeType: $scopeType, scopePath: $scopePath) {
      id
      repositoryId
      type
      audience
      depth
      scope {
        scopeType
        scopePath
        modulePath
        filePath
        symbolName
      }
      status
      progress
      stale
      errorCode
      errorMessage
      generatedAt
      createdAt
      updatedAt
      sourceRevision {
        commitSha
        branch
        contentFingerprint
      }
      sections {
        id
        artifactId
        title
        content
        summary
        confidence
        inferred
        orderIndex
        evidence {
          id
          sectionId
          sourceType
          sourceId
          filePath
          lineStart
          lineEnd
          rationale
        }
      }
    }
  }
`;

export const KNOWLEDGE_SCOPE_CHILDREN_QUERY = gql`
  query KnowledgeScopeChildren(
    $repositoryId: ID!
    $scopeType: KnowledgeScopeType!
    $scopePath: String!
    $audience: KnowledgeAudience
    $depth: KnowledgeDepth
  ) {
    knowledgeScopeChildren(
      repositoryId: $repositoryId
      scopeType: $scopeType
      scopePath: $scopePath
      audience: $audience
      depth: $depth
    ) {
      scopeType
      label
      scopePath
      hasArtifact
      summary
    }
  }
`;

export const EXECUTION_ENTRY_POINTS_QUERY = gql`
  query ExecutionEntryPoints($repositoryId: ID!) {
    executionEntryPoints(repositoryId: $repositoryId) {
      kind
      label
      value
      filePath
      lineStart
      lineEnd
      symbolId
      summary
    }
  }
`;

export const EXECUTION_PATH_QUERY = gql`
  query ExecutionPath($input: ExecutionPathInput!) {
    executionPath(input: $input) {
      entryKind
      entryLabel
      message
      trustQualified
      observedStepCount
      inferredStepCount
      steps {
        orderIndex
        kind
        label
        explanation
        confidence
        observed
        reason
        filePath
        lineStart
        lineEnd
        symbolId
        symbolName
      }
    }
  }
`;

export const GENERATE_CLIFF_NOTES_MUTATION = gql`
  mutation GenerateCliffNotes($input: GenerateCliffNotesInput!) {
    generateCliffNotes(input: $input) {
      id
      repositoryId
      type
      audience
      depth
      scope {
        scopeType
        scopePath
        modulePath
        filePath
        symbolName
      }
      status
      stale
      generatedAt
      sections {
        id
        title
        content
        summary
        confidence
        inferred
        orderIndex
        evidence {
          id
          sourceType
          filePath
          lineStart
          lineEnd
          rationale
        }
      }
    }
  }
`;

export const GENERATE_LEARNING_PATH_MUTATION = gql`
  mutation GenerateLearningPath($input: GenerateLearningPathInput!) {
    generateLearningPath(input: $input) {
      id
      type
      status
      stale
      generatedAt
      sections {
        id
        title
        content
        summary
        confidence
        orderIndex
        evidence {
          id
          sourceType
          filePath
          lineStart
          lineEnd
          rationale
        }
      }
    }
  }
`;

export const GENERATE_CODE_TOUR_MUTATION = gql`
  mutation GenerateCodeTour($input: GenerateCodeTourInput!) {
    generateCodeTour(input: $input) {
      id
      type
      status
      stale
      generatedAt
      sections {
        id
        title
        content
        summary
        confidence
        orderIndex
        evidence {
          id
          sourceType
          filePath
          lineStart
          lineEnd
          rationale
        }
      }
    }
  }
`;

export const GENERATE_WORKFLOW_STORY_MUTATION = gql`
  mutation GenerateWorkflowStory($input: GenerateWorkflowStoryInput!) {
    generateWorkflowStory(input: $input) {
      id
      repositoryId
      type
      audience
      depth
      scope {
        scopeType
        scopePath
        modulePath
        filePath
        symbolName
      }
      status
      stale
      generatedAt
      sections {
        id
        title
        content
        summary
        confidence
        inferred
        orderIndex
        evidence {
          id
          sourceType
          filePath
          lineStart
          lineEnd
          rationale
        }
      }
    }
  }
`;

export const EXPLAIN_SYSTEM_MUTATION = gql`
  mutation ExplainSystem($input: ExplainSystemInput!) {
    explainSystem(input: $input) {
      explanation
      model
      inputTokens
      outputTokens
    }
  }
`;

export const REFRESH_KNOWLEDGE_ARTIFACT_MUTATION = gql`
  mutation RefreshKnowledgeArtifact($id: ID!) {
    refreshKnowledgeArtifact(id: $id) {
      id
      repositoryId
      type
      audience
      depth
      scope {
        scopeType
        scopePath
        modulePath
        filePath
        symbolName
      }
      status
      errorCode
      errorMessage
      stale
      generatedAt
      sections {
        id
        title
        content
        summary
        confidence
        inferred
        orderIndex
        evidence {
          id
          sourceType
          filePath
          lineStart
          lineEnd
          rationale
        }
      }
    }
  }
`;

export const REQUIREMENT_KNOWLEDGE_QUERY = gql`
  query RequirementKnowledge($repositoryId: ID!, $requirementId: ID!) {
    knowledgeArtifacts(
      repositoryId: $repositoryId
      scopeType: REQUIREMENT
      scopePath: $requirementId
    ) {
      id
      status
      progress
      stale
      generatedAt
      errorCode
      errorMessage
      scope {
        scopeType
        scopePath
      }
      sections {
        id
        title
        content
        summary
        confidence
        inferred
        orderIndex
        evidence {
          id
          sourceType
          sourceId
          filePath
          lineStart
          lineEnd
          rationale
        }
      }
    }
  }
`;

// --- Change Simulation ---

export const SIMULATE_CHANGE_MUTATION = gql`
  mutation SimulateChange($input: SimulateChangeInput!) {
    simulateChange(input: $input) {
      id
      simulated
      description
      anchorFile
      anchorSymbol
      resolvedSymbols {
        symbolId
        name
        qualifiedName
        kind
        filePath
        similarity
        isAnchor
      }
      report {
        id
        repositoryId
        filesChanged { path status additions deletions }
        symbolsAdded { symbolId name filePath changeType newSignature }
        symbolsModified { symbolId name filePath changeType oldSignature newSignature }
        symbolsRemoved { symbolId name filePath changeType oldSignature }
        affectedLinks { linkId requirementId symbolId impact confidence }
        affectedRequirements { requirementId externalId title affectedLinks totalLinks }
        staleArtifacts
        computedAt
      }
      computedAt
    }
  }
`;

// --- Discovered Requirements (Spec Extraction) ---

export const DISCOVERED_REQUIREMENTS_QUERY = gql`
  query DiscoveredRequirements($repositoryId: ID!, $status: String, $confidence: String, $limit: Int, $offset: Int) {
    discoveredRequirements(repositoryId: $repositoryId, status: $status, confidence: $confidence, limit: $limit, offset: $offset) {
      nodes {
        id
        repoId
        source
        sourceFile
        sourceLine
        sourceFiles
        text
        rawText
        groupKey
        language
        keywords
        confidence
        status
        llmRefined
        promotedTo
        dismissedBy
        dismissedReason
        createdAt
      }
      totalCount
    }
  }
`;

export const TRIGGER_SPEC_EXTRACTION_MUTATION = gql`
  mutation TriggerSpecExtraction($input: TriggerSpecExtractionInput!) {
    triggerSpecExtraction(input: $input) {
      discovered
      totalCandidates
      warnings
      model
      inputTokens
      outputTokens
    }
  }
`;

export const PROMOTE_DISCOVERED_REQUIREMENT_MUTATION = gql`
  mutation PromoteDiscoveredRequirement($id: ID!, $title: String, $description: String) {
    promoteDiscoveredRequirement(id: $id, title: $title, description: $description) {
      requirement {
        id
        title
        description
        source
        priority
        tags
      }
      discoveredRequirement {
        id
        status
        promotedTo
      }
    }
  }
`;

export const DISMISS_DISCOVERED_REQUIREMENT_MUTATION = gql`
  mutation DismissDiscoveredRequirement($id: ID!, $reason: String) {
    dismissDiscoveredRequirement(id: $id, reason: $reason) {
      id
      status
      dismissedBy
      dismissedReason
    }
  }
`;

export const DISMISS_ALL_DISCOVERED_REQUIREMENTS_MUTATION = gql`
  mutation DismissAllDiscoveredRequirements($repositoryId: ID!) {
    dismissAllDiscoveredRequirements(repositoryId: $repositoryId)
  }
`;

// ---------------------------------------------------------------------------
// Multi-Repo Federation
// ---------------------------------------------------------------------------

export const REPO_LINKS_QUERY = gql`
  query RepoLinks($repoId: ID!) {
    repoLinks(repoId: $repoId) {
      id
      sourceRepoId
      targetRepoId
      linkType
      createdAt
    }
  }
`;

export const CROSS_REPO_REFS_QUERY = gql`
  query CrossRepoRefs($repoId: ID!, $refType: CrossRepoRefType, $limit: Int) {
    crossRepoRefs(repoId: $repoId, refType: $refType, limit: $limit) {
      id
      sourceSymbolId
      targetSymbolId
      sourceRepoId
      targetRepoId
      refType
      confidence
      contractFile
      consumerFile
      evidence
      createdAt
    }
  }
`;

export const SYMBOL_CROSS_REPO_REFS_QUERY = gql`
  query SymbolCrossRepoRefs($symbolId: ID!) {
    symbolCrossRepoRefs(symbolId: $symbolId) {
      id
      sourceSymbolId
      targetSymbolId
      sourceRepoId
      targetRepoId
      refType
      confidence
      contractFile
      consumerFile
      evidence
      createdAt
    }
  }
`;

export const API_CONTRACTS_QUERY = gql`
  query APIContracts($repoId: ID!) {
    apiContracts(repoId: $repoId) {
      id
      repoId
      filePath
      contractType
      endpointCount
      version
      detectedAt
    }
  }
`;

export const LINK_REPOS_MUTATION = gql`
  mutation LinkRepos($sourceRepoId: ID!, $targetRepoId: ID!, $linkType: String) {
    linkRepos(sourceRepoId: $sourceRepoId, targetRepoId: $targetRepoId, linkType: $linkType) {
      id
      sourceRepoId
      targetRepoId
      linkType
      createdAt
    }
  }
`;

export const UNLINK_REPOS_MUTATION = gql`
  mutation UnlinkRepos($linkId: ID!) {
    unlinkRepos(linkId: $linkId)
  }
`;

export const DETECT_CONTRACTS_MUTATION = gql`
  mutation DetectContracts($repoId: ID!) {
    detectContracts(repoId: $repoId)
  }
`;

// ---------------------------------------------------------------------------
// Architecture Diagrams
// ---------------------------------------------------------------------------

export const ARCHITECTURE_DIAGRAM_QUERY = gql`
  query ArchitectureDiagram(
    $repoId: ID!
    $level: DiagramLevel!
    $moduleFilter: String
    $moduleDepth: Int
    $maxNodes: Int
  ) {
    architectureDiagram(
      repoId: $repoId
      level: $level
      moduleFilter: $moduleFilter
      moduleDepth: $moduleDepth
      maxNodes: $maxNodes
    ) {
      mermaidSource
      level
      totalModules
      shownModules
      truncated
      modules {
        path
        symbolCount
        fileCount
        requirementLinkCount
        inboundEdgeCount
        outboundEdges {
          targetPath
          callCount
        }
      }
    }
  }
`;
