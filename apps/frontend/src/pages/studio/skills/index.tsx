/**
 * Studio → Skills (plan §4.6). `?id=` selects the definition editor;
 * without it the list renders.
 */
import { useSearchParams } from 'react-router'
import SkillsEditor from './SkillsEditor'
import SkillsList from './SkillsList'

export default function SkillsPage() {
  const [params] = useSearchParams()
  const id = params.get('id')
  if (!id) return <SkillsList />
  return <SkillsEditor key={id} id={id} />
}
