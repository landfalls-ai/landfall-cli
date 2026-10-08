// Drives a console tab without the console: a tab is a function from a kit and an `io` to rows, so
// these tests draw it into plain nodes (the same kit the engine draws with), find what a person
// would see by type, key and text, and press it. Module state (the room, the tab's live read) is
// the real thing: core.js and the component are imported here, so what a test sets is what the tab reads.
import { consoleState, room } from '../hooks/core.js'
import { kit } from '../hooks/kit.js'

export type Node = { type: string; key?: string; props: Record<string, any>; children: any[] }

const make =
  (type: string) =>
  (props: Record<string, any> = {}): Node => {
    const { children = [], ...rest } = props
    return { type, key: rest.key, props: rest, children }
  }

export const els = { Box: make('Box'), Text: make('Text'), Button: make('Button'), Svg: make('Svg'), Raster: make('Raster'), Link: make('Link'), Input: make('Input') }

export type Surface = 'terminal' | 'desktop' | 'vscode' | 'mobile'

// The kit a render hook draws with, for a pane `width` cells wide.
export function kitFor(surface: Surface, width = 84) {
  return kit(els as never, { surface, props: { bodyColumns: width + 2 } } as never)
}

export function textOf(n: Node): string {
  if (n.type === 'Button') return String(n.props.label ?? n.children.join(''))
  return n.children.map((c) => (typeof c === 'string' ? c : c && typeof c === 'object' ? textOf(c as Node) : '')).join('')
}

// A string matches the words that contain it, as the engine's own finders do; a RegExp is exact where it anchors.
export type Query = { type?: string; key?: string; text?: string | RegExp }

function matches(n: Node, q: Query): boolean {
  if (q.type && n.type !== q.type) return false
  if (q.key !== undefined && n.key !== q.key) return false
  if (q.text !== undefined) {
    if (n.type !== 'Text' && n.type !== 'Button') return false
    const t = textOf(n)
    if (typeof q.text === 'string' ? !t.includes(q.text) : !q.text.test(t)) return false
  }
  return true
}

export function walk(n: any, fn: (n: Node) => void) {
  if (Array.isArray(n)) return n.forEach((c) => walk(c, fn))
  if (!n || typeof n !== 'object') return
  fn(n)
  for (const c of n.children ?? []) walk(c, fn)
}

export class Drawn {
  constructor(public rows: any) {}
  all(q: Query): Node[] {
    const out: Node[] = []
    walk(this.rows, (n) => {
      if (matches(n, q)) out.push(n)
    })
    return out
  }
  find(q: Query): Node | undefined {
    return this.all(q)[0]
  }
  async press(key: string) {
    const n = this.find({ key })
    if (!n) throw new Error('nothing to press at ' + key)
    await n.props.onPress()
  }
  async input(key: string, text: string) {
    const n = this.find({ key })
    if (!n) throw new Error('no field at ' + key)
    await n.props.onSubmit(text)
  }
  // Every Text's own words, in order: what the tab says.
  words(): string[] {
    const out: string[] = []
    walk(this.rows, (n) => {
      if (n.type === 'Text' || n.type === 'Button') out.push(textOf(n))
    })
    return out
  }
}

export type Fake = ReturnType<typeof fakeIo>

// The `io` register.js hands a component, with every call recorded. `answer` stands in for each
// `landfall <command>`, by its argv after the binary name.
export function fakeIo(answer: (argv: string[]) => unknown = () => ({ ok: false, error: 'unexpected' })) {
  const clock = { t: 1791472800000 }
  const f = {
    clock,
    runs: [] as string[][],
    toasts: [] as string[],
    filled: [] as string[],
    opened: [] as string[],
    appended: [] as string[],
    invalidated: 0,
    surface: 'terminal',
    answer,
    fill: async (text: string) => {
      f.filled.push(text)
    },
    toast: (text: string) => {
      f.toasts.push(text)
    },
    open: async (id: string) => {
      f.opened.push(id)
      return { isPlaced: true }
    },
    close: async () => {},
    invalidate: () => {
      f.invalidated += 1
    },
    run: async (args: string[]) => {
      f.runs.push([...args])
      return f.answer(args) as any
    },
    now: async () => clock.t,
    append: async (text: string) => {
      f.appended.push(text)
      return null
    },
    count: (cmd: string) => f.runs.filter((r) => r[0] === cmd).length,
  }
  return f
}

// Lets reads that were started, not awaited, finish.
export async function settle(n = 6) {
  for (let i = 0; i < n; i++) await new Promise((r) => setTimeout(r, 0))
}

export function setRooms(rooms: unknown[]) {
  room.snapshot = { line: '', rooms: rooms as never }
}

export function openConsoleOn(tab: string, args: string | null = null) {
  consoleState.tab = tab
  consoleState.open = true
  consoleState.args = args
}

export function closeConsole() {
  consoleState.open = false
}
