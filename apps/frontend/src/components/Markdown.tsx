/**
 * Markdown rendering for agent text (chat transcript, Try-it panels).
 * Built on react-markdown + remark-gfm (tables, strikethrough, task lists,
 * autolinks). Raw HTML is never emitted (no rehype-raw), so untrusted model
 * output stays inert; `urlTransform`'s default allowlist keeps hrefs to
 * http(s)/mailto only.
 *
 * Styling is wrapper-level Tailwind — the component inherits the caller's
 * text color/size and only sets element defaults (spacing, headings, code,
 * lists, tables). Links open in a new tab so a click never leaves the chat.
 */
import { memo } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { cx } from '../ui/cx'

const components: Components = {
  // Open externally — navigating the app away would kill the stream state.
  a: ({ href, children }) => (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="font-medium text-accent-ink underline underline-offset-2"
    >
      {children}
    </a>
  ),
}

/** Block spacing + per-element defaults. Specificity note: `pre code` rules outrank the bare `code` rule, resetting inline styles inside code blocks. */
const markdownClasses =
  'break-words' +
  // top-level block flow
  ' [&>*:first-child]:mt-0 [&>*:last-child]:mb-0 [&>*+*]:mt-2' +
  // headings (modest scale — chat body is 13px)
  ' [&_h1]:mb-1 [&_h1]:mt-4 [&_h1]:text-[15px] [&_h1]:font-semibold [&_h1]:tracking-[-0.01em]' +
  ' [&_h2]:mb-1 [&_h2]:mt-4 [&_h2]:text-sm [&_h2]:font-semibold [&_h2]:tracking-[-0.01em]' +
  ' [&_h3]:mb-1 [&_h3]:mt-3 [&_h3]:text-[13px] [&_h3]:font-semibold' +
  ' [&_h4]:mb-1 [&_h4]:mt-3 [&_h4]:text-[13px] [&_h4]:font-semibold [&_h4]:italic' +
  ' [&_h5]:mb-1 [&_h5]:mt-3 [&_h5]:text-xs [&_h5]:font-semibold [&_h5]:uppercase [&_h5]:tracking-wide [&_h5]:text-ink-2' +
  ' [&_h6]:mb-1 [&_h6]:mt-3 [&_h6]:text-xs [&_h6]:font-semibold [&_h6]:uppercase [&_h6]:tracking-wide [&_h6]:text-ink-2' +
  // paragraphs inside loose list items / blockquotes collapse onto the item spacing
  ' [&_li>p]:my-0 [&_blockquote>p]:my-1' +
  // lists
  ' [&_ul]:my-2 [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:my-2 [&_ol]:list-decimal [&_ol]:pl-5' +
  ' [&_li]:my-1 [&_li>ul]:mb-1 [&_li>ul]:mt-1 [&_li>ol]:mb-1 [&_li>ol]:mt-1' +
  // blockquote
  ' [&_blockquote]:my-2 [&_blockquote]:border-l-2 [&_blockquote]:border-line-strong [&_blockquote]:pl-3 [&_blockquote]:text-ink-2' +
  // inline code vs. code blocks
  ' [&_code]:rounded-sm [&_code]:bg-app [&_code]:px-1 [&_code]:py-px [&_code]:font-mono [&_code]:text-[0.925em]' +
  ' [&_pre]:my-2 [&_pre]:overflow-x-auto [&_pre]:rounded-md [&_pre]:bg-app [&_pre]:p-2.5 [&_pre]:text-xs [&_pre]:leading-relaxed [&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:text-[inherit]' +
  // tables
  ' [&_table]:my-2 [&_table]:w-full [&_table]:border-collapse [&_table]:text-left [&_th]:border [&_th]:border-line [&_th]:bg-app [&_th]:px-2 [&_th]:py-1 [&_th]:font-semibold [&_td]:border [&_td]:border-line [&_td]:px-2 [&_td]:py-1 [&_td]:align-top' +
  // misc
  ' [&_hr]:my-3 [&_hr]:border-line [&_img]:my-2 [&_img]:max-w-full [&_img]:rounded-md'

export const Markdown = memo(function Markdown({
  text,
  className,
}: {
  text: string
  className?: string
}) {
  return (
    <div className={cx(markdownClasses, className)}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  )
})
