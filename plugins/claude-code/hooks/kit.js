// The mod's drawing kit: one set of Landfall-branded pieces that draw right on
// every surface the engine has.
//
//   terminal   text, plus Raster cells for sparklines and heat maps
//   desktop    the Code tab of Claude desktop: Svg for charts and the mark
//   vscode     Svg, no Client
//   mobile     Svg and Buttons, no fields
//
// Every piece takes plain data and returns one element (or null), so a
// component writes its tree once and the kit decides how each surface shows
// it. The palette is Landfall's own chart palette (libs/warroom-ui ds tokens):
// monochrome brand, color only for state.
//
// THE SIGNATURES ARE A CONTRACT between components written in parallel: add
// pieces freely, change a signature only with every caller.

export const TONE = {
  critical: '#d03b3b',
  serious: '#ec835a',
  warning: '#fab219',
  good: '#0ca30c',
  info: '#2a78d6',
  violet: '#8a5cd6',
  neutral: '#898781',
}

// The Beacon mark, 16x20 cells (apps/web/public/favicon.svg, cropped).
const MARK_PAL = { a: '#f1c21b', b: '#da1e28', c: '#eef4ff', d: '#04122e', e: '#fa4d56', f: '#0e6027', g: '#24a148' }
const MARK = [
  '................', '......aaaa......', '.....aaaaaa.....', '.....bbbbbb.....', '....bbbbbbbb....',
  '....cccccccc....', '....cdccccdc....', '....cccccccc....', '....cdccccdc....', '....ccddddcc....',
  '...cccccccccc...', '...cccccccccc...', '...eeeeeeeeee...', '...cccccccccc...', '..eeeeeeeeeeee..',
  '..cccccccccccc..', '..eeeeeeeeeeee..', '.ffggggggggggff.', '.fggggggggggggf.', '................',
]

export function markSvg(px = 18) {
  let s = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 20" width="' + Math.round((px * 16) / 20) + '" height="' + px + '" shape-rendering="crispEdges"><rect width="16" height="20" rx="2" fill="#001141"/>'
  MARK.forEach((row, y) => {
    for (let x = 0; x < 16; x++) {
      const c = row[x]
      if (c !== '.') s += '<rect x="' + x + '" y="' + y + '" width="1.02" height="1.02" fill="' + MARK_PAL[c] + '"/>'
    }
  })
  return s + '</svg>'
}

