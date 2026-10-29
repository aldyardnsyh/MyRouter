import { useCallback, useEffect, useRef, useState } from 'react';
import {
  AlertTriangle,
  BookOpen,
  Link2,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Sparkles,
  Trash2,
} from 'lucide-react';
import {
  PageHeader,
  StatCard,
  Card,
  EmptyState,
  Callout,
  SkeletonCard,
} from '../components/ui/Layout';
import { Button } from '../components/ui/Button';
import { Badge } from '../components/ui/Badge';
import { Drawer, Modal, ConfirmDialog } from '../components/ui/Overlay';
import { Field, Input, Select, Switch, Checkbox, Textarea } from '../components/ui/Field';
import { CodeBlock } from '../components/ui/CopyField';
import { useToast } from '../components/ui/Toast';
import { admin, ApiError } from '../lib/api';
import { useQuery } from '../lib/useQuery';
import { relativeTime } from '../lib/format';
import type { Route, Skill, SkillBindings, SkillImportPreview, SkillItem, SkillType } from '../lib/types';

type Tone = 'success' | 'warning' | 'danger' | 'info' | 'neutral' | 'accent';

const SKILL_TYPES: { value: SkillType; label: string; description: string }[] = [
  {
    value: 'prompt',
    label: 'Prompt',
    description: 'Static instructions added to every bound request.',
  },
  {
    value: 'memory',
    label: 'Memory',
    description: 'Recent notes injected so context survives across sessions.',
  },
  {
    value: 'retrieval',
    label: 'Retrieval',
    description: 'Query-ranked knowledge pulled from local files or synced docs.',
  },
  {
    value: 'chat',
    label: 'Chat',
    description: 'Captures past conversations and retrieves the most relevant ones.',
  },
  {
    value: 'vision',
    label: 'Vision',
    description: 'Bridges images to a vision model when the target model cannot see.',
  },
];

const SKILL_TYPE_META: Record<SkillType, { label: string; tone: Tone; description: string }> = {
  prompt: { label: 'Prompt', tone: 'accent', description: SKILL_TYPES[0].description },
  memory: { label: 'Memory', tone: 'info', description: SKILL_TYPES[1].description },
  retrieval: { label: 'Retrieval', tone: 'success', description: SKILL_TYPES[2].description },
  chat: { label: 'Chat', tone: 'info', description: SKILL_TYPES[3].description },
  vision: { label: 'Vision', tone: 'warning', description: SKILL_TYPES[4].description },
};

// Names seeded by the backend (SeedBuiltinSkills). The API exposes no builtin
// flag, so this only drives a label and a stronger delete warning; every delete
// still asks for confirmation.
const BUILTIN_NAMES = new Set(['xerj', 'ai-memory', 'chat-knowledge', 'vision', 'core', 'memory']);

// Per-type starting config for a newly created skill.
const TYPE_DEFAULTS: Record<SkillType, Record<string, unknown>> = {
  prompt: { prompt: '' },
  memory: { limit: 10, max_chars: 2000 },
  retrieval: { sync_paths: [], sync_glob: '*.md', top_k: 6, max_chars: 4000 },
  chat: { limit: 5, max_chars: 3000, capture_max_chars: 4000, capture_keep: 100 },
  vision: { vision_route: 'auto', max_images: 3, vision_prompt: '', max_chars: 2000 },
};

function errorMessage(err: unknown, fallback: string): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

function cfgString(config: Record<string, unknown>, key: string, fallback = ''): string {
  const value = config[key];
  return typeof value === 'string' ? value : fallback;
}

