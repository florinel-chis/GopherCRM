interface ErrorPayload {
  code?: string;
  message?: string;
  details?: Record<string, unknown>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** Read the unwrapped API error without assuming every rejection is an Axios error. */
export function getAPIError(error: unknown): ErrorPayload | undefined {
  if (!isRecord(error) || !isRecord(error.response) || !isRecord(error.response.data)) {
    return undefined;
  }
  const data = error.response.data;
  return {
    code: typeof data.code === 'string' ? data.code : undefined,
    message: typeof data.message === 'string' ? data.message : undefined,
    details: isRecord(data.details) ? data.details : undefined,
  };
}

export function getHTTPStatus(error: unknown): number | undefined {
  if (!isRecord(error) || !isRecord(error.response)) return undefined;
  return typeof error.response.status === 'number' ? error.response.status : undefined;
}

export function getErrorMessage(error: unknown, fallback: string): string {
  return isRecord(error) && typeof error.message === 'string' && error.message
    ? error.message
    : fallback;
}
