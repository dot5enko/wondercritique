import { useState, type KeyboardEvent } from 'react'
import { History, Layers, Plus, Send, SlidersHorizontal } from 'lucide-react'
import { Badge, Button, IconButton, Kbd, NavItem, Select, Sidebar, SidebarSection, StatusDot } from './ui'
import { Showcase, SHOWCASE_NAV } from './showcase'
import type { ShowcaseId } from './showcase'

export default function App() {
  const [section, setSection] = useState<ShowcaseId>('overview')
  const [text, setText] = useState('')
  const [model, setModel] = useState('sonnet')

  const send = () => {
    if (!text.trim()) return
    setText('')
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      send()
    }
  }

  return (
    <div className="h-dvh w-dvw bg-page p-2.5">
      <div className="flex h-full w-full overflow-hidden rounded-xl border border-line bg-surface shadow-xs">
        <Sidebar
          title="Wonder Critique"
          actions={<IconButton icon={<Plus />} label="New session" size="sm" variant="ghost" />}
          footer={
            <div className="flex items-center gap-2 px-1 py-1 text-2xs text-ink-3">
              <StatusDot tone="success" pulse />
              <span className="min-w-0 flex-1 truncate">All systems nominal</span>
              <Badge tone="accent">v0.1</Badge>
            </div>
          }
        >
          <SidebarSection title="Elements">
            {SHOWCASE_NAV.map(({ id, label, icon }) => (
              <NavItem key={id} icon={icon} label={label} active={section === id} onClick={() => setSection(id)} />
            ))}
          </SidebarSection>
          <SidebarSection title="Workspace">
            <NavItem icon={Layers} label="Canvas" />
            <NavItem icon={History} label="History" />
            <NavItem icon={SlidersHorizontal} label="Settings" />
          </SidebarSection>
        </Sidebar>

        <main className="flex min-w-0 flex-1 flex-col">
          <div className="min-h-0 flex-1 overflow-y-auto">
            <div className="mx-auto max-w-3xl px-6 py-6">
              <Showcase section={section} />
            </div>
          </div>

          <div className="shrink-0 border-t border-line bg-surface p-3">
            <div className="rounded-lg border border-line bg-surface transition duration-100 focus-within:border-accent focus-within:ring-2 focus-within:ring-accent/15">
              <textarea
                value={text}
                onChange={(e) => setText(e.target.value)}
                onKeyDown={onKeyDown}
                placeholder="Ask, critique or refactor anything…"
                rows={2}
                className="block w-full resize-none rounded-t-lg bg-transparent px-3 pt-2.5 text-base text-ink outline-none placeholder:text-ink-3"
              />
              <div className="flex items-center justify-between gap-2 px-2 pb-2">
                <Select
                  variant="ghost"
                  size="sm"
                  ariaLabel="Model"
                  value={model}
                  onChange={setModel}
                  options={[
                    { value: 'sonnet', label: 'Claude Sonnet', hint: 'balanced' },
                    { value: 'opus', label: 'Claude Opus', hint: 'deepest' },
                    { value: 'gpt', label: 'GPT-5', hint: 'fast' },
                  ]}
                />
                <div className="flex items-center gap-2.5">
                  <span className="hidden items-center gap-1 text-2xs text-ink-3 sm:flex">
                    <Kbd>↵</Kbd> send
                  </span>
                  <Button variant="primary" size="sm" iconRight={<Send />} onClick={send} disabled={!text.trim()}>
                    Send
                  </Button>
                </div>
              </div>
            </div>
          </div>
        </main>
      </div>
    </div>
  )
}
