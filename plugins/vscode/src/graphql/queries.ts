export const SYMBOLS_FOR_FILE = `
  query SymbolsForFile($repositoryId: ID!, $filePath: String) {
    symbols(repositoryId: $repositoryId, filePath: $filePath) {
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

export const REQUIREMENT_TO_CODE = `
  query RequirementToCode($requirementId: ID!) {
    requirementToCode(requirementId: $requirementId) {
      id
      symbolId
      confidence
      rationale
      verified
    }
  }
`;

export const CODE_TO_REQUIREMENTS = `
  query CodeToRequirements($symbolId: ID!) {
    codeToRequirements(symbolId: $symbolId) {
      id
      requirementId
      confidence
      rationale
      verified
    }
  }
`;

export const REQUIREMENTS = `
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
      }
      totalCount
    }
  }
`;

export const REQUIREMENT = `
  query Requirement($id: ID!) {
    requirement(id: $id) {
      id
      externalId
      title
      description
      source
      priority
      tags
    }
  }
`;

export const REPOSITORIES = `
  query Repositories {
    repositories {
      id
      name
      path
      status
      hasAuth
      fileCount
      functionCount
    }
  }
`;

export const ADD_REPOSITORY = `
  mutation AddRepository($input: AddRepositoryInput!) {
    addRepository(input: $input) {
      id
      name
      path
      status
      hasAuth
    }
  }
`;

export const HEALTH = `
  query Health {
    health {
      status
    }
  }
`;

export const DISCUSS_CODE = `
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

export const REVIEW_CODE = `
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

export const VERIFY_LINK = `
  mutation VerifyLink($linkId: ID!, $verified: Boolean!) {
    verifyLink(linkId: $linkId, verified: $verified) {
      id
      requirementId
      symbolId
      confidence
      rationale
      verified
      requirement {
        id
        externalId
        title
      }
      symbol {
        id
        name
        filePath
        startLine
        endLine
      }
    }
  }
`;

// ---------------------------------------------------------------------------
// Knowledge Engine
// ---------------------------------------------------------------------------

export const FEATURES = `
  query Features {
    features {
      cliffNotes
      learningPaths
      codeTours
      systemExplain
    }
  }
`;

export const IDE_CAPABILITIES = `
  query IdeCapabilities {
    ideCapabilities {
      repoKnowledge
      scopedKnowledge
      scopedExplain
      impactReports
      discussCode
      reviewCode
      vscode
      jetbrains
    }
  }
`;

export const EXTENSION_CAPABILITIES = `
  query ExtensionCapabilities {
    queryType: __type(name: "Query") {
      fields {
        name
      }
    }
    mutationType: __type(name: "Mutation") {
      fields {
        name
      }
    }
    explainSystemInput: __type(name: "ExplainSystemInput") {
      inputFields {
        name
      }
    }
    generateCliffNotesInput: __type(name: "GenerateCliffNotesInput") {
      inputFields {
        name
      }
    }
  }
`;

export const KNOWLEDGE_ARTIFACTS = `
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

export const KNOWLEDGE_ARTIFACT = `
  query KnowledgeArtifact($id: ID!) {
    knowledgeArtifact(id: $id) {
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

export const KNOWLEDGE_SCOPE_CHILDREN = `
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

export const GENERATE_CLIFF_NOTES = `
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
      progress
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

export const GENERATE_LEARNING_PATH = `
  mutation GenerateLearningPath($input: GenerateLearningPathInput!) {
    generateLearningPath(input: $input) {
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

export const GENERATE_CODE_TOUR = `
  mutation GenerateCodeTour($input: GenerateCodeTourInput!) {
    generateCodeTour(input: $input) {
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

export const EXPLAIN_SYSTEM = `
  mutation ExplainSystem($input: ExplainSystemInput!) {
    explainSystem(input: $input) {
      explanation
      model
      inputTokens
      outputTokens
    }
  }
`;

export const LATEST_IMPACT_REPORT = `
  query LatestImpactReport($repositoryId: ID!) {
    latestImpactReport(repositoryId: $repositoryId) {
      id
      oldCommitSha
      newCommitSha
      staleArtifacts
      computedAt
      filesChanged {
        path
        status
        additions
        deletions
      }
      affectedRequirements {
        requirementId
        externalId
        title
        affectedLinks
        totalLinks
      }
    }
  }
`;
