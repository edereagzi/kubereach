import { queryOptions } from "@tanstack/react-query";
import { ConfigService } from "@bindings/internal/bindings";

export const configQuery = queryOptions({
  queryKey: ["config"],
  queryFn: () => ConfigService.Load(),
});
