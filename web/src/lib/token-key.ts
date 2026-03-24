/** Edition-scoped localStorage key for the auth token.
 *  Enterprise builds use a separate key to prevent session bleed
 *  between OSS and enterprise instances on the same parent domain. */
export const TOKEN_KEY =
  process.env.NEXT_PUBLIC_EDITION === "enterprise"
    ? "sourcebridge_enterprise_token"
    : "sourcebridge_token";
