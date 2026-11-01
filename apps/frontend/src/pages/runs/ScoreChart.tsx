/**
 * Tiny inline SVG step-line of best-score-per-iteration (plan §5) — no chart
 * lib. Fixed pixel size so the 11px (text-2xs) axis labels keep their true
 * size; the selected ("current") iteration dot is highlighted in the accent
 * color and clicking a dot selects that iteration.
 */
export interface ScorePoint {
  iteration: number
  score: number
}

export interface ScoreChartProps {
  points: ScorePoint[]
  /** Iteration to highlight (the selected / best one). */
  highlight?: number | null
  onSelect?: (iteration: number) => void
  className?: string
}

const H = 96
const PAD_L = 40
const PAD_R = 16
const PAD_T = 16
const PAD_B = 22

export function ScoreChart({ points, highlight = null, onSelect, className }: ScoreChartProps) {
  if (points.length === 0) return null

  const n = points.length
  const w = Math.min(560, Math.max(240, 64 + n * 48))
  const scores = points.map((p) => p.score)
  const lo0 = Math.min(...scores)
  const hi0 = Math.max(...scores)
  const spread = hi0 - lo0
  const lo = spread === 0 ? lo0 - 0.5 : lo0 - spread * 0.12
  const hi = spread === 0 ? hi0 + 0.5 : hi0 + spread * 0.12

  const x = (i: number): number =>
    n === 1 ? PAD_L + (w - PAD_L - PAD_R) / 2 : PAD_L + (i * (w - PAD_L - PAD_R)) / (n - 1)
  const y = (v: number): number => PAD_T + (1 - (v - lo) / (hi - lo)) * (H - PAD_T - PAD_B)

  const line = points.map((p, i) => `${x(i).toFixed(1)},${y(p.score).toFixed(1)}`).join(' ')
  const fmt = (v: number): string => (Number.isInteger(v) ? String(v) : v.toFixed(2))
  const showXLabels = n <= 24

  return (
    <svg
      width={w}
      height={H}
      role="img"
      aria-label="Best score per iteration"
      className={className}
    >
      {/* y-axis labels (2xs muted) */}
      <text x={4} y={PAD_T + 4} style={{ fontSize: 11, fill: 'var(--color-ink-3)' }}>
        {fmt(hi)}
      </text>
      <text x={4} y={H - PAD_B + 4} style={{ fontSize: 11, fill: 'var(--color-ink-3)' }}>
        {fmt(lo)}
      </text>

      {/* baseline hairline */}
      <line
        x1={PAD_L}
        y1={H - PAD_B}
        x2={w - PAD_R}
        y2={H - PAD_B}
        stroke="var(--color-line)"
        strokeWidth={1}
      />

      <polyline
        points={line}
        fill="none"
        stroke="var(--color-accent)"
        strokeWidth={1.5}
        strokeLinejoin="round"
        strokeLinecap="round"
      />

      {points.map((p, i) => {
        const isHi = p.iteration === highlight
        return (
          <g
            key={p.iteration}
            onClick={onSelect ? () => onSelect(p.iteration) : undefined}
            className={onSelect ? 'cursor-pointer' : undefined}
          >
            <title>{`Iteration ${p.iteration} · score ${fmt(p.score)}`}</title>
            {/* generous invisible hit area */}
            <circle cx={x(i)} cy={y(p.score)} r={10} fill="transparent" />
            {isHi && (
              <circle cx={x(i)} cy={y(p.score)} r={6.5} fill="var(--color-accent)" opacity={0.2} />
            )}
            <circle
              cx={x(i)}
              cy={y(p.score)}
              r={isHi ? 4 : 3}
              fill={isHi ? 'var(--color-accent)' : 'var(--color-ink-3)'}
            />
            {showXLabels && (
              <text
                x={x(i)}
                y={H - PAD_B + 14}
                textAnchor="middle"
                style={{ fontSize: 11, fill: isHi ? 'var(--color-ink)' : 'var(--color-ink-3)' }}
              >
                {p.iteration}
              </text>
            )}
          </g>
        )
      })}
    </svg>
  )
}
