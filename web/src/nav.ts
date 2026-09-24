// Three routes don't need a router library: pages call navigate, and the
// app re-renders on popstate.
export function navigate(path: string): void {
  history.pushState(null, "", path);
  dispatchEvent(new PopStateEvent("popstate"));
}
