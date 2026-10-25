import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { CopyField, CodeBlock } from './ui/CopyField';
import { classNames } from '../lib/format';

interface ConnectPanelProps {
  baseUrl: string;
  /** Only present right after key creation; otherwise a placeholder is shown. */
  apiKey?: string;
  modelAlias?: string;
  compact?: boolean;
}

type Lang = 'curl' | 'javascript' | 'python';

const langs: { key: Lang; label: string }[] = [
  { key: 'curl', label: 'cURL' },
  { key: 'javascript', label: 'JavaScript' },
  { key: 'python', label: 'Python' },
];

/**
 * ConnectPanel shows the gateway base URL plus copy-paste client snippets.
 * The URL comes from Settings → API (custom domain) or falls back to the
 * local origin, so the same panel works on localhost and on a VPS.
 */
export function ConnectPanel({ baseUrl, apiKey, modelAlias = 'coding', compact }: ConnectPanelProps) {
  const [lang, setLang] = useState<Lang>('curl');
  const url = (baseUrl || `${window.location.origin}/v1`).replace(/\/+$/, '');
  const key = apiKey || 'mr-sk-...';

  const snippet = useMemo(() => {
    switch (lang) {
      case 'javascript':
        return `import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "${url}",
  apiKey: "${key}",
});

const res = await client.chat.completions.create({
  model: "${modelAlias}",
  messages: [{ role: "user", content: "Hello" }],
});`;
      case 'python':
        return `from openai import OpenAI

client = OpenAI(
    base_url="${url}",
    api_key="${key}",
)

res = client.chat.completions.create(
    model="${modelAlias}",
    messages=[{"role": "user", "content": "Hello"}],
)`;
      default:
        return `curl ${url}/chat/completions \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${modelAlias}","messages":[{"role":"user","content":"Hello"}]}'`;
    }
  }, [lang, url, key, modelAlias]);

  return (
    <div className="flex flex-col gap-3">
      <CopyField label="Gateway base URL" value={url} />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-1 rounded-control border border-border bg-surface p-0.5">
          {langs.map((l) => (
            <button
              key={l.key}
              type="button"
              onClick={() => setLang(l.key)}
              className={classNames(
                'rounded-[6px] px-2 py-1 text-[11.5px] font-medium transition-colors',
                lang === l.key ? 'bg-bg text-text shadow-[0_1px_2px_rgba(0,0,0,0.06)]' : 'text-muted hover:text-text',
              )}
            >
              {l.label}
            </button>
          ))}
        </div>
        {!compact && (
          <Link to="/settings" className="text-[11.5px] font-medium text-accent hover:underline">
            Customize base URL
          </Link>
        )}
      </div>
      <CodeBlock code={snippet} language={lang === 'curl' ? 'bash' : lang} />
      {!apiKey && (
        <p className="text-[11.5px] text-muted">
          Ganti <code className="font-mono">mr-sk-...</code> dengan secret dari menu API Keys (hanya tampil sekali).
        </p>
      )}
    </div>
  );
}

