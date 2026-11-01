/**
 * Studio › Model sets (plan §3.4). The `?id=` search param switches between
 * the list page (no id) and a single definition editor (`?id=<defId>`); the
 * model catalog is fetched once here and shared with the editor.
 */
import { useSearchParams } from 'react-router'
import { useAsync } from '../../../hooks'
import { getCatalog } from '../../../api/studio'
import { ModelSetEditor } from './editor'
import { ModelSetsList } from './list'

export default function ModelSetsPage() {
  const [searchParams] = useSearchParams()
  const id = searchParams.get('id')
  const catalog = useAsync(getCatalog, [])

  return id ? <ModelSetEditor key={id} id={id} catalog={catalog} /> : <ModelSetsList />
}