// kit is what a render hook draws with: kit($.ui.resolve(e), e). It takes the
// element table, never `$` (which cannot cross a file).
export function kit(els, e) {
  const surface = e.surface || 'terminal'
  const terminal = surface === 'terminal'
  const rich = !terminal && typeof els.Svg === 'function'
  const width = Math.max(20, (e.props?.bodyColumns ?? e.viewport?.columns ?? 80) - 2)
  const { Box, Text, Button } = els

  const k = {
    els,
    surface,
    terminal,
    rich,
    width,

    // mark is the Landfall mark: the pixel Beacon on a remote surface, a glyph in a terminal.
    mark(key = 'mark', px = 16) {
      if (rich) return els.Svg({ key, source: markSvg(px), alt: 'Landfall', width: Math.round((px * 16) / 20), height: px })
      return Text({ key, bold: true, children: ['◆'] })
    },

    // pill is a short state word in its tone: "● SEV2".
    pill(text, tone = 'neutral', key) {
      const color = TONE[tone] || TONE.neutral
      if (rich) return Text({ key, color, bold: true, children: ['● ' + text] })
      return Text({ key, color, children: ['● ' + text] })
    },

    // header is the branded title row: mark, title, then pills.
    header({ key = 'hdr', title, pills = [], dim }) {
      const kids = [k.mark(key + '-m'), Text({ key: key + '-t', bold: true, children: [title] })]
      pills.forEach((p, i) => kids.push(k.pill(p.text, p.tone, key + '-p' + i)))
      if (dim) kids.push(Text({ key: key + '-d', dimColor: true, children: [dim] }))
      return Box({ key, flexDirection: 'row', columnGap: 1, flexWrap: 'wrap', children: kids })
    },

    // button is a Button that reads as a key on the terminal ("1: catch up")
    // and as a real button elsewhere. `primary` marks the main action.
    button({ key, label, hotkey, onPress, primary, dim }) {
      const props = { key, label, onPress }
      if (hotkey) props.hotkey = hotkey
      if (terminal) {
        props.plain = true
        if (dim) props.dimColor = true
      } else if (primary) props.variant = 'primary'
      return Button(props)
    },

    // row lays children out left to right with a gap.
    row(children, key, gap = 2) {
      return Box({ key, flexDirection: 'row', columnGap: gap, flexWrap: 'wrap', children: children.filter(Boolean) })
    },

    col(children, key, gap = 0) {
      return Box({ key, flexDirection: 'column', rowGap: gap, children: children.filter(Boolean) })
    },

    text(children, props = {}) {
      return Text({ ...props, children: [String(children)] })
    },

    // spark draws values as a sparkline: Raster block cells on the terminal,
    // an Svg line with an area fill elsewhere.
    //   opts.key, opts.width (cells or px), opts.tone, opts.label, opts.mark (index of a marker)
    spark(values, opts = {}) {
      const key = opts.key || 'spark'
      const tone = opts.tone || 'info'
      const vals = (values || []).filter((v) => typeof v === 'number' && isFinite(v))
      if (vals.length < 2) return null
      if (rich) {
        const w = opts.px || 280
        const h = opts.height || 44
        return els.Svg({ key, source: lineSvg(vals, w, h, TONE[tone], opts), alt: opts.label || 'sparkline', width: w, height: h, isInteractive: !!opts.hover })
      }
      const cols = Math.min(opts.width || 24, k.width)
      const series = resample(vals, cols)
      const lo = Math.min(...series)
      const hi = Math.max(...series)
      const words = []
      for (let i = 0; i < series.length; i++) {
        const level = hi === lo ? 0 : Math.round(((series[i] - lo) / (hi - lo)) * 7)
        const color = opts.toneFor ? hexInt(TONE[opts.toneFor(series[i], i)] || TONE[tone]) : hexInt(TONE[tone])
        words.push(BLOCKS[level], color, DEFAULT_BG)
      }
      if (els.Raster) return els.Raster({ key, columns: series.length, rows: 1, cells: cellsB64(words) })
      return Text({ key, children: [series.map((v) => String.fromCodePoint(BLOCKS[hi === lo ? 0 : Math.round(((v - lo) / (hi - lo)) * 7)])).join('')] })
    },

    // heat draws a grid of tones (rows of tone names or null for quiet):
    // full blocks on the terminal, rounded squares elsewhere.
    heat(grid, opts = {}) {
      const key = opts.key || 'heat'
      if (!grid || grid.length === 0) return null
      const cols = Math.max(...grid.map((r) => r.length))
      if (rich) {
        const cell = opts.cell || 10
        let s = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + cols * (cell + 2) + ' ' + grid.length * (cell + 2) + '">'
        grid.forEach((r, y) =>
          r.forEach((t, x) => {
            s += '<rect x="' + x * (cell + 2) + '" y="' + y * (cell + 2) + '" width="' + cell + '" height="' + cell + '" rx="2" fill="' + (t ? TONE[t] : '#e4e4e2') + '"><title>' + (opts.titleFor ? esc(opts.titleFor(y, x)) : '') + '</title></rect>'
          }),
        )
        s += '</svg>'
        return els.Svg({ key, source: s, alt: opts.label || 'heat map', width: cols * (cell + 2), height: grid.length * (cell + 2), isInteractive: !!opts.titleFor })
      }
      const words = []
      for (const r of grid) {
        for (let x = 0; x < cols; x++) {
          const t = r[x]
          words.push(t ? 0x2588 : 0x25aa, t ? hexInt(TONE[t]) : hexInt('#5a5f73'), DEFAULT_BG)
        }
      }
      if (els.Raster) return els.Raster({ key, columns: cols, rows: grid.length, cells: cellsB64(words) })
      return Text({ key, children: [grid.map((r) => r.map((t) => (t ? '█' : '▪')).join('')).join('\n')] })
    },

    // svg draws raw markup on a surface that can, or the alt text where it cannot.
    svg(source, { key = 'svg', alt = '', width, height, hover } = {}) {
      if (rich) return els.Svg({ key, source, alt, width, height, isInteractive: !!hover })
      return Text({ key, dimColor: true, children: [alt] })
    },
  }
  return k
}

