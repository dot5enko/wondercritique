package engine

import (
	"fmt"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
)

// graph is a pipeline body prepared for execution: the non-loop edges form a
// DAG that is executed level by level; the (at most one) loop edge carries the
// gate's feedback into the next iteration.
type graph struct {
	nodes  map[string]studio.PipelineNode
	order  []string                         // body order, for stable output
	levels [][]string                       // node ids by dependency depth
	in     map[string][]studio.PipelineEdge // non-loop incoming edges per node
	loop   *studio.PipelineEdge
}

// buildGraph analyses a pipeline body. Cycles among non-loop edges and edges
// naming unknown nodes are errors (the studio validator catches them at
// publish time; this guards snapshots of drafts).
func buildGraph(body studio.PipelineBody) (*graph, error) {
	if len(body.Nodes) == 0 {
		return nil, fmt.Errorf("the pipeline has no nodes")
	}
	g := &graph{
		nodes: make(map[string]studio.PipelineNode, len(body.Nodes)),
		in:    map[string][]studio.PipelineEdge{},
	}
	for _, n := range body.Nodes {
		if _, dup := g.nodes[n.ID]; dup {
			return nil, fmt.Errorf("duplicate node id %q", n.ID)
		}
		g.nodes[n.ID] = n
		g.order = append(g.order, n.ID)
	}
	for i := range body.Edges {
		e := body.Edges[i]
		if _, ok := g.nodes[e.From.Node]; !ok {
			return nil, fmt.Errorf("edge %q: unknown node %q", e.ID, e.From.Node)
		}
		if _, ok := g.nodes[e.To.Node]; !ok {
			return nil, fmt.Errorf("edge %q: unknown node %q", e.ID, e.To.Node)
		}
		if e.Loop {
			if g.loop != nil {
				return nil, fmt.Errorf("only one loop edge is supported")
			}
			g.loop = &e
			continue
		}
		g.in[e.To.Node] = append(g.in[e.To.Node], e)
	}

	// Kahn's algorithm; depth = longest path from a source.
	indeg := make(map[string]int, len(g.nodes))
	succ := map[string][]string{}
	for id, edges := range g.in {
		for _, e := range edges {
			indeg[id]++
			succ[e.From.Node] = append(succ[e.From.Node], id)
		}
	}
	depth := make(map[string]int, len(g.nodes))
	var queue []string
	for _, id := range g.order {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	visited, maxDepth := 0, 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		visited++
		for _, m := range succ[n] {
			depth[m] = max(depth[m], depth[n]+1)
			maxDepth = max(maxDepth, depth[m])
			if indeg[m]--; indeg[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	if visited != len(g.nodes) {
		return nil, fmt.Errorf("the pipeline contains a cycle (only the gate loop edge may close one)")
	}
	g.levels = make([][]string, maxDepth+1)
	for _, id := range g.order {
		g.levels[depth[id]] = append(g.levels[depth[id]], id)
	}
	return g, nil
}

// nodesOfType returns the ids of nodes of a type in body order.
func (g *graph) nodesOfType(typ string) []string {
	var out []string
	for _, id := range g.order {
		if g.nodes[id].Type == typ {
			out = append(out, id)
		}
	}
	return out
}

// label is the display label of a node (config.label, else the id).
func (g *graph) label(id string) string {
	if l, ok := g.nodes[id].Config["label"].(string); ok && l != "" {
		return l
	}
	return id
}
