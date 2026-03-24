import { Client, cacheExchange, fetchExchange, mapExchange } from "urql";
import { isTokenExpired, forceLogout } from "@/lib/auth-utils";

export function createClient(token?: string) {
  // If the token we're about to bake into the client is already expired,
  // force logout immediately instead of building a client that will 401.
  if (token && isTokenExpired(token)) {
    forceLogout();
    token = undefined;
  }

  return new Client({
    url: "/api/v1/graphql",
    exchanges: [
      cacheExchange,
      mapExchange({
        onError(error) {
          const isGraphQLAuth = error.graphQLErrors?.some(
            (e) => e.extensions?.code === "UNAUTHENTICATED"
          );

          // CombinedError.response is the raw Response object from fetch
          const isNetworkAuth =
            (error.response as { status?: number } | undefined)?.status === 401;

          if (isGraphQLAuth || isNetworkAuth) {
            forceLogout();
          }
        },
      }),
      fetchExchange,
    ],
    fetchOptions: () => ({
      headers: {
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        "Content-Type": "application/json",
      },
    }),
  });
}
