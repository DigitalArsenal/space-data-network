import { writable } from "svelte/store";

const initialState = {
  loaded: false,
  loading: false,
  kind: "",
  message: "OrbPro not loaded.",
};

export const orbProState = writable(initialState);

export function setOrbProState(patch) {
  orbProState.update((current) => ({ ...current, ...patch }));
}

export function resetOrbProState() {
  orbProState.set(initialState);
}
