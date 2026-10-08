import { useQueryClient } from "@tanstack/react-query";
import { sessionKey } from "../api/HttpProvider";

export function useSessionCheck(): () => void {
  const queryClient = useQueryClient();
  return () => void queryClient.invalidateQueries({ queryKey: sessionKey });
}
