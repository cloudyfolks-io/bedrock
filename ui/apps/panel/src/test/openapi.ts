import type { FakeRoute } from "./fakeApi";
import { loadCrds } from "./crds";

export function openApiRoutes(): Record<string, FakeRoute> {
  const schemas = Object.fromEntries(loadCrds().map((crd) => [`io.cloudyfolks.bedrock.v1alpha1.${crd.kind}`, crd.schema]));
  return {
    "GET /openapi/v3": { body: { paths: { "apis/bedrock.cloudyfolks.io/v1alpha1": { serverRelativeURL: "/openapi/v3/apis/bedrock.cloudyfolks.io/v1alpha1?hash=abc" } } } },
    "GET /openapi/v3/apis/bedrock.cloudyfolks.io/v1alpha1": { body: { components: { schemas } } },
  };
}
