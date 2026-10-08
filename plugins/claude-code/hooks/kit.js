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
//
// THE PIECES (k = kit($.ui.resolve(e), e)):
//   k.surface, k.terminal, k.rich (draws Svg), k.mobile (compact: no fields), k.width
//   k.mark(key?, px?)                      the Beacon mark, or ◆ on the terminal
//   k.pill(text, tone?, key?)              "● SEV2" in its tone; a tinted, unbordered label off the terminal
//   k.segments(items, { key })             a segmented control: [{ id, label, active, onPress }]
//   k.header({ key, title, pills, dim })   mark, title, pills
//   k.button({ key, label, hotkey, onPress, primary, dim, dismiss })
//   k.row(children, key?, gap?) / k.col(children, key?, gap?) / k.text(text, props?)
//   k.spark(values, opts) / k.heat(grid, opts) / k.svg(source, opts)
//   k.avatar(name, here, { key, you, px })  a round initial with a presence dot; "● name" on the terminal
//   k.avatars(people, { key, max })        avatars of people ({ name, here, you }), "+N" past max
//   k.card(children, { key, tone })        a bordered card off the terminal; a plain column on it
//   k.quote(text, key?)                    someone's words: italic off the terminal, dim on it
//   k.dim(text, key?)                      a dim line
//   k.table(rows, { key, widths })         rows of cells (strings or elements) in aligned columns

