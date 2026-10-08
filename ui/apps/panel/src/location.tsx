import { createContext, useContext, type ReactNode } from "react";

export interface LocationActions {
  assign: (url: string) => void;
}

const browserActions: LocationActions = { assign: (url) => window.location.assign(url) };

const LocationContext = createContext<LocationActions>(browserActions);

export function LocationProvider({ actions, children }: { actions: LocationActions; children: ReactNode }) {
  return <LocationContext.Provider value={actions}>{children}</LocationContext.Provider>;
}

export function useLocationActions(): LocationActions {
  return useContext(LocationContext);
}
