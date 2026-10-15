/** Fail with a clear message when an expected DOM element is absent. */
export const requireElement = <T extends Element>(element: T | null | undefined): T => {
  if (!element) {
    throw new Error('Expected a DOM element, but the query returned none');
  }
  return element;
};
