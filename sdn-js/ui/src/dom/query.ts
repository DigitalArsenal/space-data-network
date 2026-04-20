export function query<TElement extends HTMLElement>(
  root: ParentNode,
  selector: string,
): TElement | null {
  return root.querySelector(selector) as TElement | null;
}

export function queryAll(root: ParentNode, selector: string): Element[] {
  return Array.from(root.querySelectorAll(selector));
}
