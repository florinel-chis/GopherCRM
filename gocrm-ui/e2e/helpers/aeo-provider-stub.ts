import { readFileSync } from 'node:fs';
import { createServer, type Server } from 'node:https';
import type { AddressInfo } from 'node:net';

export interface ProviderRequest {
  path: string;
  model: string;
  prompt: string;
  apiKey: string;
}

interface ProviderPayload {
  model: string;
  messages: { content: string | { text: string }[] }[];
}

/** A real TLS peer for the Go SDKs; the application's API is never intercepted. */
export class AEOProviderStub {
  readonly requests: ProviderRequest[] = [];
  rejectGemini = false;
  readonly rejectionMessage = 'Gemini temporarily unavailable in E2E';
  readonly suggestion = 'Which CRM works best for a small sales team?';
  readonly answer = 'E2E GopherCRM is a good choice for a small sales team.';
  private server: Server;
  private origin = '';

  constructor() {
    const certificate = process.env.E2E_AEO_CERT;
    const privateKey = process.env.E2E_AEO_KEY;
    if (!certificate || !privateKey) {
      throw new Error('AEO fixtures require the temporary TLS files supplied by make e2e');
    }
    this.server = createServer(
      { cert: readFileSync(certificate), key: readFileSync(privateKey) },
      async (request, response) => {
        const path = request.url ?? '';
        const anthropic = /^\/anthropic\/(first|second)\/v1\/messages$/.test(path);
        const compatible =
          /^\/(openai|gemini|kimi|perplexity)\/(first|second)\/chat\/completions$/.test(path);
        if (request.method !== 'POST' || (!anthropic && !compatible)) {
          response.writeHead(404).end('Unexpected provider endpoint');
          return;
        }

        try {
          const chunks: Buffer[] = [];
          for await (const chunk of request) chunks.push(Buffer.from(chunk));
          const payload = JSON.parse(Buffer.concat(chunks).toString()) as ProviderPayload;
          const content = payload.messages[0].content;
          const prompt =
            typeof content === 'string' ? content : content.map((block) => block.text).join('');
          this.requests.push({
            path,
            model: payload.model,
            prompt,
            apiKey: anthropic
              ? String(request.headers['x-api-key'] ?? '')
              : String(request.headers.authorization ?? '').replace(/^Bearer /, ''),
          });

          if (this.rejectGemini && path.startsWith('/gemini/')) {
            // Exercise Gemini's nonstandard array envelope and the SDK's real
            // retry behavior, with a short delay.
            response.writeHead(503, {
              'content-type': 'application/json',
              'retry-after-ms': '1',
            });
            response.end(
              JSON.stringify([
                { error: { code: 503, message: this.rejectionMessage, status: 'UNAVAILABLE' } },
              ]),
            );
            return;
          }

          const text = prompt.includes('Reply with a JSON array of strings')
            ? JSON.stringify([this.suggestion])
            : this.answer;
          response.writeHead(200, { 'content-type': 'application/json' });
          response.end(
            JSON.stringify(
              anthropic
                ? {
                    id: 'msg_e2e',
                    type: 'message',
                    role: 'assistant',
                    model: payload.model,
                    content: [{ type: 'text', text }],
                    stop_reason: 'end_turn',
                    usage: { input_tokens: 1, output_tokens: 1 },
                  }
                : {
                    id: 'chatcmpl-e2e',
                    object: 'chat.completion',
                    created: 1,
                    model: payload.model,
                    choices: [
                      {
                        index: 0,
                        message: { role: 'assistant', content: text },
                        finish_reason: 'stop',
                      },
                    ],
                  },
            ),
          );
        } catch {
          response.writeHead(400).end('Malformed provider request');
        }
      },
    );
  }

  async start(): Promise<void> {
    const port = Number(process.env.E2E_AEO_PORT);
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      throw new Error('AEO fixtures require the exact port supplied by make e2e');
    }
    await new Promise<void>((resolve, reject) => {
      this.server.once('error', reject);
      this.server.listen(port, '127.0.0.1', () => {
        this.server.off('error', reject);
        resolve();
      });
    });
    this.origin = `https://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
  }

  baseURL(provider: string, connection: 'first' | 'second' = 'first'): string {
    return `${this.origin}/${provider}/${connection}/`;
  }

  async stop(): Promise<void> {
    await new Promise<void>((resolve, reject) => {
      this.server.close((error) => (error ? reject(error) : resolve()));
      this.server.closeIdleConnections();
    });
  }
}
