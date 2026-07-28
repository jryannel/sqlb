// The transport. Hand-written on purpose, and the reason the generated client
// takes a request function instead of building one.
//
// Nothing here is derivable from a schema: where the API lives, where the token
// is kept, what a 401 does, and how an error body reaches the UI. The generated
// files next door know the URL grammar and the row types and nothing else,
// which is what lets this file be replaced wholesale without regenerating
// anything.

import { isProblem, type ApiRequest, type Problem, type Transport } from './client.gen';

/** Thrown for any non-2xx response. */
export class ApiError extends Error {
  readonly status: number;
  /**
   * The RFC 9457 body, when the server sent one.
   *
   * Worth keeping whole rather than flattening to a message: each detail
   * carries `allowed`, the list of what would have been accepted, which is the
   * difference between "sort: column is not sortable" and a fix. Flattening it
   * is what both of the hand-written clients ADR-0028 was written from did.
   */
  readonly problem?: Problem;

  constructor(status: number, message: string, problem?: Problem) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.problem = problem;
  }
}

export interface ClientOptions {
  /** API root, e.g. `http://localhost:8080`. */
  baseUrl: string;
  /** Returns the bearer token, or null when signed out. */
  token: () => string | null;
  /** Called when the server rejects the token. Sign-out and redirect live in
   * the application, because the generated client cannot know what a session
   * is. */
  onUnauthorized?: () => void;
}

/**
 * Builds the request function every generated call takes as its first argument.
 *
 *     const request = createTransport({ baseUrl, token: () => localStorage.getItem('token') });
 *     const page = await listTasks(request, { where: { status: 'todo' } });
 */
export function createTransport(options: ClientOptions): Transport {
  return async <T>({ method, path, query, body, signal }: ApiRequest): Promise<T> => {
    const token = options.token();
    const response = await fetch(`${options.baseUrl}${path}${query ? `?${query}` : ''}`, {
      method,
      headers: {
        ...(body === undefined ? {} : { 'content-type': 'application/json' }),
        ...(token === null ? {} : { authorization: `Bearer ${token}` }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    });

    if (!response.ok) {
      if (response.status === 401) options.onUnauthorized?.();
      throw await asError(response);
    }
    // 204 from DELETE. Reading it as JSON would throw on an empty body, and the
    // generated signature already says the result is void.
    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  };
}

async function asError(response: Response): Promise<ApiError> {
  let body: unknown;
  try {
    body = await response.json();
  } catch {
    return new ApiError(response.status, response.statusText);
  }
  if (isProblem(body)) {
    return new ApiError(response.status, body.detail ?? response.statusText, body);
  }
  return new ApiError(response.status, response.statusText);
}

/** What `POST /auth/login` answers with. */
export interface Token {
  token: string;
  expires_at: string;
  user_id: string;
  workspace_id: string;
  role: string;
}

/**
 * Logging in is a hand-written endpoint, so its client is hand-written too.
 *
 * This is the shape of the composition ADR-0028 expects rather than an
 * omission: `/auth/login` is not a table, and the generated client has to sit
 * beside hand-written calls instead of owning the namespace.
 */
export async function login(
  baseUrl: string,
  credentials: { email: string; password: string; workspace?: string },
): Promise<Token> {
  const response = await fetch(`${baseUrl}/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(credentials),
  });
  if (!response.ok) throw await asError(response);
  return (await response.json()) as Token;
}
