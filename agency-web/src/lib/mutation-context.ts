import { createContext, useContext } from "react";
import type { Mutate } from "./types";

export const MutationContext = createContext<Mutate | null>(null);
export function useMutation(): Mutate {
  const mutate = useContext(MutationContext);
  if (!mutate) throw new Error("Mutation provider is missing");
  return mutate;
}
