/** Rule body helpers shared by the rules list and editor (plan §4.6). */
import type { RuleBody } from '../../../api/studio'

/** Fresh rule draft: one empty "General" group. */
export function emptyRuleBody(): RuleBody {
  return { groups: [{ name: 'General', rules: [] }] }
}