// The filled segment of a segmented control, off the terminal: ink, and paper on it.
export const INK = '#141414'
export const PAPER = '#fafafa'

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
  // Mobile draws no fields and little width: components draw it compact.
  const mobile = surface === 'mobile'
  const width = Math.max(20, (e.props?.bodyColumns ?? e.viewport?.columns ?? 80) - 2)
  const { Box, Text, Button } = els

  const k = {
    els,
    surface,
    terminal,
    rich,
    mobile,
    width,

    // mark is the Landfall mark: the pixel Beacon on a remote surface, a glyph in a terminal.
    mark(key = 'mark', px = 16) {
      if (rich) return els.Svg({ key, source: markSvg(px), alt: 'Landfall', width: Math.round((px * 16) / 20), height: px })
      return Text({ key, bold: true, children: ['◆'] })
    },

    // pill is a short state word in its tone: "● SEV2". Off the terminal it is
    // a rounded chip around the same text, so a search for the text finds it
    // on every surface.
    // A label is never pressable and never looks it (spec §6, round 0): off the terminal a
    // tinted box (the tone at about 12% over the paper), no border, no key chip, the weight
    // and padding of a Button.
    pill(text, tone = 'neutral', key) {
      const color = TONE[tone] || TONE.neutral
      if (terminal) return Text({ key, color, children: ['● ' + text] })
      const label = Text({ ...(key ? { key: key + '-t' } : {}), color, children: ['● ' + text] })
      return Box({ key, backgroundColor: color + '1f', paddingX: 1, children: [label] })
    },

    // segments is a segmented control (spec §2.2): one row, never wrapped. Each item is
    // { id, label, active, onPress }. The active one is not a Button: inverse bold Text
    // labeled `▸ <label>` on the terminal, a Box filled ink with light bold Text off it. The
    // others are plain Buttons with no hotkey (the label alone, dim), so no key chip and no
    // digit is drawn; `│` separates them on the terminal.
    segments(items, { key = 'seg' } = {}) {
      const kids = []
      items.forEach((it, i) => {
        const id = key + '-' + it.id
        if (terminal && i > 0) kids.push(Text({ key: id + '-sep', dimColor: true, children: ['│'] }))
        if (it.active) {
          if (terminal) kids.push(Text({ key: id, inverse: true, bold: true, children: [' ▸ ' + it.label + ' '] }))
          else kids.push(Box({ key: id, backgroundColor: INK, paddingX: 1, children: [Text({ key: id + '-t', bold: true, color: PAPER, children: ['▸ ' + it.label] })] }))
          return
        }
        const props = { key: id, label: terminal ? ' ' + it.label + ' ' : it.label, dimColor: true, onPress: it.onPress }
        if (terminal) props.plain = true
        kids.push(Button(props))
      })
      return Box({ key, flexDirection: 'row', flexWrap: 'nowrap', columnGap: 0, children: kids })
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
    // `dismiss` marks the one that closes its site (a desktop draws its own
    // close control for it).
    button({ key, label, hotkey, onPress, primary, dim, dismiss }) {
      const props = { key, label, onPress }
      if (hotkey) props.hotkey = hotkey
      if (terminal) {
        props.plain = true
        if (dim) props.dimColor = true
      } else {
        if (primary) props.variant = 'primary'
        if (dismiss) props.role = 'dismiss'
      }
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
      // Untoned charts draw neutral, never info blue (spec §6 Sparklines).
      const tone = opts.tone || 'neutral'
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

    // heat draws a grid of tones (rows of tone names, null for quiet, or
    // 'none' for a cell with no data): full blocks on the terminal, rounded
    // squares elsewhere. A no-data cell is a dim dot on the terminal and an
    // outlined, unfilled square elsewhere, so it never reads as quiet.
    heat(grid, opts = {}) {
      const key = opts.key || 'heat'
      if (!grid || grid.length === 0) return null
      const cols = Math.max(...grid.map((r) => r.length))
      if (rich) {
        const cell = opts.cell || 10
        let s = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + cols * (cell + 2) + ' ' + grid.length * (cell + 2) + '">'
        grid.forEach((r, y) =>
          r.forEach((t, x) => {
            const paint = t === 'none' ? 'fill="none" stroke="#b4b4b0" stroke-dasharray="2 2"' : 'fill="' + (t ? TONE[t] || TONE.neutral : '#e4e4e2') + '"'
            s += '<rect x="' + x * (cell + 2) + '" y="' + y * (cell + 2) + '" width="' + cell + '" height="' + cell + '" rx="2" ' + paint + '><title>' + (opts.titleFor ? esc(opts.titleFor(y, x)) : '') + '</title></rect>'
          }),
        )
        s += '</svg>'
        return els.Svg({ key, source: s, alt: opts.label || 'heat map', width: cols * (cell + 2), height: grid.length * (cell + 2), isInteractive: !!opts.titleFor })
      }
      const words = []
      for (const r of grid) {
        for (let x = 0; x < cols; x++) {
          const t = r[x]
          if (t === 'none') words.push(0x00b7, hexInt('#5a5f73'), DEFAULT_BG)
          else words.push(t ? 0x2588 : 0x25aa, t ? hexInt(TONE[t] || TONE.neutral) : hexInt('#5a5f73'), DEFAULT_BG)
        }
      }
      if (els.Raster) return els.Raster({ key, columns: cols, rows: grid.length, cells: cellsB64(words) })
      return Text({ key, children: [grid.map((r) => r.map((t) => (t === 'none' ? '·' : t ? '█' : '▪')).join('')).join('\n')] })
    },

    // avatar is one person: a round initial in their own hue with a presence
    // dot (green here, grey away) where Svg draws; "● name" / "○ name" on the
    // terminal, green while here.
    avatar(name, here, { key = 'av-' + name, you, px = 22 } = {}) {
      const label = String(name || '?') + (you ? ' (you)' : '')
      if (rich) {
        return els.Svg({ key, source: avatarSvg(name, here, px, you), alt: label + (here ? ', here' : ', away'), width: px, height: px })
      }
      if (here) return Text({ key, color: TONE.good, children: ['● ' + label] })
      return Text({ key, dimColor: true, children: ['○ ' + label] })
    },

    // avatars is a row of people ({ name, here, you }), the ones here first,
    // with "+N" past `max`.
    avatars(people, { key = 'avs', max = 6 } = {}) {
      const list = [...(people || [])].sort((a, b) => Number(!!b.here) - Number(!!a.here))
      if (list.length === 0) return null
      const shown = list.slice(0, max).map((p, i) => k.avatar(p.name, !!p.here, { key: key + '-' + i, you: !!p.you }))
      if (list.length > max) shown.push(Text({ key: key + '-more', dimColor: true, children: ['+' + (list.length - max)] }))
      return Box({ key, flexDirection: 'row', columnGap: rich ? 0 : 2, flexWrap: 'wrap', alignItems: 'center', children: shown })
    },

    // card holds one thing (a room, a vote, a tool's answer) in a rounded
    // border off the terminal; on the terminal it is a plain column, so text
    // there reads as it always has.
    card(children, { key = 'card', tone, gap = 0 } = {}) {
      const kids = (children || []).filter(Boolean)
      if (terminal) return Box({ key, flexDirection: 'column', rowGap: gap, children: kids })
      return Box({
        key,
        flexDirection: 'column',
        rowGap: Math.max(gap, mobile ? 0 : 1),
        borderStyle: 'round',
        borderColor: tone ? TONE[tone] || TONE.neutral : TONE.neutral,
        paddingX: 1,
        children: kids,
      })
    },

    // quote is someone's words.
    quote(text, key = 'quote') {
      if (terminal) return Text({ key, dimColor: true, children: [String(text)] })
      return Text({ key, italic: true, children: [String(text)] })
    },

    dim(text, key = 'dim') {
      return Text({ key, dimColor: true, children: [String(text)] })
    },

    // table lays rows of cells out in columns: a string cell is text, anything
    // else an element. `widths` fixes column widths in cells (the last column
    // takes the rest); left out, each column is as wide as its widest text.
    table(rows, { key = 'table', widths } = {}) {
      if (!rows || rows.length === 0) return null
      const cols = Math.max(...rows.map((r) => r.length))
      const w = []
      for (let c = 0; c < cols; c++) {
        if (widths && widths[c] != null) w.push(widths[c])
        else w.push(Math.max(...rows.map((r) => (typeof r[c] === 'string' ? r[c].length : 4))) + 2)
      }
      return Box({
        key,
        flexDirection: 'column',
        children: rows.map((r, y) =>
          Box({
            key: key + '-r' + y,
            flexDirection: 'row',
            children: r.map((cell, x) =>
              Box({
                key: key + '-r' + y + 'c' + x,
                ...(x === cols - 1 ? { flexShrink: 1 } : { width: w[x], flexShrink: 0 }),
                children: [typeof cell === 'string' ? Text({ key: key + '-t' + y + 'c' + x, children: [cell] }) : cell],
              }),
            ),
          }),
        ),
      })
    },

    // svg draws raw markup on a surface that can, or the alt text where it cannot.
    svg(source, { key = 'svg', alt = '', width, height, hover } = {}) {
      if (rich) return els.Svg({ key, source, alt, width, height, isInteractive: !!hover })
      return Text({ key, dimColor: true, children: [alt] })
    },
  }
  return k
}

// A person's hue, from their name, so the same person reads the same everywhere.
const AVATAR_HUES = ['#2a78d6', '#8a5cd6', '#0e8a7e', '#c2571a', '#b03a78', '#4b6bb0', '#5f8a1e', '#a0662a']

export function avatarSvg(name, here, px = 22, you = false) {
  const n = String(name || '?')
  let h = 0
  for (let i = 0; i < n.length; i++) h = (h * 31 + n.charCodeAt(i)) >>> 0
  const fill = AVATAR_HUES[h % AVATAR_HUES.length]
  const initial = esc((you ? 'Y' : n.trim()[0] || '?').toUpperCase())
  let s = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">'
  s += '<circle cx="12" cy="12" r="10.5" fill="' + fill + '"' + (here ? '' : ' fill-opacity=".45"') + '/>'
  s += '<text x="12" y="16" text-anchor="middle" font-family="-apple-system,Segoe UI,sans-serif" font-size="11" font-weight="600" fill="#ffffff">' + initial + '</text>'
  s += '<circle cx="19.5" cy="19.5" r="3.6" fill="' + (here ? TONE.good : '#9a9a96') + '" stroke="#ffffff" stroke-width="1.4"/>'
  s += '<title>' + esc(n + (here ? ' · here' : ' · away')) + '</title>'
  return s + '</svg>'
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