function cfgNumber(config: Record<string, unknown>, key: string, fallback: number): number {
  const value = config[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback;
}

// sync_paths is a string[] in the contract; a single legacy string is tolerated
// so an older skill still renders its paths instead of showing an empty field.
function cfgPaths(config: Record<string, unknown>): string[] {
  const value = config.sync_paths;
  if (Array.isArray(value)) return value.filter((v): v is string => typeof v === 'string');
  if (typeof value === 'string' && value.trim()) return [value];
  return [];
}

function hasSyncPaths(skill: Skill): boolean {
  return cfgPaths(skill.config).some((path) => path.trim() !== '');
}

function isBuiltin(skill: Skill): boolean {
  return BUILTIN_NAMES.has(skill.name.trim().toLowerCase());
}

function bindingSummary(skill: Skill): string {
  if (skill.bindings?.global) return 'All models';
  const count = skill.bindings?.routes?.length ?? 0;
  return `${count} route${count === 1 ? '' : 's'}`;
}

interface SkillCardProps {
  skill: Skill;
  toggling: boolean;
  syncing: boolean;
  onToggle: (skill: Skill) => void;
  onEdit: (skill: Skill) => void;
  onKnowledge: (skill: Skill) => void;
  onSync: (skill: Skill) => void;
  onDelete: (skill: Skill) => void;
}

function SkillCard({ skill, toggling, syncing, onToggle, onEdit, onKnowledge, onSync, onDelete }: SkillCardProps) {
  const meta = SKILL_TYPE_META[skill.type] ?? { label: skill.type, tone: 'neutral' as const };
  const canSync = hasSyncPaths(skill);
  const builtin = isBuiltin(skill);

  return (
    <Card className={skill.enabled ? undefined : 'opacity-80'}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="min-w-0 truncate text-[14px] font-semibold text-text">{skill.name}</h3>
            <Badge tone={meta.tone} dot={false}>
              {meta.label}
            </Badge>
            {builtin && (
              <Badge tone="neutral" dot={false}>
                Built-in
              </Badge>
            )}
            {!skill.will_inject && (
              <Badge tone="warning" dot={false}>
                Kosong — tidak akan diinject
              </Badge>
            )}
          </div>
          <p className="mt-1 text-[12.5px] leading-relaxed text-muted">{skill.description || 'No description'}</p>
        </div>
        <Switch
          size="sm"
          checked={skill.enabled}
          disabled={toggling}
          onChange={() => onToggle(skill)}
          label={`Toggle ${skill.name}`}
        />
      </div>

      <dl className="mt-3 grid grid-cols-2 gap-x-3 gap-y-2 border-t border-border pt-3 text-[12px]">
        <div className="min-w-0">
          <dt className="text-[10.5px] uppercase tracking-wide text-muted">Bindings</dt>
          <dd className="mt-0.5 truncate font-medium text-text">{bindingSummary(skill)}</dd>
        </div>
        <div className="min-w-0">
          <dt className="text-[10.5px] uppercase tracking-wide text-muted">Knowledge</dt>
          <dd className="mt-0.5 truncate font-medium text-text">
            {skill.item_count} item{skill.item_count === 1 ? '' : 's'}
          </dd>
        </div>
        <div className="col-span-2 min-w-0">
          <dt className="text-[10.5px] uppercase tracking-wide text-muted">Sync</dt>
          <dd className="mt-0.5">
            {skill.last_sync_error ? (
              <span className="line-clamp-2 break-words text-danger" title={skill.last_sync_error}>
                Sync failed: {skill.last_sync_error}
              </span>
            ) : skill.last_sync_at ? (
              <span className="text-success">
                Synced {relativeTime(skill.last_sync_at)} - {skill.last_sync_items} item
                {skill.last_sync_items === 1 ? '' : 's'}
              </span>
            ) : (
              <span className="text-muted">Never synced</span>
            )}
          </dd>
        </div>
      </dl>

      <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-border pt-3">
        <Button variant="secondary" size="compact" icon={<Pencil size={13} />} onClick={() => onEdit(skill)}>
          Edit
        </Button>
        <Button variant="secondary" size="compact" icon={<BookOpen size={13} />} onClick={() => onKnowledge(skill)}>
          Knowledge
        </Button>
        <Button
          variant="secondary"
          size="compact"
          icon={<RefreshCw size={13} />}
          loading={syncing}
          disabled={!canSync}
          onClick={() => onSync(skill)}
          title={
            canSync
              ? 'Read the configured sync paths and refresh knowledge items'
              : 'Add sync paths in the skill config to enable local sync'
          }
          className={canSync ? undefined : 'opacity-60'}
        >
          Sync now
        </Button>
        {builtin ? (
          <Button
            variant="ghost"
            size="compact"
            disabled
            title="Built-in skills cannot be deleted. Disable it instead."
            className="ml-auto opacity-60"
          >
            Built-in - disable instead
          </Button>
        ) : (
          <Button
            variant="ghost"
            size="compact"
            icon={<Trash2 size={13} />}
            onClick={() => onDelete(skill)}
            className="ml-auto text-danger hover:bg-danger-soft hover:text-danger"
          >
            Delete
          </Button>
        )}
      </div>
    </Card>
  );
}

interface SkillEditorDrawerProps {
  skill: Skill | null;
  routes: Route[];
  onClose: () => void;
  onSaved: () => void;
}

function SkillEditorDrawer({ skill, routes, onClose, onSaved }: SkillEditorDrawerProps) {
  const toast = useToast();
  const [name, setName] = useState(skill?.name ?? '');
  const [description, setDescription] = useState(skill?.description ?? '');
  const [type, setType] = useState<SkillType>(skill?.type ?? 'retrieval');
  const [enabled, setEnabled] = useState(skill?.enabled ?? true);
  const [config, setConfig] = useState<Record<string, unknown>>(() =>
    skill ? { ...skill.config } : { ...TYPE_DEFAULTS.retrieval },
  );
  const [bindings, setBindings] = useState<SkillBindings>(() => ({
    global: skill?.bindings?.global ?? true,
    routes: [...(skill?.bindings?.routes ?? [])],
  }));
  const [rawOpen, setRawOpen] = useState(false);
  const [rawText, setRawText] = useState('');
  const [rawError, setRawError] = useState('');
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState('');

  const meta = SKILL_TYPE_META[type] ?? { label: type, tone: 'neutral' as const };

  function patchConfig(key: string, value: unknown) {
    setConfig((prev) => ({ ...prev, [key]: value }));
  }

  function changeType(next: SkillType) {
    setType(next);
    // Only the create flow allows changing the type. Reset to that type's
    // defaults so a prompt config does not leak into a retrieval skill.
    setConfig({ ...TYPE_DEFAULTS[next] });
    setRawError('');
    if (rawOpen) setRawText(JSON.stringify(TYPE_DEFAULTS[next], null, 2));
  }

  function toggleRaw() {
    if (rawOpen) {
      setRawOpen(false);
      return;
    }
    setRawText(JSON.stringify(config, null, 2));
    setRawError('');
    setRawOpen(true);
  }

  function changeRaw(text: string) {
    setRawText(text);
    if (!text.trim()) {
      setRawError('Config must be a JSON object (use {} for an empty config).');
      return;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch (err) {
      setRawError(err instanceof Error ? err.message : 'Invalid JSON');
      return;
    }
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      setRawError('Config must be a JSON object, not an array or primitive.');
      return;
    }
    setRawError('');
    setConfig(parsed as Record<string, unknown>);
  }

  function updateRoute(alias: string, checked: boolean) {
    setBindings((prev) => ({
      ...prev,
      routes: checked ? [...prev.routes.filter((a) => a !== alias), alias] : prev.routes.filter((a) => a !== alias),
    }));
  }

  async function save() {
    setFormError('');
    const trimmedName = name.trim();
    if (!trimmedName) {
      setFormError('Name is required.');
      return;
    }
    if (rawError) {
      setRawOpen(true);
      setFormError(`Raw JSON config is not valid: ${rawError}`);
      return;
    }
    const nextConfig: Record<string, unknown> = { ...config };
    if (type === 'retrieval') {
      nextConfig.sync_paths = cfgPaths(nextConfig)
        .map((path) => path.trim())
        .filter(Boolean);
    }

    setSaving(true);
    try {
      if (skill) {
        await admin.skills.update(skill.id, {
          name: trimmedName,
          description: description.trim(),
          enabled,
          config: nextConfig,
        });
        try {
          await admin.skills.setBindings(skill.id, bindings);
        } catch (err) {
          toast.error(`Skill saved, but bindings failed: ${errorMessage(err, 'request failed')}`);
          onSaved();
          return;
        }
        toast.success('Skill updated');
      } else {
        const created = await admin.skills.create({
          name: trimmedName,
          description: description.trim(),
          type,
          enabled,
          config: nextConfig,
        });
        try {
          await admin.skills.setBindings(created.data.id, bindings);
        } catch (err) {
          toast.error(`Skill created, but bindings failed: ${errorMessage(err, 'request failed')}`);
        }
        toast.success('Skill created');
      }
      onSaved();
    } catch (err) {
      setFormError(errorMessage(err, 'Could not save skill'));
    } finally {
      setSaving(false);
    }
  }

  function typedFields() {
    switch (type) {
      case 'prompt':
        return (
          <Field
            label="Instructions"
            hint="Added as a system block to every bound request."
            className="sm:col-span-2"
          >
            <Textarea
              rows={6}
              value={cfgString(config, 'prompt')}
              onChange={(e) => patchConfig('prompt', e.target.value)}
              placeholder="Always answer in concise English and cite the files you used."
            />
          </Field>
        );
      case 'retrieval':
        return (
          <>
            <Field
              label="Sync paths"
              hint="One local file or directory per line. Used by Sync now."
              className="sm:col-span-2"
            >
              <Textarea
                rows={4}
                className="font-mono text-[12px]"
                value={cfgPaths(config).join('\n')}
                onChange={(e) => patchConfig('sync_paths', e.target.value.split('\n'))}
                placeholder={'/home/me/notes\n/home/me/project/docs'}
              />
            </Field>
            <Field label="Sync glob" hint="Filter files while syncing, e.g. *.md or *.{go,ts,tsx,py}.">
              <Input
                mono
                value={cfgString(config, 'sync_glob', '*.md')}
                onChange={(e) => patchConfig('sync_glob', e.target.value)}
                placeholder="**.{md,txt}"
              />
            </Field>
            <Field label="Top K" hint="Maximum passages injected per request.">
              <Input
                type="number"
                min={1}
                max={50}
                value={cfgNumber(config, 'top_k', 6)}
                onChange={(e) => patchConfig('top_k', Number(e.target.value))}
              />
            </Field>
            <Field label="Max chars" hint="Character budget for this skill's context block.">
              <Input
                type="number"
                min={200}
                step={100}
                value={cfgNumber(config, 'max_chars', 4000)}
                onChange={(e) => patchConfig('max_chars', Number(e.target.value))}
              />
            </Field>
          </>
        );
      case 'memory':
        return (
          <>
            <Field label="Item limit" hint="Number of newest notes to inject.">
              <Input
                type="number"
                min={1}
                max={200}
                value={cfgNumber(config, 'limit', 10)}
                onChange={(e) => patchConfig('limit', Number(e.target.value))}
              />
            </Field>
            <Field label="Max chars" hint="Character budget for injected notes.">
              <Input
                type="number"
                min={200}
                step={100}
                value={cfgNumber(config, 'max_chars', 2000)}
                onChange={(e) => patchConfig('max_chars', Number(e.target.value))}
              />
            </Field>
          </>
        );
      case 'chat':
        return (
          <>
            <Field label="Retrieve limit" hint="Past conversations to consider per request.">
              <Input
                type="number"
                min={1}
                max={50}
                value={cfgNumber(config, 'limit', 5)}
                onChange={(e) => patchConfig('limit', Number(e.target.value))}
              />
            </Field>
            <Field label="Max chars" hint="Character budget for retrieved conversations.">
              <Input
                type="number"
                min={200}
                step={100}
                value={cfgNumber(config, 'max_chars', 3000)}
                onChange={(e) => patchConfig('max_chars', Number(e.target.value))}
              />
            </Field>
            <Field label="Capture max chars" hint="Longest conversation excerpt stored per turn.">
              <Input
                type="number"
                min={200}
                step={100}
                value={cfgNumber(config, 'capture_max_chars', 4000)}
                onChange={(e) => patchConfig('capture_max_chars', Number(e.target.value))}
              />
            </Field>
            <Field label="Capture keep" hint="Number of recent conversations kept for retrieval.">
              <Input
                type="number"
                min={1}
                max={1000}
                value={cfgNumber(config, 'capture_keep', 100)}
                onChange={(e) => patchConfig('capture_keep', Number(e.target.value))}
              />
            </Field>
          </>
        );
      case 'vision':
        return (
          <>
            <Field label="Vision route" hint="Route alias used to describe images for text-only models.">
              <Select
                value={cfgString(config, 'vision_route', 'auto')}
                onChange={(e) => patchConfig('vision_route', e.target.value)}
              >
                <option value="auto">Auto</option>
                {routes.map((route) => (
                  <option key={route.id} value={route.alias}>
                    {route.alias}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Max images" hint="Images bridged per request.">
              <Input
                type="number"
                min={1}
                max={20}
                value={cfgNumber(config, 'max_images', 3)}
                onChange={(e) => patchConfig('max_images', Number(e.target.value))}
              />
            </Field>
            <Field label="Vision prompt" hint="Instruction sent to the vision model." className="sm:col-span-2">
              <Textarea
                rows={4}
                value={cfgString(config, 'vision_prompt')}
                onChange={(e) => patchConfig('vision_prompt', e.target.value)}
                placeholder="Describe the image in detail. Include all visible text verbatim."
              />
            </Field>
            <Field label="Max chars" hint="Character budget for the image description.">
              <Input
                type="number"
                min={200}
                step={100}
                value={cfgNumber(config, 'max_chars', 2000)}
                onChange={(e) => patchConfig('max_chars', Number(e.target.value))}
              />
            </Field>
          </>
        );
      default:
        return null;
    }
  }

  return (
    <Drawer
      open
      onClose={onClose}
      title={skill ? `Edit skill: ${skill.name}` : 'New skill'}
      description="Context injected automatically into matching model requests."
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} loading={saving}>
            {skill ? 'Save changes' : 'Create skill'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Field label="Name" required hint="Used in previews and applied-skill lists.">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="project-docs" />
          </Field>
          <Field label="Type" hint={meta.description}>
            <Select
              value={type}
              disabled={!!skill}
              onChange={(e) => changeType(e.target.value as SkillType)}
            >
              {SKILL_TYPES.map((entry) => (
                <option key={entry.value} value={entry.value}>
                  {entry.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Description" className="sm:col-span-2">
            <Textarea
              rows={2}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What this skill adds to a request."
            />
          </Field>
          <div className="flex items-start justify-between gap-3 rounded-control border border-border bg-surface px-3 py-2.5 sm:col-span-2">
            <div>
              <p className="text-[13px] font-medium text-text">Enabled</p>
              <p className="text-[11.5px] text-muted">Disabled skills are stored but never injected.</p>
            </div>
            <Switch checked={enabled} onChange={setEnabled} label="Enabled" />
          </div>
        </div>

        <div className="border-t border-border pt-4">
          {!rawOpen ? (
            <>
              <div className="mb-3 flex items-center justify-between gap-2">
                <p className="text-[12.5px] font-semibold uppercase tracking-wide text-muted">
                  {meta.label} options
                </p>
                <Button variant="ghost" size="compact" onClick={toggleRaw}>
                  Edit raw JSON
                </Button>
              </div>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">{typedFields()}</div>
            </>
          ) : (
            <>
              <div className="mb-2 flex items-center justify-between gap-2">
                <p className="text-[12.5px] font-semibold uppercase tracking-wide text-muted">Raw JSON config</p>
                <Button variant="ghost" size="compact" onClick={toggleRaw}>
                  Hide raw JSON
                </Button>
              </div>
              <p className="mb-2 text-[11.5px] text-muted">
                Typed fields are hidden while the raw editor is open. Save is blocked while the JSON is invalid.
              </p>
              <Textarea
                className="min-h-[240px] font-mono text-[12px]"
                spellCheck={false}
                value={rawText}
                onChange={(e) => changeRaw(e.target.value)}
                aria-invalid={rawError ? true : undefined}
              />
              {rawError && <p className="mt-1 text-[11.5px] text-danger">{rawError}</p>}
            </>
          )}
        </div>

        <div className="flex flex-col gap-3 border-t border-border pt-4">
          <div>
            <h3 className="text-[12.5px] font-semibold uppercase tracking-wide text-muted">Bindings</h3>
            <p className="mt-0.5 text-[11.5px] text-muted">
              New skills apply to every model by default. Bind specific routes for narrower targeting.
            </p>
          </div>
          <div className="flex items-start justify-between gap-3 rounded-control border border-border bg-surface px-3 py-2.5">
            <div>
              <p className="text-[13px] font-medium text-text">Apply to all models</p>
              <p className="text-[11.5px] text-muted">Every route alias automatically inherits this skill.</p>
            </div>
            <Switch
              checked={bindings.global}
              onChange={(global) => setBindings((prev) => ({ ...prev, global }))}
              label="Apply to all models"
            />
          </div>
          <div>
            <div className="mb-1.5 flex items-center justify-between gap-2">
              <span className="text-small font-medium text-text">Specific routes</span>
              <span className="text-[11px] text-muted">
                {bindings.global ? 'Overridden by all models' : `${bindings.routes.length} selected`}
              </span>
            </div>
            {routes.length === 0 ? (
              <p className="text-[11.5px] text-muted">No routes defined yet.</p>
            ) : (
              <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2">
                {routes.map((route) => (
                  <Checkbox
                    key={route.id}
                    label={route.alias}
                    checked={bindings.routes.includes(route.alias)}
                    disabled={bindings.global}
                    onChange={(checked) => updateRoute(route.alias, checked)}
                  />
                ))}
              </div>
            )}
          </div>
        </div>

        {formError && <p className="text-[12px] text-danger">{formError}</p>}
      </div>
    </Drawer>
  );
}

interface KnowledgeDrawerProps {
  skill: Skill;
  onClose: () => void;
}

function KnowledgeDrawer({ skill, onClose }: KnowledgeDrawerProps) {
  const toast = useToast();
  const [query, setQuery] = useState('');
  const [items, setItems] = useState<SkillItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [title, setTitle] = useState('');
  const [content, setContent] = useState('');
  const [source, setSource] = useState('');
  const [addError, setAddError] = useState('');
  const [adding, setAdding] = useState(false);
  const [confirmItem, setConfirmItem] = useState<SkillItem | null>(null);
  const [deleting, setDeleting] = useState(false);
  const seq = useRef(0);

  const load = useCallback(
    async (search: string) => {
      const requestId = ++seq.current;
      setLoading(true);
      try {
        const res = await admin.skills.items(skill.id, search.trim() || undefined);
        if (requestId !== seq.current) return;
        setItems(res.data.items ?? []);
        setTotal(res.data.total ?? 0);
        setError('');
      } catch (err) {
        if (requestId !== seq.current) return;
        setError(errorMessage(err, 'Failed to load knowledge items'));
      } finally {
        if (requestId === seq.current) setLoading(false);
      }
    },
    [skill.id],
  );

  // Debounced search: one request per pause instead of one per keystroke.
  useEffect(() => {
    const timer = window.setTimeout(() => {
      void load(query);
    }, 250);
    return () => window.clearTimeout(timer);
  }, [query, load]);

  async function addItem() {
    setAddError('');
    if (!content.trim()) {
      setAddError('Content is required.');
      return;
    }
    setAdding(true);
    try {
      await admin.skills.createItem(skill.id, {
        title: title.trim(),
        content: content.trim(),
        source: source.trim() || undefined,
        kind: 'note',
      });
      setTitle('');
      setContent('');
      setSource('');
      toast.success('Note added');
      await load(query);
    } catch (err) {
      setAddError(errorMessage(err, 'Could not add note'));
    } finally {
      setAdding(false);
    }
  }

  async function deleteItem() {
    if (!confirmItem) return;
    setDeleting(true);
    try {
      await admin.skills.deleteItem(skill.id, confirmItem.id);
      toast.success('Item deleted');
      setConfirmItem(null);
      await load(query);
    } catch (err) {
      toast.error(errorMessage(err, 'Delete failed'));
    } finally {
      setDeleting(false);
    }
  }

  return (
    <Drawer
      open
      onClose={onClose}
      title={`Knowledge: ${skill.name}`}
      description="Notes and docs injected by this skill. Search covers title, source, and content."
      footer={
        <Button variant="secondary" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="rounded-card border border-border bg-surface p-3">
          <p className="text-[12.5px] font-semibold text-text">Add note</p>
          <div className="mt-2 flex flex-col gap-2.5">
            <Field label="Title">
              <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Deploy checklist" />
            </Field>
            <Field label="Content" required>
              <Textarea
                rows={3}
                value={content}
                onChange={(e) => setContent(e.target.value)}
                placeholder="Facts this skill should remember."
              />
            </Field>
            <Field label="Source" hint="Optional origin, e.g. a file path or conversation id.">
              <Input value={source} onChange={(e) => setSource(e.target.value)} placeholder="docs/deploy.md" />
            </Field>
            {addError && <p className="text-[12px] text-danger">{addError}</p>}
            <div className="flex justify-end">
              <Button variant="primary" size="compact" loading={adding} onClick={() => void addItem()}>
                Add note
              </Button>
            </div>
          </div>
        </div>

        <Field label="Search knowledge" hint={`${total} item${total === 1 ? '' : 's'} in this skill`}>
          <Input
            mono
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search title or content"
            prefix={<Search size={14} />}
          />
        </Field>

        {loading && items.length === 0 ? (
          <div className="flex flex-col gap-2" aria-busy="true" aria-label="Loading knowledge items">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-20 animate-pulse rounded-control bg-surface-2" />
            ))}
          </div>
        ) : error ? (
          <Callout tone="danger" icon={<AlertTriangle size={14} />}>
            <div className="flex flex-wrap items-center gap-2">
              <span>{error}</span>
              <button
                type="button"
                onClick={() => void load(query)}
                className="font-medium underline underline-offset-2"
              >
                Retry
              </button>
            </div>
          </Callout>
        ) : items.length === 0 ? (
          <EmptyState
            compact
            icon={<BookOpen size={20} />}
            title={query ? 'No matching items' : 'No knowledge items yet'}
            description={
              query ? 'Try a different search term.' : 'Add a note above, or run Sync now to import files.'
            }
          />
        ) : (
          <ul className="flex flex-col gap-2">
            {items.map((item) => (
              <li key={item.id} className="rounded-control border border-border bg-bg p-3">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge tone="neutral" dot={false}>
                        {item.kind || 'note'}
                      </Badge>
                      {item.source && (
                        <span className="truncate rounded-control border border-border bg-surface px-1.5 py-0.5 font-mono text-[10.5px] text-muted">
                          {item.source}
                        </span>
                      )}
                      <span className="text-[11px] text-muted">{relativeTime(item.created_at)}</span>
                    </div>
                    <p className="mt-1 break-words text-[13px] font-medium text-text">
                      {item.title || 'Untitled'}
                    </p>
                    <p
                      className="mt-1 line-clamp-4 whitespace-pre-wrap break-words text-[12px] leading-relaxed text-muted"
                      title={item.content}
                    >
                      {item.content}
                    </p>
                  </div>
                  <button
                    type="button"
                    onClick={() => setConfirmItem(item)}
                    className="shrink-0 rounded-control p-1 text-muted transition-colors hover:bg-danger-soft hover:text-danger"
                    aria-label={`Delete ${item.title || 'knowledge item'}`}
                  >
                    <Trash2 size={14} />
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>

      <ConfirmDialog
        open={!!confirmItem}
        onClose={() => setConfirmItem(null)}
        onConfirm={() => void deleteItem()}
        title="Delete knowledge item"
        message={`Delete "${confirmItem?.title || 'this item'}"? It will no longer be injected into requests.`}
        confirmLabel="Delete item"
        loading={deleting}
      />
    </Drawer>
  );
}

interface ImportSkillModalProps {
  onClose: () => void;
  onImported: () => void;
}

// Paste a skills.sh (or GitHub) link, preview the parsed SKILL.md, then import
// it as a normal prompt skill. Imported skills use the same edit, knowledge,
// sync, and binding flows as hand-created skills.
function ImportSkillModal({ onClose, onImported }: ImportSkillModalProps) {
  const toast = useToast();
  const [url, setUrl] = useState('');
  const [preview, setPreview] = useState<SkillImportPreview | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [error, setError] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [bindGlobal, setBindGlobal] = useState(true);
  const [importing, setImporting] = useState(false);

  async function fetchPreview() {
    const trimmed = url.trim();
    if (!trimmed) {
      setError('Enter a skills.sh link first.');
      return;
    }
    setError('');
    setPreview(null);
    setPreviewing(true);
    try {
      const res = await admin.skills.importPreview(trimmed);
      setPreview(res.data);
      setName(res.data.name ?? '');
      setDescription(res.data.description ?? '');
    } catch (err) {
      setError(errorMessage(err, 'Could not fetch the skill preview'));
    } finally {
      setPreviewing(false);
    }
  }

  async function runImport() {
    if (!preview) return;
    setError('');
    setImporting(true);
    try {
      const res = await admin.skills.importSkill({
        url: url.trim(),
        ...(name.trim() ? { name: name.trim() } : {}),
        ...(description.trim() ? { description: description.trim() } : {}),
        bind_global: bindGlobal,
      });
      toast.success(`Imported ${res.data.name}`);
      onImported();
    } catch (err) {
      setError(errorMessage(err, 'Could not import the skill'));
    } finally {
      setImporting(false);
    }
  }

  return (
    <Modal
      open
      onClose={importing ? () => {} : onClose}
      title="Import from skills.sh"
      description="Fetch a SKILL.md from skills.sh and create a prompt skill from it."
      size="lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={importing}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void runImport()} loading={importing} disabled={!preview}>
            Import skill
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-2">
          <Field
            label="Skill URL"
            hint="skills.sh link, a GitHub repo URL, or shorthand like owner/repo/skill."
          >
            <Input
              mono
              data-autofocus
              value={url}
              onChange={(e) => {
                setUrl(e.target.value);
                setPreview(null);
                setError('');
              }}
              placeholder="https://skills.sh/owner/repo/skill"
            />
          </Field>
          <div className="flex justify-end">
            <Button
              variant="secondary"
              size="compact"
              loading={previewing}
              disabled={!url.trim()}
              onClick={() => void fetchPreview()}
            >
              Fetch preview
            </Button>
          </div>
        </div>

        {error && (
          <Callout tone="danger" icon={<AlertTriangle size={14} />}>
            {error}
          </Callout>
        )}

        {!preview && !error && (
          <p className="text-[12px] text-muted">
            The skill is fetched over HTTPS from GitHub. Invalid, unsupported, or unsafe links are
            rejected before anything is stored.
          </p>
        )}

        {preview && (
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field label="Name" hint="Defaults to the SKILL.md name.">
                <Input value={name} onChange={(e) => setName(e.target.value)} />
              </Field>
              <Field label="Description" hint="Shown on the skill card.">
                <Input value={description} onChange={(e) => setDescription(e.target.value)} />
              </Field>
            </div>

            <div className="flex flex-col gap-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <p className="text-small font-medium text-text">Content preview</p>
                {preview.truncated && (
                  <Badge tone="warning" dot={false}>
                    Truncated
                  </Badge>
                )}
                <span className="font-mono text-[11px] text-muted">
                  {preview.owner}/{preview.repo} - {preview.path}
                </span>
              </div>
              <CodeBlock
                language={preview.path || 'SKILL.md'}
                code={preview.content}
                className="[&>pre]:max-h-[260px] [&>pre]:overflow-y-auto"
              />
              <a
                href={preview.source_url}
                target="_blank"
                rel="noreferrer"
                className="break-all font-mono text-[11px] text-accent hover:underline"
              >
                {preview.source_url}
              </a>
            </div>

            <div className="flex items-start justify-between gap-3 rounded-control border border-border bg-surface px-3 py-2.5">
              <div>
                <p className="text-[13px] font-medium text-text">Bind to all models</p>
                <p className="text-[11.5px] text-muted">
                  Apply this skill to every route alias. You can narrow the bindings later in Edit.
                </p>
              </div>
              <Switch checked={bindGlobal} onChange={setBindGlobal} label="Bind to all models" />
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}

export function SkillsPage() {
  const toast = useToast();
  const { data: skillsData, loading, error, reload } = useQuery(() => admin.skills.list(), {
    cacheKey: 'skills',
  });
  const { data: routesData } = useQuery(() => admin.routes.list(), { cacheKey: 'routes' });
  const skills = skillsData?.data ?? [];
  const routes = routesData?.data ?? [];

  const [editor, setEditor] = useState<{ skill: Skill | null; seq: number } | null>(null);
  const [knowledgeSkill, setKnowledgeSkill] = useState<Skill | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<Skill | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [togglingId, setTogglingId] = useState<string | null>(null);
  const [syncingId, setSyncingId] = useState<string | null>(null);
  const [importOpen, setImportOpen] = useState(false);

  const activeCount = skills.filter((skill) => skill.enabled).length;
  const totalItems = skills.reduce((sum, skill) => sum + skill.item_count, 0);
  const failedSyncs = skills.filter((skill) => skill.last_sync_error).length;
  const lastSync = skills.reduce((latest, skill) => Math.max(latest, skill.last_sync_at || 0), 0);

  function openCreate() {
    setEditor({ skill: null, seq: Date.now() });
  }

  function openEdit(skill: Skill) {
    setEditor({ skill, seq: Date.now() });
  }

  async function toggleSkill(skill: Skill) {
    setTogglingId(skill.id);
    try {
      await admin.skills.update(skill.id, { enabled: !skill.enabled });
      toast.success(`${skill.name} ${skill.enabled ? 'disabled' : 'enabled'}`);
    } catch (err) {
      toast.error(errorMessage(err, 'Update failed'));
    } finally {
      setTogglingId(null);
    }
  }

  async function syncSkill(skill: Skill) {
    setSyncingId(skill.id);
    try {
      const res = await admin.skills.sync(skill.id);
      const result = res.data;
      if (result.error) {
        toast.error(`Sync failed: ${result.error}`);
      } else {
        const skipped = result.skipped ? `, ${result.skipped} skipped` : '';
        toast.success(`Synced ${result.items} item(s) from ${result.files} file(s)${skipped}`);
      }
    } catch (err) {
      toast.error(errorMessage(err, 'Sync failed'));
    } finally {
      setSyncingId(null);
    }
  }

  async function removeSkill() {
    if (!confirmDelete) return;
    setDeleting(true);
    try {
      await admin.skills.remove(confirmDelete.id);
      toast.success('Skill deleted');
      setConfirmDelete(null);
    } catch (err) {
      toast.error(errorMessage(err, 'Delete failed'));
    } finally {
      setDeleting(false);
    }
  }

  const deleteMessage = confirmDelete
    ? `Delete skill "${confirmDelete.name}" and its ${confirmDelete.item_count} knowledge item(s)? Bound requests will stop receiving this context. This cannot be undone.`
    : '';

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Skills"
        description="Skills are server-side context automatically injected into model requests: instructions, memory, retrieved knowledge, past conversations, and image bridges."
        actions={
          <>
            <Button
              variant="secondary"
              size="compact"
              icon={<Link2 size={14} />}
              onClick={() => setImportOpen(true)}
            >
              Import from skills.sh
            </Button>
            <Button variant="primary" size="compact" icon={<Plus size={14} />} onClick={openCreate}>
              New skill
            </Button>
          </>
        }
      />

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard
          label="Skills"
          value={String(skills.length)}
          hint={`${activeCount} active`}
          icon={<Sparkles size={15} />}
        />
        <StatCard
          label="Active"
          value={String(activeCount)}
          tone="success"
          hint="applied to bound requests"
        />
        <StatCard
          label="Knowledge items"
          value={String(totalItems)}
          hint="notes and retrieved passages"
        />
        <StatCard
          label="Last sync"
          value={lastSync ? relativeTime(lastSync) : 'Never'}
          tone={failedSyncs > 0 ? 'warning' : 'default'}
          hint={failedSyncs > 0 ? `${failedSyncs} skill(s) need attention` : 'local knowledge refresh'}
        />
      </div>

      {error && (
        <Callout tone="danger" icon={<AlertTriangle size={14} />}>
          <div className="flex flex-wrap items-center gap-2">
            <span>Could not load skills: {error}</span>
            <button
              type="button"
              onClick={() => void reload()}
              className="font-medium underline underline-offset-2"
            >
              Retry
            </button>
          </div>
        </Callout>
      )}

      {loading && skills.length === 0 ? (
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2" aria-busy="true" aria-label="Loading skills">
          <SkeletonCard className="h-[220px]" />
          <SkeletonCard className="h-[220px]" />
          <SkeletonCard className="h-[220px]" />
          <SkeletonCard className="h-[220px]" />
        </div>
      ) : skills.length === 0 && !error ? (
        <Card>
          <EmptyState
            icon={<Sparkles size={20} />}
            title="No skills yet"
            description="Create a skill to add instructions, memory, or retrieved knowledge to matching model requests."
            action="New skill"
            onAction={openCreate}
          />
        </Card>
      ) : (
        <div className="grid grid-cols-1 items-start gap-3 lg:grid-cols-2">
          {skills.map((skill) => (
            <SkillCard
              key={skill.id}
              skill={skill}
              toggling={togglingId === skill.id}
              syncing={syncingId === skill.id}
              onToggle={(target) => void toggleSkill(target)}
              onEdit={openEdit}
              onKnowledge={setKnowledgeSkill}
              onSync={(target) => void syncSkill(target)}
              onDelete={setConfirmDelete}
            />
          ))}
        </div>
      )}
      {editor && (
        <SkillEditorDrawer
          key={editor.seq}
          skill={editor.skill}
          routes={routes}
          onClose={() => setEditor(null)}
          onSaved={() => setEditor(null)}
        />
      )}

      {knowledgeSkill && (
        <KnowledgeDrawer
          key={knowledgeSkill.id}
          skill={knowledgeSkill}
          onClose={() => setKnowledgeSkill(null)}
        />
      )}

      {importOpen && (
        <ImportSkillModal
          onClose={() => setImportOpen(false)}
          onImported={() => {
            setImportOpen(false);
            void reload();
          }}
        />
      )}

      <ConfirmDialog
        open={!!confirmDelete}
        onClose={() => setConfirmDelete(null)}
        onConfirm={() => void removeSkill()}
        title="Delete skill"
        message={deleteMessage}
        confirmLabel="Delete skill"
        loading={deleting}
      />
    </div>
  );
}
