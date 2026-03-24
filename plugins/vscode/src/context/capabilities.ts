import { SourceBridgeClient } from "../graphql/client";

export interface ExtensionCapabilities {
  repoKnowledge: boolean;
  scopedKnowledge: boolean;
  scopedExplain: boolean;
  impactReports: boolean;
}

export async function getCapabilities(client: SourceBridgeClient): Promise<ExtensionCapabilities> {
  return client.getCapabilities();
}
