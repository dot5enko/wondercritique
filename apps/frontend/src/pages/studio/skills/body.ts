/**
 * Skill body helpers shared by the skills list and editor (plan §4.6).
 * A skill body is just a mounted file tree: `{ files: [{ path, content }] }` —
 * the blob-hash version manifest is built server-side on publish.
 */
import type { SkillBody } from '../../../api/studio'

/** Fresh skill draft: a single SKILL.md stub. */
export function emptySkillBody(name: string): SkillBody {
  return {
    files: [{ path: 'SKILL.md', content: `# ${name}\n\nDescribe when and how to use this skill.\n` }],
  }
}

/** SKILL.md is the skill's entry file — it can't be deleted or renamed. */
export function isSkillEntry(path: string): boolean {
  return path === 'SKILL.md'
}

/** Validates a new/renamed file path; returns an error message or null. */
export function skillPathError(path: string, otherPaths: string[]): string | null {
  const p = path.trim()
  if (!p) return 'Path is required.'
  if (p.startsWith('/') || p.endsWith('/')) return 'No leading or trailing slash.'
  if (p.includes('//')) return 'Empty path segment.'
  if (otherPaths.includes(p)) return 'Another file already uses this path.'
  return null
}