const BLOCKS = [0x2581, 0x2582, 0x2583, 0x2584, 0x2585, 0x2586, 0x2587, 0x2588]
const DEFAULT_BG = 0x01000000

function hexInt(hex) {
  return parseInt(String(hex || '#898781').slice(1), 16) & 0xffffff
}

function resample(vals, n) {
  if (vals.length <= n) return vals
  const out = []
  for (let i = 0; i < n; i++) {
    const a = Math.floor((i * vals.length) / n)
    const b = Math.max(a + 1, Math.floor(((i + 1) * vals.length) / n))
    out.push(Math.max(...vals.slice(a, b)))
  }
  return out
}

// cellsB64 packs [codePoint, fg, bg] u32 words, little-endian, as padded base64.
export function cellsB64(words) {
  const bytes = new Uint8Array(words.length * 4)
  words.forEach((w, i) => {
    bytes[i * 4] = w & 0xff
    bytes[i * 4 + 1] = (w >>> 8) & 0xff
    bytes[i * 4 + 2] = (w >>> 16) & 0xff
    bytes[i * 4 + 3] = (w >>> 24) & 0xff
  })
  return b64(bytes)
}

const B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'
function b64(bytes) {
  let out = ''
  for (let i = 0; i < bytes.length; i += 3) {
    const n = (bytes[i] << 16) | ((bytes[i + 1] ?? 0) << 8) | (bytes[i + 2] ?? 0)
    out += B64[(n >> 18) & 63] + B64[(n >> 12) & 63]
    out += i + 1 < bytes.length ? B64[(n >> 6) & 63] : '='
    out += i + 2 < bytes.length ? B64[n & 63] : '='
  }
  return out
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])
}

// lineSvg is a line chart with a faint grid, an area fill, an emphasized end
// point and an optional dashed marker (a deploy) at opts.mark.
export function lineSvg(vals, w, h, color, opts = {}) {
  const lo = opts.min ?? Math.min(...vals)
  const hi = opts.max ?? Math.max(...vals)
  const span = hi - lo || 1
  const n = vals.length
  const pts = vals.map((v, i) => [(i / (n - 1)) * w, h - ((v - lo) / span) * (h - 6) - 3])
  const d = pts.map((p, i) => (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' ')
  let s = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + w + ' ' + h + '">'
  for (let g = 1; g < 4; g++) s += '<line x1="0" x2="' + w + '" y1="' + (h * g) / 4 + '" y2="' + (h * g) / 4 + '" stroke="#8a8a8a" stroke-opacity=".18"/>'
  if (opts.mark != null) {
    const mx = (opts.mark / (n - 1)) * w
    s += '<line x1="' + mx + '" x2="' + mx + '" y1="0" y2="' + h + '" stroke="#8a8a8a" stroke-dasharray="2 3"/>'
  }
  s += '<path d="' + d + ' L' + w + ' ' + h + ' L0 ' + h + ' Z" fill="' + color + '" fill-opacity=".14"/>'
  s += '<path d="' + d + '" fill="none" stroke="' + color + '" stroke-width="1.8" stroke-linejoin="round"/>'
  const last = pts[n - 1]
  s += '<circle cx="' + last[0] + '" cy="' + last[1] + '" r="2.6" fill="' + color + '"/>'
  if (opts.label) s += '<title>' + esc(opts.label) + '</title>'
  return s + '</svg>'
}
