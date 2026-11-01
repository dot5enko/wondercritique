import { useState } from 'react'
import {
  Archive,
  Bold,
  ChevronDown,
  Copy,
  Download,
  Inbox,
  Italic,
  MoreHorizontal,
  MousePointerClick,
  PanelsTopLeft,
  Pencil,
  Plus,
  Search,
  Shapes,
  Sparkles,
  TextCursorInput,
  Trash2,
  ChevronsUpDown,
  Underline,
  UnfoldVertical,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import {
  Accordion,
  AccordionItem,
  Avatar,
  Badge,
  Button,
  ButtonGroup,
  Callout,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Checkbox,
  Collapsible,
  EmptyState,
  Field,
  IconButton,
  Input,
  Kbd,
  Menu,
  Progress,
  RadioGroup,
  Segmented,
  Select,
  Shortcut,
  Skeleton,
  Spinner,
  StatusDot,
  Switch,
  Textarea,
  Tooltip,
  cx,
} from './ui'

export type ShowcaseId = 'overview' | 'buttons' | 'forms' | 'dropdowns' | 'cards' | 'expandable' | 'feedback'

export const SHOWCASE_NAV: { id: ShowcaseId; label: string; icon: LucideIcon }[] = [
  { id: 'overview', label: 'Overview', icon: Shapes },
  { id: 'buttons', label: 'Buttons', icon: MousePointerClick },
  { id: 'forms', label: 'Input forms', icon: TextCursorInput },
  { id: 'dropdowns', label: 'Dropdowns', icon: ChevronsUpDown },
  { id: 'cards', label: 'Cards', icon: PanelsTopLeft },
  { id: 'expandable', label: 'Expandable', icon: UnfoldVertical },
  { id: 'feedback', label: 'Feedback', icon: Sparkles },
]

export function Showcase({ section }: { section: ShowcaseId }) {
  return (
    <div key={section} className="animate-rise">
      <PanelSection section={section} />
    </div>
  )
}

function PanelSection({ section }: { section: ShowcaseId }) {
  switch (section) {
    case 'overview':
      return <OverviewPanel />
    case 'buttons':
      return <ButtonsPanel />
    case 'forms':
      return <FormsPanel />
    case 'dropdowns':
      return <DropdownsPanel />
    case 'cards':
      return <CardsPanel />
    case 'expandable':
      return <ExpandablePanel />
    case 'feedback':
      return <FeedbackPanel />
  }
}

/* ---------- shared panel header ---------- */

function Panel({ title, description, children }: { title: string; description: string; children: React.ReactNode }) {
  return (
    <div className="space-y-4">
      <header>
        <h2 className="text-[15px] font-semibold tracking-[-0.01em] text-ink">{title}</h2>
        <p className="mt-0.5 text-base text-ink-2">{description}</p>
      </header>
      {children}
    </div>
  )
}

/* ---------- Overview ---------- */

const SWATCHES: { name: string; cls: string; value: string }[] = [
  { name: 'page', cls: 'bg-page', value: '#ebecee' },
  { name: 'app', cls: 'bg-app', value: '#f6f7f8' },
  { name: 'sunken', cls: 'bg-sunken', value: '#f7f8f9' },
  { name: 'surface', cls: 'bg-surface', value: '#ffffff' },
  { name: 'line', cls: 'bg-line', value: '#e9eaec' },
  { name: 'line-strong', cls: 'bg-line-strong', value: '#d5d8db' },
  { name: 'ink-3', cls: 'bg-ink-3', value: '#9aa0a8' },
  { name: 'ink-2', cls: 'bg-ink-2', value: '#5d636b' },
  { name: 'ink', cls: 'bg-ink', value: '#1b1f23' },
  { name: 'accent-soft', cls: 'bg-accent-soft', value: '#e6f5f8' },
  { name: 'accent', cls: 'bg-accent', value: '#0889a0' },
  { name: 'accent-hover', cls: 'bg-accent-hover', value: '#067085' },
  { name: 'danger', cls: 'bg-danger', value: '#dc2626' },
  { name: 'warning', cls: 'bg-warning', value: '#b45309' },
  { name: 'success', cls: 'bg-success', value: '#15803d' },
]

function OverviewPanel() {
  return (
    <Panel
      title="Overview"
      description="Light, compact and rounded — teal on neutral gray, Linear-style. Every style is a Tailwind utility driven by the tokens in ui/styles.css."
    >
      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader title="Palette" description="Semantic tokens — swap values in @theme to re-skin everything." />
          <CardBody className="grid grid-cols-4 gap-2.5">
            {SWATCHES.map((s) => (
              <div key={s.name} className="min-w-0">
                <div className={cx('h-10 w-full rounded-md ring-1 ring-black/10', s.cls)} />
                <p className="mt-1 truncate text-2xs text-ink-2">{s.name}</p>
                <p className="truncate font-mono text-[10px] text-ink-3">{s.value}</p>
              </div>
            ))}
          </CardBody>
        </Card>
        <div className="space-y-4">
          <Card>
            <CardHeader title="Typography" description="Inter Variable 13px base · JetBrains Mono for code" />
            <CardBody className="space-y-2.5">
              <p className="text-[15px] font-semibold tracking-[-0.01em] text-ink">Heading — 15/600 tight</p>
              <p className="text-base text-ink">Base UI text — 13px/1.45, the workhorse size.</p>
              <p className="text-base text-ink-2">Secondary text — 13px muted.</p>
              <p className="text-xs text-ink-3">Captions &amp; hints — 12px faint.</p>
              <p className="font-mono text-xs text-ink-2">const density = 'editor'</p>
            </CardBody>
          </Card>
          <Card>
            <CardHeader title="Shape & elevation" />
            <CardBody className="flex items-end gap-4">
              {(['rounded-md', 'rounded-lg', 'rounded-xl', 'rounded-full'] as const).map((r) => (
                <div key={r} className="text-center">
                  <div className={cx('size-10 border border-line bg-app', r)} />
                  <p className="mt-1 text-[10px] text-ink-3">{r.replace('rounded-', '')}</p>
                </div>
              ))}
            </CardBody>
          </Card>
        </div>
      </div>
    </Panel>
  )
}

/* ---------- Buttons ---------- */

function ButtonsPanel() {
  return (
    <Panel title="Buttons" description="Compact controls: 24/30/36px heights, 6px radius, 1px borders. Primary carries the teal.">
      <div className="space-y-4">
        <Card>
          <CardHeader title="Variants" />
          <CardBody className="flex flex-wrap items-center gap-2.5">
            <Button variant="primary" icon={<Sparkles />}>
              Primary
            </Button>
            <Button variant="secondary">Secondary</Button>
            <Button variant="soft">Soft</Button>
            <Button variant="ghost">Ghost</Button>
            <Button variant="danger" icon={<Trash2 />}>
              Danger
            </Button>
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Sizes & states" />
          <CardBody className="flex flex-wrap items-center gap-2.5">
            <Button size="sm" variant="primary">
              Small
            </Button>
            <Button size="md" variant="primary">
              Medium
            </Button>
            <Button size="lg" variant="primary">
              Large
            </Button>
            <Button variant="secondary" icon={<Download />} iconRight={<ChevronDown />}>
              Import
            </Button>
            <Button variant="secondary" loading>
              Saving
            </Button>
            <Button variant="primary" disabled>
              Disabled
            </Button>
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Icon buttons & groups" />
          <CardBody className="flex flex-wrap items-center gap-3">
            <IconButton icon={<Plus />} label="Add" variant="secondary" />
            <IconButton icon={<Pencil />} label="Edit" />
            <Tooltip content="Search everything">
              <IconButton icon={<Search />} label="Search" />
            </Tooltip>
            <ButtonGroup>
              <Button variant="ghost" aria-label="Bold" icon={<Bold />} />
              <Button variant="ghost" aria-label="Italic" icon={<Italic />} />
              <Button variant="ghost" aria-label="Underline" icon={<Underline />} />
            </ButtonGroup>
          </CardBody>
        </Card>
      </div>
    </Panel>
  )
}

/* ---------- Forms ---------- */

function FormsPanel() {
  const [lang, setLang] = useState('go')
  const [editor, setEditor] = useState<'light' | 'dark'>('light')
  const [digest, setDigest] = useState(true)
  const [agree, setAgree] = useState(false)
  const [name, setName] = useState('')
  const invalid = name.trim().length === 0

  return (
    <Panel title="Input forms" description="Field wraps any control with label, hint and error wiring via context — no prop drilling.">
      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader title="Text inputs" />
          <CardBody className="space-y-4">
            <Field label="Project" hint="Used as the default branch name.">
              <Input placeholder="wonder-critique" />
            </Field>
            <Field label="Session name" error={invalid ? 'Required — cannot be empty.' : undefined}>
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Refactor auth flow" />
            </Field>
            <Field label="Jump to" optional>
              <Input icon={<Search />} trail={<Kbd>⌘K</Kbd>} placeholder="Files, symbols, settings…" />
            </Field>
            <Field label="Config" hint="Raw YAML, monospace on purpose.">
              <Textarea mono defaultValue={'model: sonnet\ntemperature: 0.2'} />
            </Field>
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Choices" />
          <CardBody className="space-y-4">
            <Field label="Language">
              <RadioGroup
                options={[
                  { value: 'go', label: 'Go', hint: 'Backend services' },
                  { value: 'ts', label: 'TypeScript', hint: 'Frontend app' },
                  { value: 'py', label: 'Python', hint: 'Scripts & tooling' },
                ]}
                value={lang}
                onChange={setLang}
              />
            </Field>
            <div className="flex items-center justify-between">
              <span className="text-base text-ink">Email digest</span>
              <Switch checked={digest} onChange={setDigest} aria-label="Email digest" />
            </div>
            <Checkbox checked={agree} onChange={(e) => setAgree(e.target.checked)} label="I read the changelog" />
            <Field label="Editor">
              <Segmented
                options={[
                  { value: 'light', label: 'Light' },
                  { value: 'dark', label: 'Dark' },
                ]}
                value={editor}
                onChange={setEditor}
              />
            </Field>
          </CardBody>
        </Card>
      </div>
    </Panel>
  )
}

/* ---------- Dropdowns ---------- */

const LANGS = [
  { value: 'ts', label: 'TypeScript', hint: 'strict' },
  { value: 'go', label: 'Go', hint: 'fast' },
  { value: 'py', label: 'Python', hint: 'batteries' },
  { value: 'rs', label: 'Rust', hint: 'fearless' },
]

function DropdownsPanel() {
  const [lang, setLang] = useState<string | null>('ts')
  return (
    <Panel title="Dropdowns" description="Select is a keyboard-navigable listbox; Menu is an action popover. Both close on Escape or outside click.">
      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader title="Select" />
          <CardBody className="flex flex-col gap-3">
            <Select options={LANGS} value={lang} onChange={setLang} ariaLabel="Language" />
            <Select size="lg" options={LANGS} value={lang} onChange={setLang} ariaLabel="Language large" />
            <Select size="sm" variant="ghost" options={LANGS} value={lang} onChange={setLang} ariaLabel="Language ghost" />
            <Select disabled options={LANGS} value={null} onChange={() => {}} placeholder="Disabled" />
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Menu" description="Any trigger; items support icons, shortcuts and danger." />
          <CardBody className="flex items-center gap-3">
            <Menu
              trigger={
                <Button variant="secondary" iconRight={<ChevronDown />}>
                  Actions
                </Button>
              }
            >
              <Menu.Label>File</Menu.Label>
              <Menu.Item icon={Pencil} shortcut="⌘E">
                Edit
              </Menu.Item>
              <Menu.Item icon={Copy} shortcut="⌘D">
                Duplicate
              </Menu.Item>
              <Menu.Separator />
              <Menu.Item icon={Archive}>Archive</Menu.Item>
              <Menu.Separator />
              <Menu.Item icon={Trash2} danger shortcut="⌫">
                Delete
              </Menu.Item>
            </Menu>
            <Menu
              align="end"
              trigger={<IconButton icon={<MoreHorizontal />} label="More" variant="secondary" />}
            >
              <Menu.Item shortcut="⌘R">Rename</Menu.Item>
              <Menu.Item danger>Remove</Menu.Item>
            </Menu>
          </CardBody>
        </Card>
      </div>
    </Panel>
  )
}

/* ---------- Cards ---------- */

function CardsPanel() {
  return (
    <Panel title="Cards" description="Hairline borders on flat surfaces; header and footer rails give structure without weight.">
      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader
            title="Usage report"
            description="Last 7 days"
            actions={
              <Button size="sm" variant="soft">
                Export
              </Button>
            }
          />
          <CardBody className="space-y-3">
            <div className="flex items-baseline justify-between">
              <span className="text-base text-ink-2">Reviews</span>
              <span className="text-xl font-semibold text-ink">
                1,284 <Badge tone="success">+12%</Badge>
              </span>
            </div>
            <Progress value={64} />
            <p className="text-xs text-ink-3">64% of quota used this cycle.</p>
          </CardBody>
          <CardFooter>
            <span className="text-xs text-ink-3">Updated 2m ago</span>
            <Button size="sm" variant="ghost">
              Details
            </Button>
          </CardFooter>
        </Card>
        <div className="space-y-3">
          <Callout tone="info" title="Heads up">
            Composer drafts persist per session, not per tab.
          </Callout>
          <Callout tone="warning" title="Rate limit">
            The LLM provider throttles at 60 rpm on the free tier.
          </Callout>
          <Callout tone="danger" title="Migration needed">
            Run <code className="font-mono text-xs">task db:migrate</code> before first boot.
          </Callout>
          <Callout tone="success" title="Shipped">
            Design system is live on all surfaces.
          </Callout>
        </div>
      </div>
      <Card className="mt-4">
        <CardBody>
          <EmptyState
            icon={Inbox}
            title="No sessions yet"
            description="Start a conversation — critiques land here as they finish."
            action={
              <Button size="sm" variant="primary" icon={<Plus />}>
                New session
              </Button>
            }
          />
        </CardBody>
      </Card>
    </Panel>
  )
}

/* ---------- Expandable ---------- */

function ExpandablePanel() {
  const [open, setOpen] = useState(true)
  return (
    <Panel title="Expandable zones" description="Collapsible and Accordion animate on a 0fr → 1fr grid row — no JS measuring.">
      <div className="space-y-4">
        <Collapsible title="Session details" open={open} onOpenChange={setOpen} trail={<Badge tone="accent">controlled</Badge>}>
          <p>Anything inside collapses to zero height and springs back — the header stays put.</p>
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="secondary">
              Cancel
            </Button>
            <Button size="sm" variant="primary">
              Apply
            </Button>
          </div>
        </Collapsible>
        <Collapsible title="Environment" defaultOpen trail={<span className="text-xs text-ink-3">2 vars</span>}>
          <pre className="font-mono text-xs leading-relaxed text-ink-2">
            {'DATABASE_URL=postgres://…\nMODEL=gpt-5'}
          </pre>
        </Collapsible>
        <Accordion defaultOpen={['tokens']}>
          <AccordionItem id="tokens" title="How are tokens themed?">
            Every component reads semantic colors (--color-accent, --color-line…) declared in @theme inside
            ui/styles.css. Re-skinning means editing one file.
          </AccordionItem>
          <AccordionItem id="dark" title="Is dark mode included?">
            Not yet — the system is light-only by design. With tokens centralized, a dark pass is mechanical later.
          </AccordionItem>
          <AccordionItem id="icons" title="Where do icons come from?">
            lucide-react, tree-shaken per import — no hand-drawn SVGs in the codebase.
          </AccordionItem>
        </Accordion>
      </div>
    </Panel>
  )
}

/* ---------- Feedback ---------- */

function FeedbackPanel() {
  return (
    <Panel title="Feedback & bits" description="Small primitives that carry status, keys and loading states.">
      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader title="Badges, dots & avatars" />
          <CardBody className="space-y-3">
            <div className="flex flex-wrap gap-2">
              <Badge>default</Badge>
              <Badge tone="accent">teal</Badge>
              <Badge tone="success" dot>
                passing
              </Badge>
              <Badge tone="warning">throttled</Badge>
              <Badge tone="danger" dot>
                failed
              </Badge>
            </div>
            <div className="flex items-center gap-3 text-base text-ink-2">
              <StatusDot tone="accent" pulse /> running
              <StatusDot tone="success" /> idle
              <StatusDot tone="danger" pulse /> error
            </div>
            <div className="flex items-center gap-2">
              <Avatar name="Sergio Vanta" />
              <Avatar name="Ada Lovelace" />
              <Avatar name="K" size="md" />
            </div>
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Keys & loading" />
          <CardBody className="space-y-3">
            <div className="flex flex-wrap items-center gap-2 text-base text-ink-2">
              <Shortcut keys={['mod', 'K']} /> command palette
              <Shortcut keys={['shift', 'enter']} /> newline
            </div>
            <div className="flex items-center gap-2">
              <Button variant="primary" loading>
                Generating
              </Button>
              <Spinner />
            </div>
            <Progress value={72} />
            <div className="space-y-2">
              <Skeleton className="h-3 w-3/4" />
              <Skeleton className="h-3 w-1/2" />
            </div>
          </CardBody>
        </Card>
      </div>
    </Panel>
  )
}
