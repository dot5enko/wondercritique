/**
 * Agents studio section (plan §4.4): list ↔ editor switched by the `?id=`
 * search param — the back link clears it and returns to the list.
 */
import { useSearchParams } from 'react-router'
import { AgentEditor } from './AgentEditor'
import { AgentsList } from './AgentsList'

export default function AgentsPage() {
  const [params, setParams] = useSearchParams()
  const id = params.get('id')

  if (id) return <AgentEditor id={id} />
  return <AgentsList onOpen={(def) => setParams({ id: def.id })} />
}
