/**
 * Studio → Rules (plan §4.6). `?id=` selects the definition editor;
 * without it the list renders.
 */
import { useSearchParams } from 'react-router'
import RulesEditor from './RulesEditor'
import RulesList from './RulesList'

export default function RulesPage() {
  const [params] = useSearchParams()
  const id = params.get('id')
  if (!id) return <RulesList />
  return <RulesEditor key={id} id={id} />
}
