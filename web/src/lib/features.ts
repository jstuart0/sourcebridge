// Feature flags — queried from the server at runtime.
// ossFeatures provides safe defaults while the query is in flight.

import { useQuery } from "urql";
import { FEATURES_QUERY } from "@/lib/graphql/queries";

export interface FeatureFlags {
  multiTenant: boolean;
  sso: boolean;
  linearConnector: boolean;
  jiraConnector: boolean;
  githubApp: boolean;
  gitlabApp: boolean;
  auditLog: boolean;
  webhooks: boolean;
  customTemplates: boolean;
  billing: boolean;
  // Knowledge engine — OSS features
  cliffNotes: boolean;
  learningPaths: boolean;
  codeTours: boolean;
  systemExplain: boolean;
  symbolScopedAnalysis: boolean;
  // Knowledge engine — enterprise features
  multiAudienceKnowledge: boolean;
  customKnowledgeTemplates: boolean;
  advancedLearningPaths: boolean;
  slideGeneration: boolean;
  podcastGeneration: boolean;
  knowledgeScheduling: boolean;
  knowledgeExport: boolean;
}

export const ossFeatures: FeatureFlags = {
  multiTenant: false,
  sso: false,
  linearConnector: false,
  jiraConnector: false,
  githubApp: false,
  gitlabApp: false,
  auditLog: false,
  webhooks: false,
  customTemplates: false,
  billing: false,
  // Knowledge engine — OSS features disabled until server confirms
  cliffNotes: false,
  learningPaths: false,
  codeTours: false,
  systemExplain: false,
  symbolScopedAnalysis: false,
  // Knowledge engine — enterprise features
  multiAudienceKnowledge: false,
  customKnowledgeTemplates: false,
  advancedLearningPaths: false,
  slideGeneration: false,
  podcastGeneration: false,
  knowledgeScheduling: false,
  knowledgeExport: false,
};

/** React hook — queries the server for real feature availability. */
export function useFeatures(): FeatureFlags {
  const [result] = useQuery({ query: FEATURES_QUERY });
  if (result.data?.features) {
    return result.data.features as FeatureFlags;
  }
  return ossFeatures;
}

/** Synchronous fallback for non-React contexts. Returns OSS defaults. */
export function getFeatures(): FeatureFlags {
  return ossFeatures;
}
